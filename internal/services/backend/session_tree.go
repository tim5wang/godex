package backend

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/tim5wang/godex/internal/domain/security"
	"github.com/tim5wang/godex/internal/sessiongraph"
)

// SessionGraphView summarizes one session's checkpoint/merge graph for the
// session-tree API (roadmap 6.3).
type SessionGraphView struct {
	MainHead  string   `json:"main_head,omitempty"`
	Branches  []string `json:"branches,omitempty"`
	Nodes     int      `json:"nodes"`
	Checkpoints int    `json:"checkpoints,omitempty"`
	Merges    int      `json:"merges,omitempty"`
}

// SessionTreeNode describes one node in the session fork tree (roadmap 6.3).
type SessionTreeNode struct {
	SessionID              string             `json:"session_id"`
	Title                  string             `json:"title,omitempty"`
	ParentSessionID        string             `json:"parent_session_id,omitempty"`
	ForkedFromTurnID       string             `json:"forked_from_turn_id,omitempty"`
	ForkedFromMessageIndex *int               `json:"forked_from_message_index,omitempty"`
	BranchTitle            string             `json:"branch_title,omitempty"`
	CreatedAt              time.Time          `json:"created_at"`
	UpdatedAt              time.Time          `json:"updated_at"`
	Graph                  *SessionGraphView  `json:"graph,omitempty"`
	Children               []*SessionTreeNode `json:"children,omitempty"`
}

// SessionTree returns the fork tree rooted at the given session. All sessions
// linked through parent_session_id are discovered from disk (no need to open
// them); the root itself need not have a parent.
func (s *Service) SessionTree(ctx context.Context, sessionID string) (*SessionTreeNode, error) {
	listed, err := s.ListSessions(ctx, SessionListFilter{})
	if err != nil {
		return nil, err
	}
	byID := make(map[string]*SessionTreeNode, len(listed))
	for i := range listed {
		item := listed[i]
		byID[item.SessionID] = &SessionTreeNode{
			SessionID:              item.SessionID,
			Title:                  item.Title,
			ParentSessionID:        item.ParentSessionID,
			ForkedFromTurnID:       item.ForkedFromTurnID,
			ForkedFromMessageIndex: cloneIntPtr(item.ForkedFromMessageIndex),
			BranchTitle:            item.BranchTitle,
			CreatedAt:              item.CreatedAt,
			UpdatedAt:              item.UpdatedAt,
		}
	}
	root := byID[sessionID]
	if root == nil {
		return nil, newSessionNotFoundError(sessionID)
	}
	for id := range byID {
		if id == sessionID {
			continue
		}
		node := byID[id]
		if parent, ok := byID[node.ParentSessionID]; ok && parent != nil {
			parent.Children = append(parent.Children, node)
		}
	}
	for id := range byID {
		node := byID[id]
		if g, err := s.loadSessionGraph(id); err == nil && g != nil {
			node.Graph = summarizeSessionGraph(g)
		}
		sortSessionTreeChildren(node)
	}
	return root, nil
}

func summarizeSessionGraph(g *sessiongraph.SessionGraph) *SessionGraphView {
	if g == nil {
		return nil
	}
	view := &SessionGraphView{}
	if head, ok := g.Head(sessiongraph.MainBranchID); ok {
		view.MainHead = string(head.Head)
	}
	branches := make([]string, 0, len(g.Branches))
	for b := range g.Branches {
		branches = append(branches, string(b))
	}
	sort.Strings(branches)
	view.Branches = branches
	view.Nodes = len(g.NodeSet)
	for _, node := range g.NodeSet {
		if node.Checkpoint != nil {
			view.Checkpoints++
		}
		if node.Merge != nil {
			view.Merges++
		}
	}
	return view
}

func sortSessionTreeChildren(node *SessionTreeNode) {
	sort.Slice(node.Children, func(i, j int) bool {
		if node.Children[i].UpdatedAt.Equal(node.Children[j].UpdatedAt) {
			return node.Children[i].SessionID < node.Children[j].SessionID
		}
		return node.Children[i].UpdatedAt.After(node.Children[j].UpdatedAt)
	})
}

// RollbackSession rolls the session's main branch head back to the given graph
// node (roadmap 6.3: "回滚到早期 node"). The graph persists the rollback; the
// agent transcript is not truncated by this call — callers can rebuild context
// from the checkpoint recorded on the target node.
func (s *Service) RollbackSession(ctx context.Context, sessionID, nodeID string) (*SessionGraphView, error) {
	_ = ctx
	session, err := s.requireSession(sessionID)
	if err != nil {
		return nil, err
	}
	session.mu.Lock()
	if session.graph == nil {
		session.graph = &sessiongraph.SessionGraph{}
	}
	session.graph.EnsureMainBranch()
	if _, err := session.graph.RollbackBranch(sessiongraph.MainBranchID, sessiongraph.NodeID(nodeID)); err != nil {
		session.mu.Unlock()
		return nil, fmt.Errorf("rollback session: %w", err)
	}
	graph := session.graph.Clone()
	session.mu.Unlock()
	if err := s.writeSessionGraph(session); err != nil {
		return nil, err
	}
	s.appendSecurityEvent(security.SecurityEvent{
		At:        s.now(),
		Category:  "knowledge",
		Action:    "rollback_session",
		Severity:  "info",
		SessionID: sessionID,
		Summary:   "Rolled session main branch back to node " + nodeID,
		Metadata: map[string]string{
			"node_id": nodeID,
		},
	})
	return summarizeSessionGraph(graph), nil
}

// MergeSessionBranch merges a worker branch back into the session main branch
// (roadmap 6.3: "把 worker 的结果、摘要、diff 和决策合并回主分支"). The merge is
// recorded as a graph merge node on the main branch.
func (s *Service) MergeSessionBranch(ctx context.Context, sessionID, branchID, summary string) (*SessionGraphView, error) {
	_ = ctx
	session, err := s.requireSession(sessionID)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(branchID) == "" {
		return nil, fmt.Errorf("merge session branch: empty branch id")
	}
	nodeID := sessionGraphNodeID("merge", randomSuffix(8))
	session.mu.Lock()
	if session.graph == nil {
		session.graph = &sessiongraph.SessionGraph{}
	}
	session.graph.EnsureMainBranch()
	_, err = session.graph.MergeBranch(sessiongraph.MainBranchID, sessiongraph.BranchID(branchID), nodeID, sessiongraph.MergeRecord{
		MergeID: strings.TrimSpace(branchID),
		Summary: strings.TrimSpace(summary),
	})
	if err != nil {
		session.mu.Unlock()
		return nil, fmt.Errorf("merge session branch: %w", err)
	}
	graph := session.graph.Clone()
	session.mu.Unlock()
	if err := s.writeSessionGraph(session); err != nil {
		return nil, err
	}
	s.appendSecurityEvent(security.SecurityEvent{
		At:        s.now(),
		Category:  "knowledge",
		Action:    "merge_session_branch",
		Severity:  "info",
		SessionID: sessionID,
		Summary:   "Merged worker branch " + branchID + " into session main branch",
		Metadata: map[string]string{
			"branch_id": branchID,
			"node_id":   string(nodeID),
		},
	})
	return summarizeSessionGraph(graph), nil
}
