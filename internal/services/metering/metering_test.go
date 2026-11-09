package metering

import (
	"testing"
	"time"
)

func newTestService(t *testing.T) *Service {
	t.Helper()
	service, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = service.Close() })
	return service
}

func TestRecordAndSummarizeByTenant(t *testing.T) {
	service := newTestService(t)

	records := []Record{
		{TenantID: "t1", UserID: "u1", NodeID: "n1", Method: "post", Path: "/a", Status: 200, RequestBytes: 10, ResponseBytes: 100},
		{TenantID: "t1", UserID: "u1", NodeID: "n2", Method: "GET", Path: "/b", Status: 200, RequestBytes: 5, ResponseBytes: 50},
		{TenantID: "t2", UserID: "u2", NodeID: "n3", Method: "GET", Path: "/c", Status: 502, RequestBytes: 1, ResponseBytes: 2},
	}
	for _, record := range records {
		if err := service.Record(record); err != nil {
			t.Fatalf("Record: %v", err)
		}
	}

	summary, err := service.Summary("t1", time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatalf("Summary: %v", err)
	}
	if summary.Requests != 2 || summary.RequestBytes != 15 || summary.ResponseBytes != 150 {
		t.Errorf("t1 summary = %+v, want 2 requests / 15 in / 150 out", summary)
	}

	all, err := service.TenantSummaries(time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatalf("TenantSummaries: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("tenants = %d, want 2", len(all))
	}
}

// TestSummaryIsTenantScoped: the reconciliation output is traffic metadata about
// someone else's machines, so a tenant query must never include another tenant's
// rows.
func TestSummaryIsTenantScoped(t *testing.T) {
	service := newTestService(t)
	_ = service.Record(Record{TenantID: "t1", RequestBytes: 10})
	_ = service.Record(Record{TenantID: "t2", RequestBytes: 999})

	summary, err := service.Summary("t1", time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatalf("Summary: %v", err)
	}
	if summary.RequestBytes != 10 {
		t.Errorf("request bytes = %d, want 10 (t2 leaked in)", summary.RequestBytes)
	}
}

// TestSummaryWindowExcludesOldRows: reconciliation is about a period, so a row
// outside the window must not be counted.
func TestSummaryWindowExcludesOldRows(t *testing.T) {
	service := newTestService(t)
	base := time.Now()
	service.SetNow(func() time.Time { return base.Add(-48 * time.Hour) })
	_ = service.Record(Record{TenantID: "t1", RequestBytes: 7})
	service.SetNow(func() time.Time { return base })
	_ = service.Record(Record{TenantID: "t1", RequestBytes: 3})

	summary, err := service.Summary("t1", base.Add(-24*time.Hour))
	if err != nil {
		t.Fatalf("Summary: %v", err)
	}
	if summary.Requests != 1 || summary.RequestBytes != 3 {
		t.Errorf("summary = %+v, want only the in-window row (1 request / 3 bytes)", summary)
	}
}

// TestSummaryEmptyTenantIsZeroNotError: a tenant with no traffic is a normal
// reconciliation result, not a failure.
func TestSummaryEmptyTenantIsZeroNotError(t *testing.T) {
	service := newTestService(t)
	summary, err := service.Summary("t-missing", time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatalf("Summary: %v", err)
	}
	if summary.Requests != 0 || summary.TenantID != "t-missing" {
		t.Errorf("summary = %+v, want a zeroed row naming the tenant", summary)
	}
}

// TestUnscopedTrafficIsItsOwnBucket: the platform operator's own traffic has no
// tenant. It must be recorded (so total == sum of rows) rather than attributed to
// a tenant that did not make the request.
func TestUnscopedTrafficIsItsOwnBucket(t *testing.T) {
	service := newTestService(t)
	_ = service.Record(Record{TenantID: "", UserID: "local-operator", RequestBytes: 4})

	summaries, err := service.TenantSummaries(time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatalf("TenantSummaries: %v", err)
	}
	if len(summaries) != 1 || summaries[0].TenantID != "" {
		t.Fatalf("summaries = %+v, want one empty-tenant bucket", summaries)
	}
	if summaries[0].RequestBytes != 4 {
		t.Errorf("request bytes = %d, want 4", summaries[0].RequestBytes)
	}
}

// TestRecordQuietSurvivesClosedStore: metering must never be able to fail a user
// request, so the request-path recorder swallows store errors.
func TestRecordQuietSurvivesClosedStore(t *testing.T) {
	service := newTestService(t)
	if err := service.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// Must not panic; the error goes to the log, not to the caller.
	service.RecordQuiet(Record{TenantID: "t1"})
}

func TestNewRejectsEmptyDir(t *testing.T) {
	if _, err := New(""); err == nil {
		t.Fatal("New accepted an empty state dir")
	}
}

// TestForwardAuditIsTenantScoped: an audit row names a host:port the caller tried
// to reach, which is traffic metadata about someone else's network. A tenant read
// must never include another tenant's attempts, for the same reason the usage
// summary is scoped.
func TestForwardAuditIsTenantScoped(t *testing.T) {
	service := newTestService(t)
	_ = service.RecordForwardAudit(ForwardAuditRecord{TenantID: "t1", UserID: "u1", NodeID: "n1", Target: "db:5432", Allowed: true})
	_ = service.RecordForwardAudit(ForwardAuditRecord{TenantID: "t2", UserID: "u2", NodeID: "n2", Target: "redis:6379", Allowed: false, Reason: "forward target not allowed"})

	scoped, err := service.ForwardAudits("t1", time.Now().Add(-time.Hour), 0)
	if err != nil {
		t.Fatalf("ForwardAudits: %v", err)
	}
	if len(scoped) != 1 {
		t.Fatalf("t1 attempts = %d, want 1: %#v", len(scoped), scoped)
	}
	if scoped[0].Target != "db:5432" || !scoped[0].Allowed {
		t.Errorf("t1 attempt = %+v, want db:5432 allowed", scoped[0])
	}

	// The operator view spans tenants, which is its whole purpose.
	all, err := service.AllForwardAudits(time.Now().Add(-time.Hour), 0)
	if err != nil {
		t.Fatalf("AllForwardAudits: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("all attempts = %d, want 2", len(all))
	}
}

// TestForwardAuditRecordsADenial is the refusal path: a denied attempt has no
// usage bytes at all, so it can only exist in the audit table — which is exactly
// why it is a separate table and not a status column on relay_usage.
func TestForwardAuditRecordsADenial(t *testing.T) {
	service := newTestService(t)
	_ = service.RecordForwardAudit(ForwardAuditRecord{
		TenantID: "t1", UserID: "u1", NodeID: "n1", Target: "10.0.0.5:22",
		Allowed: false, Reason: "cross_tenant",
	})

	attempts, err := service.ForwardAudits("t1", time.Now().Add(-time.Hour), 0)
	if err != nil {
		t.Fatalf("ForwardAudits: %v", err)
	}
	if len(attempts) != 1 {
		t.Fatalf("attempts = %d, want 1", len(attempts))
	}
	if attempts[0].Allowed {
		t.Error("denial was stored as allowed")
	}
	if attempts[0].Reason != "cross_tenant" {
		t.Errorf("reason = %q, want cross_tenant", attempts[0].Reason)
	}
	// A refusal must not leak into the billing counters: it moved no bytes.
	summary, err := service.Summary("t1", time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatalf("Summary: %v", err)
	}
	if summary.Requests != 0 {
		t.Errorf("requests = %d, want 0 — a refused attempt is not forwarded traffic", summary.Requests)
	}
}

// TestForwardAuditRespectsLimitAndWindow pins the two bounds: the read is bounded
// (a security list must not grow with retention) and windowed.
func TestForwardAuditRespectsLimitAndWindow(t *testing.T) {
	service := newTestService(t)
	for i := 0; i < 5; i++ {
		_ = service.RecordForwardAudit(ForwardAuditRecord{TenantID: "t1", NodeID: "n1", Target: "host:1", Allowed: true})
	}

	limited, err := service.ForwardAudits("t1", time.Now().Add(-time.Hour), 2)
	if err != nil {
		t.Fatalf("ForwardAudits: %v", err)
	}
	if len(limited) != 2 {
		t.Fatalf("limited = %d, want 2", len(limited))
	}

	// A future window excludes everything recorded now.
	old, err := service.ForwardAudits("t1", time.Now().Add(time.Hour), 0)
	if err != nil {
		t.Fatalf("ForwardAudits: %v", err)
	}
	if len(old) != 0 {
		t.Errorf("future window returned %d rows, want 0", len(old))
	}
}

// TestRecordForwardAuditQuietSurvivesClosedStore mirrors RecordQuiet: auditing
// must never be able to fail a user request.
func TestRecordForwardAuditQuietSurvivesClosedStore(t *testing.T) {
	service := newTestService(t)
	if err := service.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	service.RecordForwardAuditQuiet(ForwardAuditRecord{TenantID: "t1", NodeID: "n1"})
}
