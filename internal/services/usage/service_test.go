package usage

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCreateKeyReturnsSecret(t *testing.T) {
	dir := t.TempDir()
	store, err := NewJSONStore(dir)
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

func TestVerifyKey(t *testing.T) {
	dir := t.TempDir()
	store, err := NewJSONStore(dir)
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
	store, err := NewJSONStore(dir)
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
	store, err := NewJSONStore(dir)
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
	store, err := NewJSONStore(dir)
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
	store, err := NewJSONStore(dir)
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
		APIKeyID:       keyResp.Key.ID,
		PublicModel:    "test-model",
		InputTokens:    100,
		OutputTokens:   50,
		CacheReadTokens: 40,
		CacheWriteTokens: 10,
		CreditWeight:   2.0,
		Status:         "success",
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
	store, err := NewJSONStore(dir)
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
			APIKeyID:      keyResp.Key.ID,
			PublicModel:   "test-model",
			InputTokens:   100,
			OutputTokens:  50,
			CreditWeight:  1.0,
			Status:        "success",
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

func TestJSONStorePersistence(t *testing.T) {
	dir := t.TempDir()
	store1, err := NewJSONStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	svc1 := NewService(store1)

	resp, err := svc1.CreateKey(KeyCreateRequest{Name: "persist-test", BudgetCredits: 500})
	if err != nil {
		t.Fatal(err)
	}

	// Re-create store from same dir
	store2, err := NewJSONStore(dir)
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

func TestEstimateTokens(t *testing.T) {
	// Verify the test works with no body
}

func TestMain(m *testing.M) {
	// Use a temp dir for all tests that create stores
	code := m.Run()
	os.RemoveAll(filepath.Join(os.TempDir(), "usage-test"))
	os.Exit(code)
}
