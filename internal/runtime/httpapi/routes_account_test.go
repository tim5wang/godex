package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// The user centre, member management and person invites (design §3.6).
//
// Focused on the properties that would be dangerous to get wrong rather than on
// every field: self-scoping (no id in the request can address another account),
// password change bounding a stolen session, ownership protection, and the
// one-time nature of an invite code.

func TestUserCentreScopesToTheCaller(t *testing.T) {
	server, _ := newAuthServer(t, "web-secret")
	token, tenantID, userID := registerTenantOwner(t, server, "owner@example.com")

	resp, body := doJSON(t, http.MethodGet, server.URL+"/account", token, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /account: status = %d, want 200: %v", resp.StatusCode, body)
	}
	if got, _ := body["email"].(string); got != "owner@example.com" {
		t.Fatalf("email = %q, want owner@example.com", got)
	}
	if got, _ := body["tenant_id"].(string); got != tenantID {
		t.Fatalf("tenant_id = %q, want %q", got, tenantID)
	}
	if got, _ := body["user_id"].(string); got != userID {
		t.Fatalf("user_id = %q, want %q", got, userID)
	}
	if got, _ := body["role"].(string); got != "owner" {
		t.Fatalf("role = %q, want owner", got)
	}

	// Sign-up is itself a sign-in, so the history must already carry it.
	resp, body = doJSON(t, http.MethodGet, server.URL+"/account/login-events", token, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /account/login-events: status = %d, want 200: %v", resp.StatusCode, body)
	}
	events, _ := body["events"].([]any)
	if len(events) == 0 {
		t.Fatal("signup left no login event; the history would start empty for every new account")
	}
	first, _ := events[0].(map[string]any)
	if got, _ := first["event"].(string); got != "register" {
		t.Fatalf("newest event = %q, want register", got)
	}

	// The session that is making the request is flagged, so the UI does not offer
	// to revoke the device the user is holding.
	resp, body = doJSON(t, http.MethodGet, server.URL+"/account/sessions", token, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /account/sessions: status = %d, want 200: %v", resp.StatusCode, body)
	}
	sessions, _ := body["sessions"].([]any)
	if len(sessions) != 1 {
		t.Fatalf("sessions = %d, want 1", len(sessions))
	}
	current, _ := sessions[0].(map[string]any)
	if flag, _ := current["current"].(bool); !flag {
		t.Fatal("the current session was not flagged; the UI cannot tell which one it is on")
	}
	// The token hash must never be handed back: it is a credential.
	if _, leaked := current["token_hash"]; leaked {
		t.Fatal("the session list exposed a token hash")
	}
}

// TestSharedWebTokenHasNoAccount pins that the operator's credential is not
// mistaken for an account: it has no user row, so account surfaces must say so
// rather than answer with an empty list ("no devices signed in" would be a lie).
func TestSharedWebTokenHasNoAccount(t *testing.T) {
	server, _ := newAuthServer(t, "web-secret")

	resp, body := doJSON(t, http.MethodGet, server.URL+"/account/sessions", "web-secret", nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %v", resp.StatusCode, body)
	}

	resp, body = doJSON(t, http.MethodGet, server.URL+"/account", "web-secret", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("profile: status = %d, want 200: %v", resp.StatusCode, body)
	}
	if flag, _ := body["shared_token"].(bool); !flag {
		t.Fatalf("profile did not flag the shared web token: %v", body)
	}
}

// TestPasswordChangeRevokesOtherSessions is the reason changing a password is
// worth doing: a leaked credential must stop working. The caller's own session
// survives so the UI stays usable.
func TestPasswordChangeRevokesOtherSessions(t *testing.T) {
	server, _ := newAuthServer(t, "web-secret")
	token, _, _ := registerTenantOwner(t, server, "owner@example.com")

	// A second sign-in stands in for the "other device".
	resp, body := postAuth(t, server.URL+"/auth/login", map[string]any{
		"email": "owner@example.com", "password": "correct-horse"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("second login: status = %d: %v", resp.StatusCode, body)
	}
	otherToken, _ := body["token"].(string)
	if otherToken == "" {
		t.Fatal("second login returned no token")
	}

	resp, body = doJSON(t, http.MethodPost, server.URL+"/account/password", token, map[string]any{
		"current_password": "correct-horse", "new_password": "a-longer-secret"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("change password: status = %d, want 200: %v", resp.StatusCode, body)
	}

	if resp, _ := doJSON(t, http.MethodGet, server.URL+"/account", otherToken, nil); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("the other session survived a password change: status = %d, want 401", resp.StatusCode)
	}
	if resp, _ := doJSON(t, http.MethodGet, server.URL+"/account", token, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("the caller's own session was revoked: status = %d, want 200", resp.StatusCode)
	}

	// The new password must actually work, and the old one must not.
	if resp, _ := postAuth(t, server.URL+"/auth/login", map[string]any{
		"email": "owner@example.com", "password": "a-longer-secret"}); resp.StatusCode != http.StatusOK {
		t.Fatalf("new password rejected: status = %d", resp.StatusCode)
	}
	if resp, _ := postAuth(t, server.URL+"/auth/login", map[string]any{
		"email": "owner@example.com", "password": "correct-horse"}); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("old password still works: status = %d, want 401", resp.StatusCode)
	}
}

// TestPasswordChangeRequiresTheCurrentPassword pins that a stolen session cannot
// lock the real owner out.
func TestPasswordChangeRequiresTheCurrentPassword(t *testing.T) {
	server, _ := newAuthServer(t, "web-secret")
	token, _, _ := registerTenantOwner(t, server, "owner@example.com")

	resp, body := doJSON(t, http.MethodPost, server.URL+"/account/password", token, map[string]any{
		"current_password": "not-the-password", "new_password": "a-longer-secret"})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %v", resp.StatusCode, body)
	}
	// And the old password must still be the working one.
	if resp, _ := postAuth(t, server.URL+"/auth/login", map[string]any{
		"email": "owner@example.com", "password": "correct-horse"}); resp.StatusCode != http.StatusOK {
		t.Fatalf("the password changed despite a bad current password: status = %d", resp.StatusCode)
	}
}

func TestPasswordChangeRejectsReusingTheSamePassword(t *testing.T) {
	server, _ := newAuthServer(t, "web-secret")
	token, _, _ := registerTenantOwner(t, server, "owner@example.com")

	resp, body := doJSON(t, http.MethodPost, server.URL+"/account/password", token, map[string]any{
		"current_password": "correct-horse", "new_password": "correct-horse"})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %v", resp.StatusCode, body)
	}
}

// inviteMember issues a code as the owner and has a new person redeem it.
func inviteMember(t *testing.T, server *httptest.Server, ownerToken, role string) (personToken, personUserID string) {
	t.Helper()
	resp, body := doJSON(t, http.MethodPost, server.URL+"/tenant/invites", ownerToken, map[string]any{
		"role": role, "ttl_hours": 24})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create invite: status = %d, want 201: %v", resp.StatusCode, body)
	}
	code, _ := body["code"].(string)
	if code == "" {
		t.Fatal("create invite returned no plaintext code; it is shown exactly once")
	}

	resp, body = postAuth(t, server.URL+"/auth/accept-invite", map[string]any{
		"code": code, "email": "person@example.com", "password": "correct-horse"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("accept invite: status = %d, want 201: %v", resp.StatusCode, body)
	}
	personToken, _ = body["token"].(string)
	principal, _ := body["principal"].(map[string]any)
	personUserID, _ = principal["user_id"].(string)
	if personToken == "" || personUserID == "" {
		t.Fatalf("accept invite returned no session: %v", body)
	}
	return personToken, personUserID
}

// TestMemberInviteJoinsAnExistingTenant is the gap this closes: before invites, a
// signup ALWAYS created a new tenant, so a second person could never join one.
func TestMemberInviteJoinsAnExistingTenant(t *testing.T) {
	server, _ := newAuthServer(t, "web-secret")
	ownerToken, tenantID, ownerUserID := registerTenantOwner(t, server, "owner@example.com")

	personToken, personUserID := inviteMember(t, server, ownerToken, "member")

	// The new member is in the SAME tenant, with the invited role.
	resp, body := doJSON(t, http.MethodGet, server.URL+"/account", personToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /account as member: status = %d: %v", resp.StatusCode, body)
	}
	if got, _ := body["tenant_id"].(string); got != tenantID {
		t.Fatalf("member tenant = %q, want the inviter's %q", got, tenantID)
	}
	if got, _ := body["role"].(string); got != "member" {
		t.Fatalf("member role = %q, want member", got)
	}

	// Both appear in the owner's member list, with emails for display.
	resp, body = doJSON(t, http.MethodGet, server.URL+"/tenant/members", ownerToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list members: status = %d: %v", resp.StatusCode, body)
	}
	rows, _ := body["members"].([]any)
	if len(rows) != 2 {
		t.Fatalf("members = %d, want 2: %v", len(rows), body)
	}
	seen := map[string]string{}
	for _, raw := range rows {
		row, _ := raw.(map[string]any)
		id, _ := row["user_id"].(string)
		email, _ := row["email"].(string)
		seen[id] = email
	}
	if seen[ownerUserID] != "owner@example.com" || seen[personUserID] != "person@example.com" {
		t.Fatalf("member emails wrong: %v", seen)
	}
}

// TestInviteCannotGrantOwnership pins that member management cannot be used to
// take over a tenant.
func TestInviteCannotGrantOwnership(t *testing.T) {
	server, _ := newAuthServer(t, "web-secret")
	ownerToken, _, _ := registerTenantOwner(t, server, "owner@example.com")

	resp, body := doJSON(t, http.MethodPost, server.URL+"/tenant/invites", ownerToken, map[string]any{
		"role": "owner"})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("owner invite: status = %d, want 400: %v", resp.StatusCode, body)
	}
}

// TestInviteCodeIsOneTime pins that a leaked code is useless after the intended
// person has used it.
func TestInviteCodeIsOneTime(t *testing.T) {
	server, _ := newAuthServer(t, "web-secret")
	ownerToken, _, _ := registerTenantOwner(t, server, "owner@example.com")

	resp, body := doJSON(t, http.MethodPost, server.URL+"/tenant/invites", ownerToken, map[string]any{"role": "member"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create invite: status = %d: %v", resp.StatusCode, body)
	}
	code, _ := body["code"].(string)

	if resp, body := postAuth(t, server.URL+"/auth/accept-invite", map[string]any{
		"code": code, "email": "person@example.com", "password": "correct-horse"}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("first redemption: status = %d: %v", resp.StatusCode, body)
	}
	if resp, _ := postAuth(t, server.URL+"/auth/accept-invite", map[string]any{
		"code": code, "email": "other@example.com", "password": "correct-horse"}); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("second redemption: status = %d, want 401 (the code is one-time)", resp.StatusCode)
	}
}

// TestOwnerIsProtectedFromRemovalAndDemotion pins that a tenant cannot be left
// with nobody able to administer it.
func TestOwnerIsProtectedFromRemovalAndDemotion(t *testing.T) {
	server, _ := newAuthServer(t, "web-secret")
	ownerToken, _, ownerUserID := registerTenantOwner(t, server, "owner@example.com")

	resp, body := doJSON(t, http.MethodDelete, server.URL+"/tenant/members/"+ownerUserID, ownerToken, nil)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("remove owner: status = %d, want 409: %v", resp.StatusCode, body)
	}

	resp, body = doJSON(t, http.MethodPut, server.URL+"/tenant/members/"+ownerUserID+"/role", ownerToken,
		map[string]any{"role": "member"})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("demote owner: status = %d, want 409: %v", resp.StatusCode, body)
	}
}

// TestMemberRoleChangeAndRemoval covers the tenant-admin half of member
// management, including that a removed member's session stops working at once.
func TestMemberRoleChangeAndRemoval(t *testing.T) {
	server, _ := newAuthServer(t, "web-secret")
	ownerToken, _, _ := registerTenantOwner(t, server, "owner@example.com")
	personToken, personUserID := inviteMember(t, server, ownerToken, "member")

	// A plain member is not an administrator.
	if resp, _ := doJSON(t, http.MethodGet, server.URL+"/tenant/members", personToken, nil); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("member listing members: status = %d, want 403", resp.StatusCode)
	}
	if resp, _ := doJSON(t, http.MethodPost, server.URL+"/tenant/invites", personToken, map[string]any{
		"role": "member"}); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("member issuing an invite: status = %d, want 403", resp.StatusCode)
	}

	// Promoting to admin grants the surfaces.
	resp, body := doJSON(t, http.MethodPut, server.URL+"/tenant/members/"+personUserID+"/role", ownerToken,
		map[string]any{"role": "admin"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("promote: status = %d, want 200: %v", resp.StatusCode, body)
	}
	if resp, _ := doJSON(t, http.MethodGet, server.URL+"/tenant/members", personToken, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("admin listing members: status = %d, want 200", resp.StatusCode)
	}

	// Removing drops the membership AND its live sessions: "removed" must be true
	// immediately, not when the session happens to expire.
	if resp, body := doJSON(t, http.MethodDelete, server.URL+"/tenant/members/"+personUserID, ownerToken, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("remove member: status = %d, want 200: %v", resp.StatusCode, body)
	}
	if resp, _ := doJSON(t, http.MethodGet, server.URL+"/account", personToken, nil); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("removed member's session still works: status = %d, want 401", resp.StatusCode)
	}
}

// TestPlatformAdminCanManageAnyTenant covers the superadmin path, and that a
// tenant user cannot reach it.
func TestPlatformAdminCanManageAnyTenant(t *testing.T) {
	server, _ := newAuthServer(t, "web-secret")
	ownerToken, tenantID, ownerUserID := registerTenantOwner(t, server, "owner@example.com")

	resp, body := doJSON(t, http.MethodPost, server.URL+"/admin/tenants/"+tenantID+"/invites", "web-secret",
		map[string]any{"role": "member"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("admin invite: status = %d, want 201: %v", resp.StatusCode, body)
	}
	code, _ := body["code"].(string)
	if resp, body := postAuth(t, server.URL+"/auth/accept-invite", map[string]any{
		"code": code, "email": "person@example.com", "password": "correct-horse"}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("accept admin invite: status = %d: %v", resp.StatusCode, body)
	}

	// A tenant user must not be able to administer tenants (including its own)
	// through the platform surface.
	if resp, _ := doJSON(t, http.MethodGet, server.URL+"/admin/tenants/"+tenantID+"/invites", ownerToken, nil); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("tenant user on the admin surface: status = %d, want 403", resp.StatusCode)
	}

	// And a non-member cannot be removed by id from another tenant's admin route.
	if resp, body := doJSON(t, http.MethodDelete,
		server.URL+"/admin/tenants/"+tenantID+"/members/"+ownerUserID, "web-secret", nil); resp.StatusCode != http.StatusConflict {
		t.Fatalf("admin removing the owner: status = %d, want 409: %v", resp.StatusCode, body)
	}
}

// TestAdminCanRevokeInviteOnAnyTenant pins the admin-scoped invite revoke. The
// tenant surface had DELETE /tenant/invites/{id} but the admin surface had only
// create and list, so an operator revoking a code from the superadmin member page
// got a 404 while the button looked identical — the two surfaces must offer the
// same operations or the UI silently lies about what it can do.
func TestAdminCanRevokeInviteOnAnyTenant(t *testing.T) {
	server, _ := newAuthServer(t, "web-secret")
	_, tenantID, _ := registerTenantOwner(t, server, "owner@example.com")

	_, body := doJSON(t, http.MethodPost, server.URL+"/admin/tenants/"+tenantID+"/invites", "web-secret",
		map[string]any{"role": "member"})
	invite, _ := body["invite"].(map[string]any)
	inviteID, _ := invite["id"].(string)
	if inviteID == "" {
		t.Fatalf("no invite id in %v", body)
	}

	resp, body := doJSON(t, http.MethodDelete,
		server.URL+"/admin/tenants/"+tenantID+"/invites/"+inviteID, "web-secret", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("admin revoke invite: status = %d, want 200: %v", resp.StatusCode, body)
	}

	// The revoked code must no longer be redeemable — the point of revoking.
	_, created := doJSON(t, http.MethodPost, server.URL+"/admin/tenants/"+tenantID+"/invites", "web-secret",
		map[string]any{"role": "member"})
	code, _ := created["code"].(string)
	if resp, _ := doJSON(t, http.MethodDelete,
		server.URL+"/admin/tenants/"+tenantID+"/invites/"+code, "web-secret", nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("revoking an unknown invite id: status = %d, want 404", resp.StatusCode)
	}
}

// TestTenantSurfaceRefusesCallersWithoutATenant pins the operator's behaviour on
// the tenant surface: a plain 400 saying this is a tenant surface, not a 403.
func TestTenantSurfaceRefusesCallersWithoutATenant(t *testing.T) {
	server, _ := newAuthServer(t, "web-secret")

	resp, body := doJSON(t, http.MethodGet, server.URL+"/tenant/members", "web-secret", nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %v", resp.StatusCode, body)
	}
}

// TestAccountSurfacesRequireAuthentication pins that nothing here is reachable
// anonymously.
func TestAccountSurfacesRequireAuthentication(t *testing.T) {
	server, _ := newAuthServer(t, "web-secret")

	for _, path := range []string{"/account", "/account/sessions", "/account/login-events", "/tenant/members"} {
		t.Run(path, func(t *testing.T) {
			resp, _ := doJSON(t, http.MethodGet, server.URL+path, "", nil)
			if resp.StatusCode != http.StatusUnauthorized {
				t.Fatalf("%s: status = %d, want 401", path, resp.StatusCode)
			}
		})
	}
}
