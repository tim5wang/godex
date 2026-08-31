package usage

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tim5wang/godex/internal/core/conversation"
	"github.com/tim5wang/godex/internal/contracts/protocol"
)

func TestCreateKeyReturnsSecret(t *testing.T) {
	dir := t.TempDir()
	store, err := NewSQLiteStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	svc := NewService(store)

	resp, err := svc.CreateKey(KeyCreateRequest{
		Name:          "test-key",
		BudgetCredits: 1000,
	})
	if err != nil {
		t.Fatal(err)
	}

	if resp.Secret == "" {
		t.Fatal("expected non-empty secret")
	}
	if !strings.HasPrefix(resp.Secret, "gdx_") {
		t.Fatalf("expected secret to start with 'gdx_', got %q", resp.Secret[:8])
	}
	if resp.Key.KeyHash == "" {
		t.Fatal("expected stored key to have hash")
	}
	if resp.Key.KeyPrefix == "" {
		t.Fatal("expected stored key to have prefix")
	}
	if strings.Contains(resp.Key.KeyPrefix, resp.Secret[8:]) {
		t.Fatal("key prefix should not contain full secret tail")
	}

	// Listed keys must not expose secret
	keys, err := svc.ListKeys()
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 1 {
		t.Fatalf("expected 1 key, got %d", len(keys))
	}
	if keys[0].KeyHash != "" {
		t.Fatal("listed key should not expose hash")
	}
}

func TestResetKeyGeneratesNewSecretAndHash(t *testing.T) {
	dir := t.TempDir()
	store, err := NewSQLiteStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	svc := NewService(store)

	created, err := svc.CreateKey(KeyCreateRequest{Name: "rotate-me", BudgetCredits: 100})
	if err != nil {
		t.Fatalf("seed create: %v", err)
	}
	oldSecret := created.Secret
	oldKeyID := created.Key.ID

	reset, err := svc.ResetKey(oldKeyID)
	if err != nil {
		t.Fatalf("reset: %v", err)
	}
	if reset.Secret == "" {
		t.Fatal("expected non-empty new secret on reset")
	}
	if !strings.HasPrefix(reset.Secret, "gdx_") {
		t.Fatalf("expected reset secret to start with gdx_, got %q", reset.Secret[:8])
	}
	if reset.Secret == oldSecret {
		t.Fatal("reset secret must differ from original")
	}
	if reset.Key.ID != oldKeyID {
		t.Fatalf("reset must keep the same key id, got %q vs %q", reset.Key.ID, oldKeyID)
	}
	if reset.Key.KeyHash == "" {
		t.Fatal("expected stored key to retain a hash after reset")
	}
	if reset.Key.KeyHash == created.Key.KeyHash {
		t.Fatal("reset must rotate the stored hash")
	}
}

func TestResetKeyInvalidatesOldSecret(t *testing.T) {
	dir := t.TempDir()
	store, err := NewSQLiteStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	svc := NewService(store)

	created, err := svc.CreateKey(KeyCreateRequest{Name: "rotate-me", BudgetCredits: 100})
	if err != nil {
		t.Fatalf("seed create: %v", err)
	}

	if _, err := svc.AuthenticateKey(created.Secret); err != nil {
		t.Fatalf("original secret should authenticate before reset: %v", err)
	}

	reset, err := svc.ResetKey(created.Key.ID)
	if err != nil {
		t.Fatalf("reset: %v", err)
	}

	if _, err := svc.AuthenticateKey(created.Secret); err == nil {
		t.Fatal("original secret must be rejected after reset")
	}
	if _, err := svc.AuthenticateKey(reset.Secret); err != nil {
		t.Fatalf("new secret must authenticate after reset: %v", err)
	}
}

func TestResetKeyRejectsUnknownID(t *testing.T) {
	dir := t.TempDir()
	store, err := NewSQLiteStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	svc := NewService(store)

	if _, err := svc.ResetKey("does-not-exist"); err == nil {
		t.Fatal("expected error when resetting unknown key id")
	}
}

func TestVerifyKey(t *testing.T) {
	dir := t.TempDir()
	store, err := NewSQLiteStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	svc := NewService(store)

	resp, err := svc.CreateKey(KeyCreateRequest{Name: "test"})
	if err != nil {
		t.Fatal(err)
	}

	key, err := svc.AuthenticateKey(resp.Secret)
	if err != nil {
		t.Fatal(err)
	}
	if key.ID != resp.Key.ID {
		t.Fatalf("expected key ID %s, got %s", resp.Key.ID, key.ID)
	}
}

func TestDisabledKeyIsRejected(t *testing.T) {
	dir := t.TempDir()
	store, err := NewSQLiteStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	svc := NewService(store)

	resp, err := svc.CreateKey(KeyCreateRequest{Name: "test"})
	if err != nil {
		t.Fatal(err)
	}

	// Disable the key
	disabled := false
	_, err = svc.UpdateKey(resp.Key.ID, KeyUpdateRequest{Enabled: &disabled})
	if err != nil {
		t.Fatal(err)
	}

	_, err = svc.AuthenticateKey(resp.Secret)
	if err == nil {
		t.Fatal("expected error for disabled key")
	}
}

func TestKeyAllowedModels(t *testing.T) {
	dir := t.TempDir()
	store, err := NewSQLiteStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	svc := NewService(store)

	resp, err := svc.CreateKey(KeyCreateRequest{
		Name:          "test",
		AllowedModels: []string{"fast"},
	})
	if err != nil {
		t.Fatal(err)
	}

	key, err := svc.AuthenticateKey(resp.Secret)
	if err != nil {
		t.Fatal(err)
	}

	// Simulate check: key has allowed_models=["fast"], requesting "expensive"
	for _, allowed := range key.AllowedModels {
		if allowed == "expensive" {
			t.Fatal("expensive should not be in allowed models")
		}
	}

	// But "fast" should be allowed
	found := false
	for _, allowed := range key.AllowedModels {
		if allowed == "fast" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected 'fast' in allowed models")
	}
}

func TestDisabledModelMappingRejected(t *testing.T) {
	dir := t.TempDir()
	store, err := NewSQLiteStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	svc := NewService(store)

	m, err := svc.CreateModel(ModelCreateRequest{
		PublicModel:     "fast",
		TargetProfileID: "profile-1",
		TargetModel:     "gpt-4o-mini",
		CreditWeight:    1.0,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Resolve works initially
	_, err = svc.ResolveModel("fast")
	if err != nil {
		t.Fatal(err)
	}

	// Disable
	disabled := false
	_, err = svc.UpdateModel(m.ID, ModelUpdateRequest{Enabled: &disabled})
	if err != nil {
		t.Fatal(err)
	}

	_, err = svc.ResolveModel("fast")
	if err == nil {
		t.Fatal("expected error for disabled model mapping")
	}
}

func TestCreditCalculation(t *testing.T) {
	dir := t.TempDir()
	store, err := NewSQLiteStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	svc := NewService(store)

	// Create key and model
	keyResp, err := svc.CreateKey(KeyCreateRequest{Name: "test", BudgetCredits: 10000})
	if err != nil {
		t.Fatal(err)
	}

	// Record a call with cache read tokens
	call := &UsageCall{
		APIKeyID:         keyResp.Key.ID,
		PublicModel:      "test-model",
		InputTokens:      100,
		OutputTokens:     50,
		CacheReadTokens:  40,
		CacheWriteTokens: 10,
		CreditWeight:     2.0,
		Status:           "success",
	}
	if err := svc.RecordCall(call); err != nil {
		t.Fatal(err)
	}

	// billable = 100 + 50 + 10 + ceil(40 * 0.25) = 160 + 10 = 170
	// credits = 170 * 2.0 = 340
	expectedBillable := 100 + 50 + 10 + 10 // ceil(40*0.25)=10
	if call.BillableTokens != expectedBillable {
		t.Fatalf("expected billable %d, got %d", expectedBillable, call.BillableTokens)
	}

	expectedCredits := float64(expectedBillable) * 2.0
	if call.Credits != expectedCredits {
		t.Fatalf("expected credits %.2f, got %.2f", expectedCredits, call.Credits)
	}
}

func TestSummaryAggregation(t *testing.T) {
	dir := t.TempDir()
	store, err := NewSQLiteStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	svc := NewService(store)

	keyResp, err := svc.CreateKey(KeyCreateRequest{Name: "test", BudgetCredits: 10000})
	if err != nil {
		t.Fatal(err)
	}

	// Record two calls on same key
	for i := 0; i < 2; i++ {
		call := &UsageCall{
			APIKeyID:     keyResp.Key.ID,
			PublicModel:  "test-model",
			InputTokens:  100,
			OutputTokens: 50,
			CreditWeight: 1.0,
			Status:       "success",
		}
		if err := svc.RecordCall(call); err != nil {
			t.Fatal(err)
		}
	}

	summaries, err := svc.GetSummary("day", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(summaries) == 0 {
		t.Fatal("expected at least one summary")
	}
	var total float64
	for _, s := range summaries {
		total += s.Credits
		// 2 calls: (100+50) * 1.0 = 150 per call, 300 total
		if s.CallCount == 2 {
			if s.InputTokens != 200 {
				t.Fatalf("expected input tokens 200, got %d", s.InputTokens)
			}
			if s.OutputTokens != 100 {
				t.Fatalf("expected output tokens 100, got %d", s.OutputTokens)
			}
		}
	}
	if total < 290 || total > 310 {
		t.Fatalf("expected ~300 credits total, got %.2f", total)
	}
}

func TestSummaryIncludesDistinctPeriods(t *testing.T) {
	dir := t.TempDir()
	store, err := NewSQLiteStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	svc := NewService(store)

	keyResp, err := svc.CreateKey(KeyCreateRequest{Name: "test", BudgetCredits: 10000})
	if err != nil {
		t.Fatal(err)
	}
	for _, ts := range []string{"2026-05-14T10:00:00Z", "2026-05-15T10:00:00Z"} {
		parsed, err := time.Parse(time.RFC3339, ts)
		if err != nil {
			t.Fatal(err)
		}
		if err := svc.RecordCall(&UsageCall{
			Timestamp:    parsed,
			APIKeyID:     keyResp.Key.ID,
			PublicModel:  "test-model",
			InputTokens:  10,
			OutputTokens: 5,
			CreditWeight: 1,
			Status:       "success",
		}); err != nil {
			t.Fatal(err)
		}
	}

	summaries, err := svc.GetSummary("day", keyResp.Key.ID)
	if err != nil {
		t.Fatal(err)
	}
	periods := map[string]bool{}
	for _, summary := range summaries {
		periods[summary.Period] = true
	}
	if !periods["2026-05-14"] || !periods["2026-05-15"] {
		t.Fatalf("expected distinct day periods, got %+v", summaries)
	}
}

func TestCreateModelRejectsDuplicatePublicModel(t *testing.T) {
	dir := t.TempDir()
	store, err := NewSQLiteStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	svc := NewService(store)

	_, err = svc.CreateModel(ModelCreateRequest{
		PublicModel:     "fast",
		TargetProfileID: "coding",
		TargetModel:     "gpt-4o-mini",
		CreditWeight:    1,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = svc.CreateModel(ModelCreateRequest{
		PublicModel:     "fast",
		TargetProfileID: "other",
		TargetModel:     "gpt-4o",
		CreditWeight:    2,
	})
	if err == nil || !strings.Contains(err.Error(), "public model already exists") {
		t.Fatalf("expected duplicate public model error, got %v", err)
	}
}

func TestRecordLLMUsageUsesSystemKey(t *testing.T) {
	dir := t.TempDir()
	store, err := NewSQLiteStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	svc := NewService(store)

	err = svc.RecordLLMUsage(conversation.UsageEvent{
		Context: conversation.UsageContext{
			SourceChannel:   "web",
			SessionID:       "sess-1",
			TurnID:          "turn-1",
			TargetProfileID: "openai.fast",
			TargetModel:     "gpt-4o-mini",
		},
		Request: protocol.Request{
			Model: "gpt-4o-mini",
			Messages: []protocol.APIMessage{{
				Role:    protocol.RoleUser,
				Content: []protocol.Block{protocol.TextBlock("hello world")},
			}},
		},
		Response: &protocol.Response{
			Content: []protocol.Block{protocol.TextBlock("ok")},
			Usage:   &protocol.Usage{InputTokens: 9, OutputTokens: 2},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	calls, err := svc.GetCalls(time.Now().Format("2006-01-02"), SystemKeyID("web"))
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 || calls[0].APIKeyID != "system:web" || calls[0].SessionID != "sess-1" || calls[0].InputTokens != 9 || calls[0].OutputTokens != 2 {
		t.Fatalf("unexpected internal usage call: %+v", calls)
	}
}

func TestSQLiteStorePersistence(t *testing.T) {
	dir := t.TempDir()
	store1, err := NewSQLiteStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	svc1 := NewService(store1)

	resp, err := svc1.CreateKey(KeyCreateRequest{Name: "persist-test", BudgetCredits: 500})
	if err != nil {
		t.Fatal(err)
	}

	// Re-create store from same dir
	store2, err := NewSQLiteStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	svc2 := NewService(store2)

	keys, err := svc2.ListKeys()
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 1 {
		t.Fatalf("expected 1 persisted key, got %d", len(keys))
	}

	// Authenticate with the secret against new store
	_, err = svc2.AuthenticateKey(resp.Secret)
	if err != nil {
		t.Fatalf("should authenticate persisted key: %v", err)
	}
}

func TestSQLiteStorePersistsCallsInSQLite(t *testing.T) {
	dir := t.TempDir()
	store, err := NewSQLiteStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	svc := NewService(store)
	keyResp, err := svc.CreateKey(KeyCreateRequest{Name: "jsonl-test"})
	if err != nil {
		t.Fatal(err)
	}

	if err := svc.RecordCall(&UsageCall{
		APIKeyID:      keyResp.Key.ID,
		PublicModel:   "public-fast",
		InputTokens:   10,
		OutputTokens:  5,
		CreditWeight:  1,
		Status:        "success",
		SourceChannel: "web",
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(filepath.Join(dir, "usage-gateway.sqlite")); err != nil {
		t.Fatalf("expected sqlite usage store: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "usage-gateway.json")); !os.IsNotExist(err) {
		t.Fatalf("did not expect legacy json usage store, got %v", err)
	}
	reopened, err := NewSQLiteStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	calls, err := reopened.GetCalls(time.Now().Format("2006-01-02"), keyResp.Key.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 || calls[0].PublicModel != "public-fast" || calls[0].InputTokens != 10 || calls[0].OutputTokens != 5 {
		t.Fatalf("unexpected persisted call: %+v", calls)
	}
}

func TestSQLiteStoreDoesNotUseLegacyJSON(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "usage-gateway.json"), []byte(`{"keys":[{"id":"key-legacy","name":"legacy","key_prefix":"gdx_lege****","enabled":true}],"calls":[{"id":"call-legacy","timestamp":"2026-05-16T10:00:00Z","api_key_id":"key-legacy","public_model":"legacy-model","input_tokens":3,"output_tokens":4,"credit_weight":1,"status":"success"}]}`), 0644); err != nil {
		t.Fatal(err)
	}

	store, err := NewSQLiteStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	keys, err := store.ListKeys()
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 0 {
		t.Fatalf("expected sqlite store to ignore legacy json keys, got %+v", keys)
	}
	calls, err := store.GetCalls("2026-05-16", "key-legacy")
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 0 {
		t.Fatalf("expected sqlite store to ignore legacy json calls, got %+v", calls)
	}
}

func TestEstimateTokens(t *testing.T) {
	// Verify the test works with no body
}

// TestCacheStatsHitRateIsCallLevel pins the contract that
// CacheStats.HitRate is the call-level hit rate (the share of
// calls that surfaced at least one cached token), NOT the
// token-level efficiency the previous implementation
// mislabelled. The two metrics live on the same row as
// HitRate and CacheEfficiency respectively; without this
// split the dashboard would conflate "low hit rate" with
// "low token savings" and the operator would not be able to
// tell whether the issue is upstream (caching not enabled)
// or local (cache key mismatch).
func TestCacheStatsHitRateIsCallLevel(t *testing.T) {
	store, err := NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	svc := NewService(store)
	key, err := svc.CreateKey(KeyCreateRequest{Name: "cache-stats", BudgetCredits: 1000})
	if err != nil {
		t.Fatal(err)
	}
	// 4 calls: 3 with cache reads, 1 without. The expected
	// call-level hit rate is 75%, regardless of the per-call
	// token counts. Token-level efficiency depends on the
	// specific input / cache_read distribution; we verify it
	// separately below.
	calls := []*UsageCall{
		{APIKeyID: key.Key.ID, PublicModel: "m", TargetModel: "m", InputTokens: 100, CacheReadTokens: 50, Status: "success"},
		{APIKeyID: key.Key.ID, PublicModel: "m", TargetModel: "m", InputTokens: 100, CacheReadTokens: 50, Status: "success"},
		{APIKeyID: key.Key.ID, PublicModel: "m", TargetModel: "m", InputTokens: 100, CacheReadTokens: 0, Status: "success"},
		{APIKeyID: key.Key.ID, PublicModel: "m", TargetModel: "m", InputTokens: 0, CacheReadTokens: 200, Status: "success"},
	}
	for _, c := range calls {
		if err := svc.RecordCall(c); err != nil {
			t.Fatal(err)
		}
	}
	stats, err := svc.GetCacheStats(CacheStatsQuery{RangeType: "all"})
	if err != nil {
		t.Fatal(err)
	}
	if len(stats) != 1 {
		t.Fatalf("expected 1 row, got %d: %+v", len(stats), stats)
	}
	got := stats[0]
	if got.TotalCalls != 4 {
		t.Errorf("expected TotalCalls=4, got %d", got.TotalCalls)
	}
	if got.CacheHitCalls != 3 {
		t.Errorf("expected CacheHitCalls=3, got %d", got.CacheHitCalls)
	}
	if got.CacheMissCalls != 1 {
		t.Errorf("expected CacheMissCalls=1, got %d", got.CacheMissCalls)
	}
	// 3 / 4 = 75.00 (rounded to two decimals).
	if got.HitRate != 75.0 {
		t.Errorf("expected call-level HitRate=75.0, got %.2f", got.HitRate)
	}
	// Token-level: total input = 300, total cache_read = 300
	// (50+50+0+200). Efficiency = 300 / (300+300) = 50.00%.
	// This is the field the dashboard renders as the
	// cost-saving story.
	if got.CacheEfficiency != 50.0 {
		t.Errorf("expected CacheEfficiency=50.0, got %.2f", got.CacheEfficiency)
	}
	if got.InputTokens != 300 {
		t.Errorf("expected InputTokens=300, got %d", got.InputTokens)
	}
	if got.CacheReadTokens != 300 {
		t.Errorf("expected CacheReadTokens=300, got %d", got.CacheReadTokens)
	}
	if got.CacheWriteTokens != 0 {
		t.Errorf("expected CacheWriteTokens=0, got %d", got.CacheWriteTokens)
	}
}

// TestCacheStatsAllRange pins the new "all" range type
// returning a single lifetime aggregate row. The previous
// implementation rejected unknown range types with no
// useful error, so the dashboard's "lifetime" tile
// 500'd whenever the URL omitted the range parameter. Now
// empty / unknown range defaults to "all" and the response
// carries an empty Period string.
func TestCacheStatsAllRange(t *testing.T) {
	store, err := NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	svc := NewService(store)
	key, err := svc.CreateKey(KeyCreateRequest{Name: "all", BudgetCredits: 1000})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.RecordCall(&UsageCall{
		APIKeyID:        key.Key.ID,
		PublicModel:     "m",
		TargetModel:     "m",
		InputTokens:     100,
		CacheReadTokens: 50,
		Status:          "success",
	}); err != nil {
		t.Fatal(err)
	}
	stats, err := svc.GetCacheStats(CacheStatsQuery{RangeType: "all"})
	if err != nil {
		t.Fatal(err)
	}
	if len(stats) != 1 {
		t.Fatalf("expected 1 lifetime row, got %d", len(stats))
	}
	if stats[0].Period != "" {
		t.Errorf("expected empty Period for all range, got %q", stats[0].Period)
	}
	if stats[0].HitRate != 100.0 {
		t.Errorf("expected HitRate=100, got %.2f", stats[0].HitRate)
	}
	// Also verify the empty range string defaults to "all".
	stats2, err := svc.GetCacheStats(CacheStatsQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if len(stats2) != 1 || stats2[0].Period != "" {
		t.Errorf("expected empty range to default to all, got %+v", stats2)
	}
}

// TestCacheStatsPerKeyFilter pins the new APIKeyID filter
// that lets the proxy-key path expose per-key cache
// stats without leaking the all-tenants view. Two keys
// contribute cache_read calls; the filter must surface
// only the matching key's row.
func TestCacheStatsPerKeyFilter(t *testing.T) {
	store, err := NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	svc := NewService(store)
	keyA, err := svc.CreateKey(KeyCreateRequest{Name: "A", BudgetCredits: 1000})
	if err != nil {
		t.Fatal(err)
	}
	keyB, err := svc.CreateKey(KeyCreateRequest{Name: "B", BudgetCredits: 1000})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.RecordCall(&UsageCall{
		APIKeyID: keyA.Key.ID, PublicModel: "m", TargetModel: "m",
		InputTokens: 100, CacheReadTokens: 80, Status: "success",
	}); err != nil {
		t.Fatal(err)
	}
	if err := svc.RecordCall(&UsageCall{
		APIKeyID: keyB.Key.ID, PublicModel: "m", TargetModel: "m",
		InputTokens: 100, CacheReadTokens: 0, Status: "success",
	}); err != nil {
		t.Fatal(err)
	}
	stats, err := svc.GetCacheStats(CacheStatsQuery{RangeType: "all", APIKeyID: keyA.Key.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(stats) != 1 {
		t.Fatalf("expected exactly 1 row for key A, got %d: %+v", len(stats), stats)
	}
	if stats[0].CacheReadTokens != 80 {
		t.Errorf("expected key A cache_read=80, got %d", stats[0].CacheReadTokens)
	}
	if stats[0].HitRate != 100.0 {
		t.Errorf("expected key A HitRate=100, got %.2f", stats[0].HitRate)
	}
}

// TestCacheStatsEstimatedSavings pins the
// EstimatedSavingsCredits calculation. The gateway treats
// cached tokens as 0.25x billable in RecordCall, so each
// cache_read token saves 0.75x the unit credit rate. The
// number is rounded to six decimal places to match the
// precision of UsageCall.Credits.
func TestCacheStatsEstimatedSavings(t *testing.T) {
	store, err := NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	svc := NewService(store)
	key, err := svc.CreateKey(KeyCreateRequest{Name: "savings", BudgetCredits: 1000})
	if err != nil {
		t.Fatal(err)
	}
	// 1000 cache_read tokens. Unweighted saving = 1000 * 0.75 = 750.0.
	if err := svc.RecordCall(&UsageCall{
		APIKeyID: key.Key.ID, PublicModel: "m", TargetModel: "m",
		InputTokens: 1000, CacheReadTokens: 1000, Status: "success",
	}); err != nil {
		t.Fatal(err)
	}
	stats, err := svc.GetCacheStats(CacheStatsQuery{RangeType: "all", APIKeyID: key.Key.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(stats) != 1 {
		t.Fatalf("expected 1 row, got %d", len(stats))
	}
	if stats[0].EstimatedSavingsCredits != 750.0 {
		t.Errorf("expected EstimatedSavingsCredits=750.0, got %.6f", stats[0].EstimatedSavingsCredits)
	}
}

// TestCacheStatsIgnoresFailedCalls pins the contract that
// cache stats never include failed calls in the aggregate.
// A failed call (e.g. 4xx provider error) never delivered
// any model output, so counting it as a "cache miss" would
// bias the hit rate downward for tenants with intermittent
// provider outages.
func TestCacheStatsIgnoresFailedCalls(t *testing.T) {
	store, err := NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	svc := NewService(store)
	key, err := svc.CreateKey(KeyCreateRequest{Name: "fail", BudgetCredits: 1000})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.RecordCall(&UsageCall{
		APIKeyID: key.Key.ID, PublicModel: "m", TargetModel: "m",
		InputTokens: 100, CacheReadTokens: 0, Status: "success",
	}); err != nil {
		t.Fatal(err)
	}
	if err := svc.RecordCall(&UsageCall{
		APIKeyID: key.Key.ID, PublicModel: "m", TargetModel: "m",
		InputTokens: 100, CacheReadTokens: 0, Status: "error", Error: "boom",
	}); err != nil {
		t.Fatal(err)
	}
	stats, err := svc.GetCacheStats(CacheStatsQuery{RangeType: "all", APIKeyID: key.Key.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(stats) != 1 {
		t.Fatalf("expected 1 row, got %d", len(stats))
	}
	if stats[0].TotalCalls != 1 {
		t.Errorf("expected TotalCalls=1 (failed call excluded), got %d", stats[0].TotalCalls)
	}
	if stats[0].CacheHitCalls != 0 {
		t.Errorf("expected CacheHitCalls=0, got %d", stats[0].CacheHitCalls)
	}
}

func TestMain(m *testing.M) {
	// Use a temp dir for all tests that create stores
	code := m.Run()
	os.RemoveAll(filepath.Join(os.TempDir(), "usage-test"))
	os.Exit(code)
}
