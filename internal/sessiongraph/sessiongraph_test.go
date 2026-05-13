package sessiongraph

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestEnsureMainBranchInitializesEmptyGraph(t *testing.T) {
	var graph SessionGraph

	head := graph.EnsureMainBranch()

	if head.BranchID != MainBranchID {
		t.Fatalf("main branch ID = %q, want %q", head.BranchID, MainBranchID)
	}
	if head.Head != "" {
		t.Fatalf("main head = %q, want empty", head.Head)
	}
	if got, ok := graph.Head(MainBranchID); !ok || got != head {
		t.Fatalf("Head(main) = (%+v, %v), want (%+v, true)", got, ok, head)
	}
	if len(graph.Nodes()) != 0 {
		t.Fatalf("empty graph has %d nodes, want 0", len(graph.Nodes()))
	}
}

func TestAppendNodeAdvancesBranchHead(t *testing.T) {
	var graph SessionGraph
	graph.EnsureMainBranch()

	node, err := graph.AppendNode(MainBranchID, "n1", CheckpointRecord{
		CheckpointID: "cp1",
		Summary:      "first checkpoint",
	})
	if err != nil {
		t.Fatalf("AppendNode failed: %v", err)
	}

	if node.ID != "n1" || node.Parent != "" || node.BranchID != MainBranchID {
		t.Fatalf("node = %+v, want ID n1 on main without parent", node)
	}
	head, ok := graph.Head(MainBranchID)
	if !ok || head.Head != "n1" {
		t.Fatalf("Head(main) = (%+v, %v), want n1", head, ok)
	}

	node, err = graph.AppendNode(MainBranchID, "n2", CheckpointRecord{CheckpointID: "cp2"})
	if err != nil {
		t.Fatalf("AppendNode second node failed: %v", err)
	}
	if node.Parent != "n1" {
		t.Fatalf("second node parent = %q, want n1", node.Parent)
	}
}

func TestCloneBranchAndRollbackBranch(t *testing.T) {
	var graph SessionGraph
	graph.EnsureMainBranch()
	if _, err := graph.AppendNode(MainBranchID, "n1", CheckpointRecord{CheckpointID: "cp1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := graph.AppendNode(MainBranchID, "n2", CheckpointRecord{CheckpointID: "cp2"}); err != nil {
		t.Fatal(err)
	}

	cloned, err := graph.CloneBranch(MainBranchID, "feature")
	if err != nil {
		t.Fatalf("CloneBranch failed: %v", err)
	}
	if cloned.Head != "n2" {
		t.Fatalf("cloned head = %q, want n2", cloned.Head)
	}

	rolledBack, err := graph.RollbackBranch("feature", "n1")
	if err != nil {
		t.Fatalf("RollbackBranch failed: %v", err)
	}
	if rolledBack.Head != "n1" {
		t.Fatalf("rolled back head = %q, want n1", rolledBack.Head)
	}
	mainHead, _ := graph.Head(MainBranchID)
	if mainHead.Head != "n2" {
		t.Fatalf("main head = %q, want unchanged n2", mainHead.Head)
	}
}

func TestMergeBranchAppendsMergeRecord(t *testing.T) {
	var graph SessionGraph
	graph.EnsureMainBranch()
	if _, err := graph.AppendNode(MainBranchID, "n1", CheckpointRecord{CheckpointID: "cp1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := graph.CloneBranch(MainBranchID, "feature"); err != nil {
		t.Fatal(err)
	}
	if _, err := graph.AppendNode("feature", "n2", CheckpointRecord{CheckpointID: "cp2"}); err != nil {
		t.Fatal(err)
	}

	node, err := graph.MergeBranch(MainBranchID, "feature", "n3", MergeRecord{
		MergeID: "merge1",
		Summary: "merge feature",
	})
	if err != nil {
		t.Fatalf("MergeBranch failed: %v", err)
	}

	if node.ID != "n3" || node.Parent != "n1" {
		t.Fatalf("merge node = %+v, want ID n3 parent n1", node)
	}
	if node.Merge == nil {
		t.Fatalf("merge node has nil Merge record")
	}
	if node.Merge.SourceBranch != "feature" || node.Merge.SourceHead != "n2" {
		t.Fatalf("merge source = %q/%q, want feature/n2", node.Merge.SourceBranch, node.Merge.SourceHead)
	}
	head, _ := graph.Head(MainBranchID)
	if head.Head != "n3" {
		t.Fatalf("main head = %q, want n3", head.Head)
	}
}

func TestStoreJSONRoundTripAndMissingLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "graph.json")
	store := NewStore(path)

	loaded, err := store.Load()
	if err != nil {
		t.Fatalf("Load missing graph failed: %v", err)
	}
	if len(loaded.Branches) != 0 || len(loaded.Nodes()) != 0 {
		t.Fatalf("missing graph load = %+v, want empty", loaded)
	}

	loaded.EnsureMainBranch()
	if _, err := loaded.AppendNode(MainBranchID, "n1", CheckpointRecord{CheckpointID: "cp1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := loaded.CloneBranch(MainBranchID, "feature"); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(loaded); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("graph.json not written: %v", err)
	}
	roundTripped, err := store.Load()
	if err != nil {
		t.Fatalf("Load saved graph failed: %v", err)
	}
	if !reflect.DeepEqual(loaded, roundTripped) {
		t.Fatalf("round trip mismatch:\n got: %+v\nwant: %+v", roundTripped, loaded)
	}
}
