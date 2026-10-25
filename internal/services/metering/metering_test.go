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
