package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tim5wang/godex/internal/core/identity"
	"github.com/tim5wang/godex/internal/services/accounts"
	"github.com/tim5wang/godex/internal/services/noderegistry"
)

// registerTenantOwner signs up an owner and returns the session token plus the
// tenant/user ids the delegation surface keys on.
func registerTenantOwner(t *testing.T, server *httptest.Server, email string) (token, tenantID, userID string) {
	t.Helper()
	_, body := postAuth(t, server.URL+"/auth/register", map[string]any{
		"email": email, "password": "correct-horse", "tenant_name": "Acme",
	})
	token, _ = body["token"].(string)
	tenant, _ := body["tenant"].(map[string]any)
	principal, _ := body["principal"].(map[string]any)
	tenantID, _ = tenant["id"].(string)
	userID, _ = principal["user_id"].(string)
	if token == "" || tenantID == "" || userID == "" {
		t.Fatalf("signup did not yield token/tenant/user: %v", body)
	}
	return token, tenantID, userID
}

// TestTenantGrantRevokeRoundTrip is the §3.6 happy path: the tenant creates a
// time-bounded grant, sees it as active, withdraws it, and sees it as not active.
func TestTenantGrantRevokeRoundTrip(t *testing.T) {
	server, _ := newAuthServer(t, "web-secret")
	token, _, _ := registerTenantOwner(t, server, "owner@example.com")

	resp, body := doJSON(t, http.MethodPost, server.URL+"/tenant/delegations", token, map[string]any{
		"operator_user_id": "local-operator", "ttl_hours": 2, "reason": "debug node connectivity",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201: %v", resp.StatusCode, body)
	}
	grant, _ := body["delegation"].(map[string]any)
	id, _ := grant["id"].(string)
	if id == "" {
		t.Fatalf("no delegation id returned: %v", body)
	}
	if grant["active"] != true {
		t.Errorf("a freshly created grant reports active=%v", grant["active"])
	}
	// Read-only is the default and the only scope today; a grant must never come
	// back broader than the tenant asked for.
	if grant["scope"] != identity.ScopeReadNodes {
		t.Errorf("scope = %v, want %v", grant["scope"], identity.ScopeReadNodes)
	}

	resp, body = doJSON(t, http.MethodGet, server.URL+"/tenant/delegations", token, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list status = %d, want 200: %v", resp.StatusCode, body)
	}
	if body["max_ttl_hours"] != float64(int(identity.MaxDelegationTTL.Hours())) {
		t.Errorf("max_ttl_hours = %v, want the server's own cap so the UI cannot advertise more",
			body["max_ttl_hours"])
	}
	list, _ := body["delegations"].([]any)
	if len(list) != 1 {
		t.Fatalf("got %d grants, want 1", len(list))
	}

	resp, body = doJSON(t, http.MethodDelete, server.URL+"/tenant/delegations/"+id, token, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("revoke status = %d, want 200: %v", resp.StatusCode, body)
	}

	_, body = doJSON(t, http.MethodGet, server.URL+"/tenant/delegations", token, nil)
	list, _ = body["delegations"].([]any)
	if len(list) != 1 {
		t.Fatalf("got %d grants after revoke, want the row retained for the record", len(list))
	}
	revoked, _ := list[0].(map[string]any)
	if revoked["active"] != false {
		t.Errorf("revoked grant still reports active=%v", revoked["active"])
	}
	if revoked["revoked_at"] == nil || revoked["revoked_at"] == "" {
		t.Error("revoked_at was not recorded, so the withdrawal is not auditable")
	}
}

// TestTenantGrantRejectsBadWindow: an out-of-range window must be refused rather
// than silently clamped, so a tenant does not believe it granted a longer window
// than it did.
func TestTenantGrantRejectsBadWindow(t *testing.T) {
	server, _ := newAuthServer(t, "web-secret")
	token, _, _ := registerTenantOwner(t, server, "owner@example.com")

	for _, ttl := range []int{-1, int(identity.MaxDelegationTTL.Hours()) + 1} {
		resp, body := doJSON(t, http.MethodPost, server.URL+"/tenant/delegations", token, map[string]any{
			"operator_user_id": "local-operator", "ttl_hours": ttl,
		})
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("ttl_hours=%d status = %d, want 400: %v", ttl, resp.StatusCode, body)
		}
	}

	// A grant that names no operator would be "any operator, any time".
	resp, _ := doJSON(t, http.MethodPost, server.URL+"/tenant/delegations", token, map[string]any{
		"ttl_hours": 2,
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("grant without operator_user_id status = %d, want 400", resp.StatusCode)
	}
}

// TestOperatorOwnViewIsScopedToItself: the operator console must show what it may
// read without becoming a map of who else holds grants.
func TestOperatorOwnViewIsScopedToItself(t *testing.T) {
	server, _ := newAuthServer(t, "web-secret")
	tenantToken, _, _ := registerTenantOwner(t, server, "owner@example.com")

	resp, _ := doJSON(t, http.MethodPost, server.URL+"/tenant/delegations", tenantToken, map[string]any{
		"operator_user_id": "local-operator", "ttl_hours": 1,
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create grant status = %d, want 201", resp.StatusCode)
	}

	// The web token IS the platform operator ("local-operator"), so it is the
	// principal the grant names.
	resp, body := doJSON(t, http.MethodGet, server.URL+"/delegations/mine", "web-secret", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %v", resp.StatusCode, body)
	}
	mine, _ := body["delegations"].([]any)
	if len(mine) != 1 {
		t.Fatalf("operator sees %d grants, want the 1 issued to it", len(mine))
	}

	// A tenant user asking for "my grants" gets its own (none), not the operator's.
	resp, body = doJSON(t, http.MethodGet, server.URL+"/delegations/mine", tenantToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("tenant status = %d, want 200: %v", resp.StatusCode, body)
	}
	if theirs, _ := body["delegations"].([]any); len(theirs) != 0 {
		t.Errorf("a tenant user saw %d operator grants, want 0", len(theirs))
	}
}

// TestOperatorCannotUseTheTenantGrantSurface: the operator has no tenant, so it
// cannot create a grant, list a tenant's grants, or read a tenant's audit trail.
// There is deliberately no route for "grant myself access".
func TestOperatorCannotUseTheTenantGrantSurface(t *testing.T) {
	server, _ := newAuthServer(t, "web-secret")

	resp, body := doJSON(t, http.MethodGet, server.URL+"/tenant/delegations", "web-secret", nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (an operator holds no tenant): %v", resp.StatusCode, body)
	}
	resp, _ = doJSON(t, http.MethodPost, server.URL+"/tenant/delegations", "web-secret", map[string]any{
		"operator_user_id": "local-operator",
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("operator creating a grant status = %d, want 400", resp.StatusCode)
	}
	resp, _ = doJSON(t, http.MethodGet, server.URL+"/tenant/delegations/audit", "web-secret", nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("operator reading the audit trail status = %d, want 400", resp.StatusCode)
	}
}

func TestTenantGrantSurfaceRequiresAuthentication(t *testing.T) {
	server, _ := newAuthServer(t, "web-secret")
	// Only the registered method/path pairs are probed: a method the route does not
	// serve answers 405, which is a different question from authentication.
	for _, probe := range []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/tenant/delegations"},
		{http.MethodPost, "/tenant/delegations"},
		{http.MethodGet, "/tenant/delegations/audit"},
		{http.MethodGet, "/tenant/consent"},
		{http.MethodPost, "/tenant/consent"},
	} {
		resp, _ := doJSON(t, probe.method, server.URL+probe.path, "", nil)
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s %s status = %d, want 401", probe.method, probe.path, resp.StatusCode)
		}
	}
}

// TestSignupRecordsConsent: the disclosure happens at signup, so the trail must be
// written there — without it, "the tenant agreed" is an assumption.
func TestSignupRecordsConsent(t *testing.T) {
	server, accountsService := newAuthServer(t, "web-secret")
	token, tenantID, _ := registerTenantOwner(t, server, "owner@example.com")

	resp, body := doJSON(t, http.MethodGet, server.URL+"/tenant/consent", token, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %v", resp.StatusCode, body)
	}
	records, _ := body["records"].([]any)
	if len(records) < 1 {
		t.Fatalf("signup recorded no consent for tenant %s: %v", tenantID, body)
	}
	first, _ := records[0].(map[string]any)
	if first["terms_version"] != identity.CurrentTermsVersion {
		t.Errorf("terms_version = %v, want %v", first["terms_version"], identity.CurrentTermsVersion)
	}
	if first["source_ip"] == nil || first["source_ip"] == "" {
		t.Error("source_ip was not recorded")
	}
	if body["current_terms_version"] != identity.CurrentTermsVersion {
		t.Errorf("current_terms_version = %v, want %v", body["current_terms_version"], identity.CurrentTermsVersion)
	}

	// A stored row is what the store says, not what the response echoed.
	stored, err := accountsService.ConsentRecords(tenantID)
	if err != nil {
		t.Fatalf("ConsentRecords: %v", err)
	}
	if len(stored) != 1 {
		t.Errorf("store holds %d consent rows, want exactly the 1 from signup", len(stored))
	}
}

func TestConsentRequiresExplicitAcceptance(t *testing.T) {
	server, _ := newAuthServer(t, "web-secret")
	token, _, _ := registerTenantOwner(t, server, "owner@example.com")

	// An unaccepted revision must not produce a record of agreement: that is the
	// one thing a consent trail exists to be able to distinguish.
	resp, _ := doJSON(t, http.MethodPost, server.URL+"/tenant/consent", token, map[string]any{
		"terms_version": "2027-01", "accepted": false,
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for an unaccepted revision", resp.StatusCode)
	}

	resp, body := doJSON(t, http.MethodPost, server.URL+"/tenant/consent", token, map[string]any{
		"terms_version": "2027-01", "accepted": true,
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201: %v", resp.StatusCode, body)
	}
	if body["terms_version"] != "2027-01" {
		t.Errorf("recorded version = %v, want the stated one so the revision stays identified", body["terms_version"])
	}
}

// controlMuxWithAccounts wires the control-plane read routes with a REAL account
// store, so the delegation lookup and the audit write run through production
// wiring rather than a stub.
func controlMuxWithAccounts(registry controlNodeRegistry, accountsService *accounts.Service) *http.ServeMux {
	mux := http.NewServeMux()
	attach := func(handler http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			handler.ServeHTTP(w, r.WithContext(WithPrincipal(r.Context(), principalFromHeader(r))))
		})
	}
	registerControlNodeRoutes(mux, registry, nil, attach, attach,
		NewNodeAccessPolicy(accountsService), accountsService)
	return mux
}

// TestDelegatedReadIsAuditedAndEndsAtRevocation is the §3.6 property end to end:
// an operator reaches a tenant's node only while the grant is live, and every
// delegated read leaves a row the tenant can see.
func TestDelegatedReadIsAuditedAndEndsAtRevocation(t *testing.T) {
	accountsService, err := accounts.New(t.TempDir())
	if err != nil {
		t.Fatalf("accounts.New: %v", err)
	}
	t.Cleanup(func() { _ = accountsService.Close() })

	owner, err := accountsService.Register(accounts.RegisterInput{
		Email: "owner@example.com", Password: "correct-horse", TenantName: "Acme",
	})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	tenantID := owner.Tenant.ID

	registry := &fakeControlRegistry{nodes: []noderegistry.NodeView{
		{ID: "acme-1", TenantID: tenantID, OwnerUserID: owner.User.ID, Visibility: "private"},
		{ID: "other-1", TenantID: "tnt_elsewhere", OwnerUserID: "someone", Visibility: "tenant"},
	}}
	mux := controlMuxWithAccounts(registry, accountsService)

	// Before the grant: the operator must not reach the tenant's node, and it must
	// not be able to reach it "by default" either.
	if ids := getNodeList(t, mux, ""); len(ids) != 0 {
		t.Fatalf("operator saw %v without a delegation, want none", ids)
	}
	if rows, err := accountsService.ListDelegationAudit(tenantID, 100); err != nil || len(rows) != 0 {
		t.Fatalf("audit rows before any delegated read: %v (err=%v), want none", rows, err)
	}

	grant, err := accountsService.CreateDelegation(accounts.CreateDelegationInput{
		TenantID: tenantID, OperatorUserID: "local-operator", GrantedBy: owner.User.ID,
	})
	if err != nil {
		t.Fatalf("CreateDelegation: %v", err)
	}

	ids := getNodeList(t, mux, "")
	if len(ids) != 1 || ids[0] != "acme-1" {
		t.Fatalf("operator saw %v with a grant, want exactly the granted tenant's node", ids)
	}

	rows, err := accountsService.ListDelegationAudit(tenantID, 100)
	if err != nil {
		t.Fatalf("ListDelegationAudit: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d audit rows for the delegated list read, want 1", len(rows))
	}
	if rows[0].DelegationID != grant.ID {
		t.Errorf("audit names grant %q, want %q", rows[0].DelegationID, grant.ID)
	}
	if rows[0].OperatorUserID != "local-operator" || !rows[0].Allowed {
		t.Errorf("audit row = %+v, want the operator and allowed=true", rows[0])
	}
	if rows[0].At == "" {
		t.Error("audit row has no timestamp, so \"when\" is unanswerable")
	}

	// A single-node read is a separate act and must be audited too.
	req := httptest.NewRequest(http.MethodGet, "/control/nodes/acme-1", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("single-node read status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	rows, err = accountsService.ListDelegationAudit(tenantID, 100)
	if err != nil {
		t.Fatalf("ListDelegationAudit: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("got %d audit rows after a second delegated read, want 2", len(rows))
	}
	if rows[0].Target != "node acme-1" {
		t.Errorf("single-node audit target = %q, want the node named", rows[0].Target)
	}

	// Revocation ends access immediately AND stops the audit stream: rows exist for
	// access that happened, not for access that was refused.
	if err := accountsService.RevokeDelegation(grant.ID, owner.User.ID); err != nil {
		t.Fatalf("RevokeDelegation: %v", err)
	}
	if ids := getNodeList(t, mux, ""); len(ids) != 0 {
		t.Fatalf("operator still saw %v after revocation", ids)
	}
	rows, err = accountsService.ListDelegationAudit(tenantID, 100)
	if err != nil {
		t.Fatalf("ListDelegationAudit: %v", err)
	}
	if len(rows) != 2 {
		t.Errorf("got %d audit rows after revocation, want the 2 from when access was allowed", len(rows))
	}
}

// controlMuxAsPrincipal wires the control-plane read routes with a real account
// store and an explicit principal. Needed where the tenant id is generated at
// signup and therefore cannot be named by the header-based helper.
func controlMuxAsPrincipal(registry controlNodeRegistry, accountsService *accounts.Service, principal identity.Principal) *http.ServeMux {
	mux := http.NewServeMux()
	attach := func(handler http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			handler.ServeHTTP(w, r.WithContext(WithPrincipal(r.Context(), principal)))
		})
	}
	registerControlNodeRoutes(mux, registry, nil, attach, attach,
		NewNodeAccessPolicy(accountsService), accountsService)
	return mux
}

// TestTenantPeerReadIsNotAuditedAsDelegation: the audit trail is for operating
// access. A tenant member reading its own tenant's nodes is not "代看", and logging
// it would bury the rows the tenant actually needs to inspect.
func TestTenantPeerReadIsNotAuditedAsDelegation(t *testing.T) {
	accountsService, err := accounts.New(t.TempDir())
	if err != nil {
		t.Fatalf("accounts.New: %v", err)
	}
	t.Cleanup(func() { _ = accountsService.Close() })

	owner, err := accountsService.Register(accounts.RegisterInput{
		Email: "owner@example.com", Password: "correct-horse", TenantName: "Acme",
	})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	tenantID := owner.Tenant.ID
	registry := &fakeControlRegistry{nodes: []noderegistry.NodeView{
		{ID: "acme-shared", TenantID: tenantID, OwnerUserID: "user-b", Visibility: "tenant"},
	}}
	// A fellow tenant member (not the owner, so the read really is the peer path).
	peer := identity.NewPrincipal(tenantID, "user-a", "", identity.KindUser, identity.EntitlementRelayOnly)
	mux := controlMuxAsPrincipal(registry, accountsService, peer)

	req := httptest.NewRequest(http.MethodGet, "/control/nodes", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "acme-shared") {
		t.Fatalf("the tenant peer did not see its own tenant-visible node: %s", rec.Body.String())
	}

	rows, err := accountsService.ListDelegationAudit(tenantID, 100)
	if err != nil {
		t.Fatalf("ListDelegationAudit: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("a tenant's own read produced %d delegation-audit rows, want 0: %+v", len(rows), rows)
	}
}
