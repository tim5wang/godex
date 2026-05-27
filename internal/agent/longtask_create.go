package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/tim5wang/godex/internal/platform/fsutil"
)

// storyCompiler abstracts the compilation of longtask stories into workflow nodes.
type storyCompiler interface {
	CompileStories(args longTaskArgs, stories []longTaskStoryInput) ([]workflowNodeInput, error)
}

// longTaskDefaultStoryCompiler is the default storyCompiler implementation.
type longTaskDefaultStoryCompiler struct{}

func (c *longTaskDefaultStoryCompiler) CompileStories(args longTaskArgs, stories []longTaskStoryInput) ([]workflowNodeInput, error) {
	nodes := make([]workflowNodeInput, 0, len(stories))
	for i, story := range stories {
		deps := []string{}
		if i > 0 {
			deps = []string{stories[i-1].ID}
		}
		handoffPolicy := workflowHandoffPolicySummary
		handoffMaxBytes := workflowDefaultHandoffMaxBytes
		if strings.TrimSpace(story.HandoffPolicy) != "" {
			handoffPolicy = story.HandoffPolicy
		}
		if story.HandoffMaxBytes > 0 {
			handoffMaxBytes = story.HandoffMaxBytes
		}
		nodes = append(nodes, workflowNodeInput{
			ID:              story.ID,
			Kind:            "story",
			Title:           story.Title,
			Prompt:          renderLongTaskStoryPrompt(args, story),
			DependsOn:       deps,
			HandoffPolicy:   handoffPolicy,
			HandoffMaxBytes: handoffMaxBytes,
			AgentType:       story.AgentType,
			WriteScope:      story.WriteScope,
		})
	}
	return nodes, nil
}

func (a *Agent) createLongTask(sessionID string, args longTaskArgs) (longTaskView, error) {
	id := strings.TrimSpace(args.LongTaskID)
	if id == "" {
		id = strings.TrimSpace(args.WorkflowID)
	}
	if id == "" {
		id = "lt_" + fmt.Sprintf("%d", time.Now().UTC().UnixNano())
	}
	workflowID := strings.TrimSpace(args.WorkflowID)
	if workflowID == "" {
		workflowID = id
	}
	if len(args.Stories) == 0 {
		return longTaskView{}, fmt.Errorf("missing longtask stories")
	}
	now := time.Now().UTC()
	stories := normalizeLongTaskStories(args.Stories)

	compiler := &longTaskDefaultStoryCompiler{}
	nodes, err := compiler.CompileStories(args, stories)
	if err != nil {
		return longTaskView{}, fmt.Errorf("compile stories: %w", err)
	}

	state, err := a.workflows.create(sessionID, workflowID, nodes, nil)
	if err != nil {
		return longTaskView{}, err
	}
	spec := longTaskSpec{
		ID:                  id,
		WorkflowID:          workflowID,
		Project:             strings.TrimSpace(args.Project),
		BranchName:          strings.TrimSpace(args.BranchName),
		Description:         strings.TrimSpace(args.Description),
		QualityChecks:       normalizeWorkflowStrings(args.QualityChecks),
		ValidationTimeoutMS: args.ValidationTimeoutMS,
		MergePolicy:         normalizeLongTaskMergePolicy(args.MergePolicy),
		CommitPolicy:        normalizeLongTaskCommitPolicy(args.CommitPolicy),
		Stories:             stories,
		CreatedAt:           now,
		UpdatedAt:           now,
	}
	if err := a.workflows.writeLongTaskSpec(workflowID, spec); err != nil {
		return longTaskView{}, err
	}
	_ = a.workflows.appendEvent(workflowID, map[string]interface{}{"event": "longtask_created", "longtask_id": id, "stories": len(stories), "at": now})
	return a.longTaskViewForState(state)
}

func normalizeLongTaskStories(input []longTaskStoryInput) []longTaskStoryInput {
	stories := make([]longTaskStoryInput, 0, len(input))
	for i, story := range input {
		story.ID = strings.TrimSpace(story.ID)
		if story.ID == "" {
			story.ID = fmt.Sprintf("US-%03d", i+1)
		}
		story.Title = strings.TrimSpace(story.Title)
		story.Description = strings.TrimSpace(story.Description)
		story.AcceptanceCriteria = normalizeWorkflowStrings(story.AcceptanceCriteria)
		story.AgentType = strings.TrimSpace(story.AgentType)
		story.WriteScope = normalizeWorkflowStrings(story.WriteScope)
		story.HandoffPolicy = strings.TrimSpace(story.HandoffPolicy)
		if story.AgentType == "" && len(story.WriteScope) > 0 {
			story.AgentType = "general-purpose"
		}
		stories = append(stories, story)
	}
	sort.SliceStable(stories, func(i, j int) bool {
		pi, pj := stories[i].Priority, stories[j].Priority
		if pi <= 0 {
			pi = 1 << 30
		}
		if pj <= 0 {
			pj = 1 << 30
		}
		return pi < pj
	})
	return stories
}

func normalizeLongTaskMergePolicy(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", longTaskMergePolicyAutoMerge:
		return longTaskMergePolicyAutoMerge
	case longTaskMergePolicyReviewOnly:
		return longTaskMergePolicyReviewOnly
	default:
		return longTaskMergePolicyAutoMerge
	}
}

func normalizeLongTaskCommitPolicy(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", longTaskCommitPolicyAuto:
		return longTaskCommitPolicyAuto
	case longTaskCommitPolicyNone:
		return longTaskCommitPolicyNone
	default:
		return longTaskCommitPolicyAuto
	}
}

func renderLongTaskStoryPrompt(args longTaskArgs, story longTaskStoryInput) string {
	var builder strings.Builder
	builder.WriteString("You are executing one story in a Ralph-style GoDex long task. Work on this story only; do not start other stories.\n\n")
	if project := strings.TrimSpace(args.Project); project != "" {
		builder.WriteString("Project: ")
		builder.WriteString(project)
		builder.WriteString("\n")
	}
	if branch := strings.TrimSpace(args.BranchName); branch != "" {
		builder.WriteString("Target branch: ")
		builder.WriteString(branch)
		builder.WriteString("\n")
	}
	if desc := strings.TrimSpace(args.Description); desc != "" {
		builder.WriteString("Long task description: ")
		builder.WriteString(desc)
		builder.WriteString("\n")
	}
	builder.WriteString("\nStory ID: ")
	builder.WriteString(story.ID)
	if story.Title != "" {
		builder.WriteString("\nTitle: ")
		builder.WriteString(story.Title)
	}
	if story.Description != "" {
		builder.WriteString("\nDescription: ")
		builder.WriteString(story.Description)
	}
	if len(story.AcceptanceCriteria) > 0 {
		builder.WriteString("\nAcceptance criteria:")
		for _, item := range story.AcceptanceCriteria {
			builder.WriteString("\n- ")
			builder.WriteString(item)
		}
	}
	checks := normalizeWorkflowStrings(args.QualityChecks)
	if len(checks) > 0 {
		builder.WriteString("\n\nRequired quality checks before reporting pass:")
		for _, check := range checks {
			builder.WriteString("\n- ")
			builder.WriteString(check)
		}
	}
	builder.WriteString("\n\nCompletion contract:\n- Keep changes minimal and focused on this story.\n- Run the relevant checks above when possible.\n- Finish with an explicit line: Verdict: pass|fail|blocked|needs_fix.\n- Include a compact summary, changed files, validation run, and reusable learnings.\n")
	return builder.String()
}

func storyAgentType(spec longTaskSpec, storyID string) string {
	for _, story := range spec.Stories {
		if story.ID == storyID {
			return story.AgentType
		}
	}
	return ""
}

func storyWriteScope(spec longTaskSpec, storyID string) []string {
	for _, story := range spec.Stories {
		if story.ID == storyID {
			return append([]string{}, story.WriteScope...)
		}
	}
	return nil
}

func (s *workflowStore) writeLongTaskSpec(workflowID string, spec longTaskSpec) error {
	if s == nil || strings.TrimSpace(s.dir) == "" {
		return fmt.Errorf("workflow store is unavailable")
	}
	workflowID = strings.TrimSpace(workflowID)
	if err := validateWorkflowID(workflowID); err != nil {
		return err
	}
	path := filepath.Join(s.dir, workflowID, longTaskSpecFile)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	return fsutil.WriteJSONAtomic(path, spec, 0644)
}

func (s *workflowStore) loadLongTaskSpec(workflowID string) (longTaskSpec, error) {
	if s == nil || strings.TrimSpace(s.dir) == "" {
		return longTaskSpec{}, fmt.Errorf("workflow store is unavailable")
	}
	workflowID = strings.TrimSpace(workflowID)
	if err := validateWorkflowID(workflowID); err != nil {
		return longTaskSpec{}, err
	}
	var spec longTaskSpec
	if err := readJSONFile(filepath.Join(s.dir, workflowID, longTaskSpecFile), &spec); err != nil {
		return longTaskSpec{}, fmt.Errorf("read longtask spec: %w", err)
	}
	if spec.WorkflowID == "" {
		spec.WorkflowID = workflowID
	}
	if spec.ID == "" {
		spec.ID = workflowID
	}
	spec.MergePolicy = normalizeLongTaskMergePolicy(spec.MergePolicy)
	spec.CommitPolicy = normalizeLongTaskCommitPolicy(spec.CommitPolicy)
	return spec, nil
}
