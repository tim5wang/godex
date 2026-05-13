package sessiongraph

import (
	"errors"
	"fmt"
	"sort"
)

type NodeID string

type BranchID string

const MainBranchID BranchID = "branch:main"

type SessionGraph struct {
	Branches map[BranchID]BranchHead `json:"branches,omitempty"`
	NodeSet  map[NodeID]GraphNode    `json:"nodes,omitempty"`
}

type BranchHead struct {
	BranchID BranchID `json:"branch_id"`
	Head     NodeID   `json:"head,omitempty"`
}

type GraphNode struct {
	ID         NodeID            `json:"id"`
	BranchID   BranchID          `json:"branch_id"`
	Parent     NodeID            `json:"parent,omitempty"`
	Checkpoint *CheckpointRecord `json:"checkpoint,omitempty"`
	Merge      *MergeRecord      `json:"merge,omitempty"`
}

type CheckpointRecord struct {
	CheckpointID string `json:"checkpoint_id"`
	Summary      string `json:"summary,omitempty"`
}

type MergeRecord struct {
	MergeID      string   `json:"merge_id"`
	SourceBranch BranchID `json:"source_branch"`
	SourceHead   NodeID   `json:"source_head,omitempty"`
	Summary      string   `json:"summary,omitempty"`
}

var (
	ErrEmptyID      = errors.New("sessiongraph: empty ID")
	ErrDuplicateID  = errors.New("sessiongraph: duplicate node ID")
	ErrBranchExists = errors.New("sessiongraph: branch already exists")
	ErrNotFound     = errors.New("sessiongraph: not found")
)

func (g *SessionGraph) EnsureMainBranch() BranchHead {
	g.ensureMaps()
	if head, ok := g.Branches[MainBranchID]; ok {
		return head
	}
	head := BranchHead{BranchID: MainBranchID}
	g.Branches[MainBranchID] = head
	return head
}

func (g *SessionGraph) AppendNode(branchID BranchID, nodeID NodeID, checkpoint CheckpointRecord) (GraphNode, error) {
	if branchID == "" || nodeID == "" {
		return GraphNode{}, ErrEmptyID
	}
	g.ensureMaps()
	head, ok := g.Branches[branchID]
	if !ok {
		return GraphNode{}, fmt.Errorf("%w: branch %q", ErrNotFound, branchID)
	}
	if _, exists := g.NodeSet[nodeID]; exists {
		return GraphNode{}, fmt.Errorf("%w: node %q", ErrDuplicateID, nodeID)
	}
	node := GraphNode{
		ID:         nodeID,
		BranchID:   branchID,
		Parent:     head.Head,
		Checkpoint: &checkpoint,
	}
	g.NodeSet[nodeID] = node
	g.Branches[branchID] = BranchHead{BranchID: branchID, Head: nodeID}
	return node, nil
}

func (g *SessionGraph) CloneBranch(from BranchID, to BranchID) (BranchHead, error) {
	if from == "" || to == "" {
		return BranchHead{}, ErrEmptyID
	}
	g.ensureMaps()
	source, ok := g.Branches[from]
	if !ok {
		return BranchHead{}, fmt.Errorf("%w: branch %q", ErrNotFound, from)
	}
	if _, exists := g.Branches[to]; exists {
		return BranchHead{}, fmt.Errorf("%w: branch %q", ErrBranchExists, to)
	}
	head := BranchHead{BranchID: to, Head: source.Head}
	g.Branches[to] = head
	return head, nil
}

func (g *SessionGraph) RollbackBranch(branchID BranchID, nodeID NodeID) (BranchHead, error) {
	if branchID == "" {
		return BranchHead{}, ErrEmptyID
	}
	g.ensureMaps()
	if _, ok := g.Branches[branchID]; !ok {
		return BranchHead{}, fmt.Errorf("%w: branch %q", ErrNotFound, branchID)
	}
	if nodeID != "" {
		if _, ok := g.NodeSet[nodeID]; !ok {
			return BranchHead{}, fmt.Errorf("%w: node %q", ErrNotFound, nodeID)
		}
	}
	head := BranchHead{BranchID: branchID, Head: nodeID}
	g.Branches[branchID] = head
	return head, nil
}

func (g *SessionGraph) MergeBranch(target BranchID, source BranchID, nodeID NodeID, record MergeRecord) (GraphNode, error) {
	if target == "" || source == "" || nodeID == "" {
		return GraphNode{}, ErrEmptyID
	}
	g.ensureMaps()
	targetHead, ok := g.Branches[target]
	if !ok {
		return GraphNode{}, fmt.Errorf("%w: branch %q", ErrNotFound, target)
	}
	sourceHead, ok := g.Branches[source]
	if !ok {
		return GraphNode{}, fmt.Errorf("%w: branch %q", ErrNotFound, source)
	}
	if _, exists := g.NodeSet[nodeID]; exists {
		return GraphNode{}, fmt.Errorf("%w: node %q", ErrDuplicateID, nodeID)
	}
	record.SourceBranch = source
	record.SourceHead = sourceHead.Head
	node := GraphNode{
		ID:       nodeID,
		BranchID: target,
		Parent:   targetHead.Head,
		Merge:    &record,
	}
	g.NodeSet[nodeID] = node
	g.Branches[target] = BranchHead{BranchID: target, Head: nodeID}
	return node, nil
}

func (g *SessionGraph) Head(branchID BranchID) (BranchHead, bool) {
	if g == nil || g.Branches == nil {
		return BranchHead{}, false
	}
	head, ok := g.Branches[branchID]
	return head, ok
}

func (g *SessionGraph) Nodes() []GraphNode {
	if g == nil || len(g.NodeSet) == 0 {
		return nil
	}
	ids := make([]string, 0, len(g.NodeSet))
	for id := range g.NodeSet {
		ids = append(ids, string(id))
	}
	sort.Strings(ids)

	nodes := make([]GraphNode, 0, len(ids))
	for _, id := range ids {
		nodes = append(nodes, g.NodeSet[NodeID(id)])
	}
	return nodes
}

func (g *SessionGraph) Clone() *SessionGraph {
	if g == nil {
		return &SessionGraph{}
	}
	cloned := &SessionGraph{
		Branches: make(map[BranchID]BranchHead, len(g.Branches)),
		NodeSet:  make(map[NodeID]GraphNode, len(g.NodeSet)),
	}
	for id, head := range g.Branches {
		cloned.Branches[id] = head
	}
	for id, node := range g.NodeSet {
		if node.Checkpoint != nil {
			checkpoint := *node.Checkpoint
			node.Checkpoint = &checkpoint
		}
		if node.Merge != nil {
			merge := *node.Merge
			node.Merge = &merge
		}
		cloned.NodeSet[id] = node
	}
	return cloned
}

func (g *SessionGraph) ensureMaps() {
	if g.Branches == nil {
		g.Branches = make(map[BranchID]BranchHead)
	}
	if g.NodeSet == nil {
		g.NodeSet = make(map[NodeID]GraphNode)
	}
}
