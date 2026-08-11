package persistence

import (
	"os"
	"path/filepath"
	"testing"
)

type testValue struct {
	Name  string   `json:"name"`
	Count int      `json:"count"`
	Tags  []string `json:"tags"`
}

func exerciseDurableMap(t *testing.T, m DurableMap[testValue]) {
	t.Helper()

	if values, err := m.All(); err != nil || len(values) != 0 {
		t.Fatalf("All on empty map = %v, %v", values, err)
	}

	// Put + Get
	if err := m.Put("b", testValue{Name: "beta", Count: 2}); err != nil {
		t.Fatalf("Put b: %v", err)
	}
	if err := m.Put("a", testValue{Name: "alpha", Count: 1}); err != nil {
		t.Fatalf("Put a: %v", err)
	}
	got, ok, err := m.Get("a")
	if err != nil || !ok || got.Name != "alpha" {
		t.Fatalf("Get a = %+v, %v, %v", got, ok, err)
	}
	if _, ok, err := m.Get("missing"); err != nil || ok {
		t.Fatalf("Get missing = ok=%v, err=%v", ok, err)
	}

	// All + Entries sorted by key
	values, err := m.All()
	if err != nil || len(values) != 2 || values[0].Name != "alpha" || values[1].Name != "beta" {
		t.Fatalf("All = %+v, %v", values, err)
	}
	entries, err := m.Entries()
	if err != nil || len(entries) != 2 || entries[0].Key != "a" || entries[1].Key != "b" {
		t.Fatalf("Entries = %+v, %v", entries, err)
	}

	// PutIfAbsent / InsertIfAbsent
	if err := m.Put("a", testValue{Name: "alpha2", Count: 10}); err != nil {
		t.Fatalf("Put overwrite a: %v", err)
	}
	kept, err := m.PutIfAbsent("a", testValue{Name: "should-not-store"})
	if err != nil || kept.Name != "alpha2" {
		t.Fatalf("PutIfAbsent existing = %+v, %v", kept, err)
	}
	inserted, err := m.InsertIfAbsent("c", testValue{Name: "gamma"})
	if err != nil || !inserted {
		t.Fatalf("InsertIfAbsent c = %v, %v", inserted, err)
	}
	inserted, err = m.InsertIfAbsent("c", testValue{Name: "gamma2"})
	if err != nil || inserted {
		t.Fatalf("InsertIfAbsent c again = %v, %v", inserted, err)
	}

	// Update
	updated, ok, err := m.Update("c", func(v testValue) testValue {
		v.Count = v.Count + 1
		return v
	})
	if err != nil || !ok || updated.Count != 1 {
		t.Fatalf("Update c = %+v, %v, %v", updated, ok, err)
	}
	if _, ok, err := m.Update("missing", func(v testValue) testValue { return v }); err != nil || ok {
		t.Fatalf("Update missing = ok=%v, err=%v", ok, err)
	}

	// DeleteIf
	deleted, err := m.DeleteIf("b", func(v testValue) bool { return v.Name == "beta" })
	if err != nil || !deleted {
		t.Fatalf("DeleteIf b = %v, %v", deleted, err)
	}
	deleted, err = m.DeleteIf("b", func(v testValue) bool { return true })
	if err != nil || deleted {
		t.Fatalf("DeleteIf b again = %v, %v", deleted, err)
	}

	// Take
	taken, ok, err := m.Take("c")
	if err != nil || !ok || taken.Count != 1 {
		t.Fatalf("Take c = %+v, %v, %v", taken, ok, err)
	}
	if _, ok, err := m.Take("c"); err != nil || ok {
		t.Fatalf("Take c again = ok=%v, err=%v", ok, err)
	}

	// Delete
	if err := m.Delete("a"); err != nil {
		t.Fatalf("Delete a: %v", err)
	}
	if values, err := m.All(); err != nil || len(values) != 0 {
		t.Fatalf("All after deletes = %+v, %v", values, err)
	}
}

func TestMemoryMapFullContract(t *testing.T) {
	exerciseDurableMap(t, NewMemoryMap[testValue]())
}

func TestSQLiteMapFullContract(t *testing.T) {
	dir := t.TempDir()
	m, err := NewSQLiteMap[testValue](dir, "testmap")
	if err != nil {
		t.Fatalf("NewSQLiteMap: %v", err)
	}
	exerciseDurableMap(t, m)
}

func TestSQLiteMapPersistsAcrossReopen(t *testing.T) {
	dir := t.TempDir()
	m, err := NewSQLiteMap[testValue](dir, "persist")
	if err != nil {
		t.Fatalf("NewSQLiteMap: %v", err)
	}
	if err := m.Put("k1", testValue{Name: "one", Count: 1}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := m.Put("k2", testValue{Name: "two", Count: 2, Tags: []string{"x", "y"}}); err != nil {
		t.Fatalf("Put: %v", err)
	}

	// Reopen a fresh map over the same db file.
	reopened, err := NewSQLiteMap[testValue](dir, "persist")
	if err != nil {
		t.Fatalf("NewSQLiteMap reopen: %v", err)
	}
	values, err := reopened.All()
	if err != nil || len(values) != 2 {
		t.Fatalf("All after reopen = %+v, %v", values, err)
	}
	got, ok, err := reopened.Get("k2")
	if err != nil || !ok || got.Name != "two" || len(got.Tags) != 2 {
		t.Fatalf("Get k2 after reopen = %+v, %v, %v", got, ok, err)
	}
}

func TestSQLiteMapRejectsInvalidTableName(t *testing.T) {
	if _, err := NewSQLiteMap[testValue](t.TempDir(), "bad-table;drop"); err == nil {
		t.Fatalf("expected error for invalid table name")
	}
}

func TestSQLiteMapDBFileLocation(t *testing.T) {
	dir := t.TempDir()
	m, err := NewSQLiteMap[testValue](dir, "locmap")
	if err != nil {
		t.Fatalf("NewSQLiteMap: %v", err)
	}
	if err := m.Put("x", testValue{Name: "x"}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	want := filepath.Join(dir, "locmap.db")
	if _, err := fileExists(want); err != nil {
		t.Fatalf("expected db file at %s: %v", want, err)
	}
}

func fileExists(path string) (bool, error) {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	return !info.IsDir(), nil
}
