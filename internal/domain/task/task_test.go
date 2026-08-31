package task

import (
	"sync"
	"sync/atomic"
	"testing"
)

type testRepository struct {
	tasks map[int]FileTask
}

func newTestRepository(items ...FileTask) *testRepository {
	repository := &testRepository{tasks: make(map[int]FileTask, len(items))}
	for _, item := range items {
		repository.tasks[item.ID] = item
	}
	return repository
}

func (r *testRepository) LoadAll() ([]FileTask, error) {
	items := make([]FileTask, 0, len(r.tasks))
	for _, item := range r.tasks {
		items = append(items, item)
	}
	return items, nil
}

func (r *testRepository) Save(item FileTask) error {
	r.tasks[item.ID] = item
	return nil
}

func (r *testRepository) Delete(id int) error {
	delete(r.tasks, id)
	return nil
}

func newTestManager() *Manager {
	return NewManager(newTestRepository())
}

func TestListReturnsTasksSortedByID(t *testing.T) {
	manager := newTestManager()

	if _, err := manager.Create("first", ""); err != nil {
		t.Fatalf("create first task: %v", err)
	}
	if _, err := manager.Create("second", ""); err != nil {
		t.Fatalf("create second task: %v", err)
	}
	if _, err := manager.Create("third", ""); err != nil {
		t.Fatalf("create third task: %v", err)
	}

	tasks := manager.List()
	if len(tasks) != 3 {
		t.Fatalf("expected 3 tasks, got %d", len(tasks))
	}
	if tasks[0].ID != 1 || tasks[1].ID != 2 || tasks[2].ID != 3 {
		t.Fatalf("expected sorted task ids [1 2 3], got [%d %d %d]", tasks[0].ID, tasks[1].ID, tasks[2].ID)
	}
}

func TestGetAndListReturnSnapshots(t *testing.T) {
	manager := newTestManager()
	created, err := manager.Create("original", "")
	if err != nil {
		t.Fatalf("create task: %v", err)
	}

	got, err := manager.Get(created.ID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	got.Subject = "mutated"
	got.BlockedBy = append(got.BlockedBy, 99)

	list := manager.List()
	list[0].Subject = "list-mutated"

	again, err := manager.Get(created.ID)
	if err != nil {
		t.Fatalf("get task again: %v", err)
	}
	if again.Subject != "original" {
		t.Fatalf("expected snapshot mutation not to leak, got %q", again.Subject)
	}
	if len(again.BlockedBy) != 0 {
		t.Fatalf("expected blocked_by to remain unchanged, got %#v", again.BlockedBy)
	}
}

func TestClaimPendingIsAtomic(t *testing.T) {
	manager := newTestManager()
	created, err := manager.Create("claim me", "")
	if err != nil {
		t.Fatalf("create task: %v", err)
	}

	var successCount int32
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := manager.ClaimPending(created.ID); err == nil {
				atomic.AddInt32(&successCount, 1)
			}
		}()
	}
	wg.Wait()

	if successCount != 1 {
		t.Fatalf("expected exactly one successful claim, got %d", successCount)
	}

	got, err := manager.Get(created.ID)
	if err != nil {
		t.Fatalf("get claimed task: %v", err)
	}
	if got.Status != StatusInProgress {
		t.Fatalf("expected task to be in progress, got %s", got.Status)
	}
}

func TestUpdateRejectsInvalidBlockedByReferences(t *testing.T) {
	manager := newTestManager()
	first, err := manager.Create("first", "")
	if err != nil {
		t.Fatalf("create first task: %v", err)
	}
	second, err := manager.Create("second", "")
	if err != nil {
		t.Fatalf("create second task: %v", err)
	}

	if err := manager.Update(first.ID, "", []int{999}, nil); err == nil {
		t.Fatal("expected missing blocked_by task to be rejected")
	}
	if err := manager.Update(first.ID, "", []int{first.ID}, nil); err == nil {
		t.Fatal("expected self blocked_by task to be rejected")
	}
	if err := manager.Update(first.ID, "", []int{second.ID, second.ID}, nil); err != nil {
		t.Fatalf("update blocked_by: %v", err)
	}

	got, err := manager.Get(first.ID)
	if err != nil {
		t.Fatalf("get first task: %v", err)
	}
	if len(got.BlockedBy) != 1 || got.BlockedBy[0] != second.ID {
		t.Fatalf("expected deduped blocked_by list [%d], got %#v", second.ID, got.BlockedBy)
	}
}

func TestDeleteClearsBlockedByReferencesFromOtherTasks(t *testing.T) {
	manager := newTestManager()
	first, err := manager.Create("first", "")
	if err != nil {
		t.Fatalf("create first task: %v", err)
	}
	second, err := manager.Create("second", "")
	if err != nil {
		t.Fatalf("create second task: %v", err)
	}
	if err := manager.Update(second.ID, "", []int{first.ID}, nil); err != nil {
		t.Fatalf("update second task blockers: %v", err)
	}

	if err := manager.Delete(first.ID); err != nil {
		t.Fatalf("delete first task: %v", err)
	}

	got, err := manager.Get(second.ID)
	if err != nil {
		t.Fatalf("get second task: %v", err)
	}
	if len(got.BlockedBy) != 0 {
		t.Fatalf("expected blocked_by to be cleared, got %#v", got.BlockedBy)
	}
}

func TestNewManagerLoadsRepositoryTasks(t *testing.T) {
	manager := NewManager(newTestRepository(FileTask{ID: 1, Subject: "first", Status: StatusPending}))
	tasks := manager.List()
	if len(tasks) != 1 || tasks[0].ID != 1 {
		t.Fatalf("expected repository task to be loaded, got %#v", tasks)
	}
}
