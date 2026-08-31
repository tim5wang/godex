package backend

import (
	"context"
	"testing"
	"time"

	"github.com/tim5wang/godex/internal/contracts/protocol"
	"github.com/tim5wang/godex/internal/domain/message"
	"github.com/tim5wang/godex/internal/sessiongraph"
)

func TestSessionTreeBuildsForkHierarchy(t *testing.T) {
	cfg := newTestConfig(t)
	service := newTestService(cfg, &stubCaller{responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("done")}}}})

	opened, err := service.OpenSession(context.Background(), SessionLocator{Channel: "web", Key: "tree-root"})
	if err != nil {
		t.Fatalf("open session: %v", err)
	}
	if _, err := service.Submit(context.Background(), opened.SessionID, message.NewTextEnvelope(message.SourceWeb, opened.SessionID, cfg.LeadName, "hello", time.Now())); err != nil {
		t.Fatalf("submit: %v", err)
	}

	forked, err := service.ForkSession(context.Background(), opened.SessionID, ForkRequest{Title: "experiment"})
	if err != nil {
		t.Fatalf("fork session: %v", err)
	}

	root, err := service.SessionTree(context.Background(), opened.SessionID)
	if err != nil {
		t.Fatalf("session tree: %v", err)
	}
	if root.SessionID != opened.SessionID {
		t.Fatalf("expected root %s, got %s", opened.SessionID, root.SessionID)
	}
	if len(root.Children) != 1 || root.Children[0].SessionID != forked.SessionID {
		t.Fatalf("expected exactly one fork child %s, got %+v", forked.SessionID, root.Children)
	}
	if root.Children[0].ParentSessionID != opened.SessionID || root.Children[0].BranchTitle != "experiment" {
		t.Fatalf("expected fork child metadata, got %+v", root.Children[0])
	}
	if root.Graph == nil || root.Graph.Nodes == 0 {
		t.Fatalf("expected root graph summary, got %+v", root.Graph)
	}
}

func TestSessionTreeUnknownRootFails(t *testing.T) {
	cfg := newTestConfig(t)
	service := newTestService(cfg, &stubCaller{responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("done")}}}})

	if _, err := service.SessionTree(context.Background(), "no-such-session"); err == nil {
		t.Fatalf("expected error for unknown session")
	}
}

func TestRollbackSessionMovesMainBranchHead(t *testing.T) {
	cfg := newTestConfig(t)
	service := newTestService(cfg, &stubCaller{responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("done")}}}})

	opened, err := service.OpenSession(context.Background(), SessionLocator{Channel: "web", Key: "rollback-root"})
	if err != nil {
		t.Fatalf("open session: %v", err)
	}
	if _, err := service.Submit(context.Background(), opened.SessionID, message.NewTextEnvelope(message.SourceWeb, opened.SessionID, cfg.LeadName, "hello", time.Now())); err != nil {
		t.Fatalf("submit: %v", err)
	}
	session, err := service.requireSession(opened.SessionID)
	if err != nil {
		t.Fatalf("require session: %v", err)
	}
	if err := service.appendSessionGraphCheckpoint(session, "cp-1", "first"); err != nil {
		t.Fatalf("append cp-1: %v", err)
	}
	if err := service.appendSessionGraphCheckpoint(session, "cp-2", "second"); err != nil {
		t.Fatalf("append cp-2: %v", err)
	}
	graph := readTestSessionGraph(t, service, opened.SessionID)
	if head, ok := graph.Head(sessiongraph.MainBranchID); !ok || head.Head == "" {
		t.Fatalf("expected main branch head, got %+v", head)
	}
	target := string(sessionGraphNodeID("checkpoint", "cp-1"))
	view, err := service.RollbackSession(context.Background(), opened.SessionID, target)
	if err != nil {
		t.Fatalf("rollback session: %v", err)
	}
	if view == nil || view.MainHead != target {
		t.Fatalf("expected main head %s after rollback, got %+v", target, view)
	}
	graph = readTestSessionGraph(t, service, opened.SessionID)
	if head, ok := graph.Head(sessiongraph.MainBranchID); !ok || string(head.Head) != target {
		t.Fatalf("expected persisted main head %s, got %+v", target, head)
	}
}

func TestRollbackSessionUnknownNodeFails(t *testing.T) {
	cfg := newTestConfig(t)
	service := newTestService(cfg, &stubCaller{responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("done")}}}})

	opened, err := service.OpenSession(context.Background(), SessionLocator{Channel: "web", Key: "rollback-bad"})
	if err != nil {
		t.Fatalf("open session: %v", err)
	}
	if _, err := service.RollbackSession(context.Background(), opened.SessionID, "node:checkpoint:missing"); err == nil {
		t.Fatalf("expected error for unknown node")
	}
}

func TestMergeSessionBranchMergesWorkerBranch(t *testing.T) {
	cfg := newTestConfig(t)
	service := newTestService(cfg, &stubCaller{responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("done")}}}})

	opened, err := service.OpenSession(context.Background(), SessionLocator{Channel: "web", Key: "merge-root"})
	if err != nil {
		t.Fatalf("open session: %v", err)
	}
	if _, err := service.Submit(context.Background(), opened.SessionID, message.NewTextEnvelope(message.SourceWeb, opened.SessionID, cfg.LeadName, "hello", time.Now())); err != nil {
		t.Fatalf("submit: %v", err)
	}
	session, err := service.requireSession(opened.SessionID)
	if err != nil {
		t.Fatalf("require session: %v", err)
	}
	workerBranch := sessiongraph.BranchID("branch:worker-test")
	if err := service.cloneSessionGraphBranch(session, sessiongraph.MainBranchID, workerBranch, ""); err != nil {
		t.Fatalf("clone branch: %v", err)
	}

	before := readTestSessionGraph(t, service, opened.SessionID)
	beforeSummary := summarizeSessionGraph(before)
	view, err := service.MergeSessionBranch(context.Background(), opened.SessionID, string(workerBranch), "worker did the thing")
	if err != nil {
		t.Fatalf("merge session branch: %v", err)
	}
	if view == nil || view.Merges != beforeSummary.Merges+1 {
		t.Fatalf("expected merge count to increase, before=%+v after=%+v", beforeSummary, view)
	}
	graph := readTestSessionGraph(t, service, opened.SessionID)
	found := false
	for _, node := range graph.NodeSet {
		if node.Merge != nil && node.Merge.SourceBranch == workerBranch {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected merge record for worker branch, nodes=%+v", graph.NodeSet)
	}
}

func TestMergeSessionBranchEmptyBranchFails(t *testing.T) {
	cfg := newTestConfig(t)
	service := newTestService(cfg, &stubCaller{responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("done")}}}})

	opened, err := service.OpenSession(context.Background(), SessionLocator{Channel: "web", Key: "merge-bad"})
	if err != nil {
		t.Fatalf("open session: %v", err)
	}
	if _, err := service.MergeSessionBranch(context.Background(), opened.SessionID, "", "nope"); err == nil {
		t.Fatalf("expected error for empty branch id")
	}
}
