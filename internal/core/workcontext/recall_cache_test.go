package workcontext

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestRecallSnapshotTracksOtherConnectionAndOwner(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memory.sqlite")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	c := mustCreate(t, s, testScope(), "snapshot")
	f := memoryFact(t, s, c.ID, "gopls references")
	for i := 0; i < 3; i++ {
		if got, err := s.RecallCurrent(ctx, testScope(), c.ID, "gopls references"); err != nil || len(got) != 1 {
			t.Fatal(got, err)
		}
	}
	if _, err = s.RecallCurrent(ctx, Scope{TenantID: testScope().TenantID, OwnerID: "other"}, c.ID, "gopls references"); !errors.Is(err, ErrNotFound) {
		t.Fatal("cached ownership bypass", err)
	}
	other, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if _, err = other.ReviewFact(ctx, testScope(), c.ID, f.ID, FactReview{Status: "stale", IfVersion: f.Version}); err != nil {
		t.Fatal(err)
	}
	if got, err := s.RecallCurrent(ctx, testScope(), c.ID, "gopls references"); err != nil || len(got) != 0 {
		t.Fatal("cached another process's retired fact", got, err)
	}
}

func TestRecallSnapshotExpiresAliasesWithoutAWrite(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	c := mustCreate(t, s, testScope(), "deadline")
	f := memoryFact(t, s, c.ID, "截图识别模块")
	deadline := time.Now().Add(250 * time.Millisecond)
	until := deadline.UTC().Format(time.RFC3339Nano)
	f, err := s.ReviewFact(ctx, testScope(), c.ID, f.ID, FactReview{Status: "confirmed", IfVersion: f.Version, ValidUntil: &until})
	if err != nil {
		t.Fatal(err)
	}
	saveEntityTest(t, s, c.ID, f, "module", "截图识别模块", "vision_alias")
	for i := 0; i < 3; i++ {
		if got, err := s.RecallCurrent(ctx, testScope(), c.ID, "vision_alias"); err != nil || len(got) != 1 {
			t.Fatal(got, err)
		}
	}
	time.Sleep(time.Until(deadline) + 10*time.Millisecond)
	if got, err := s.RecallCurrent(ctx, testScope(), c.ID, "vision_alias"); err != nil || len(got) != 0 {
		t.Fatal("expired alias remained in snapshot", got, err)
	}
}
