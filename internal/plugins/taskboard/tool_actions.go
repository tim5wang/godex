package taskboard

import (
	"context"
	"fmt"
	"strings"

	"github.com/tim5wang/godex/internal/tools"
)

func listTaskboardCards(ledger toolLedger, args taskboardArgs) (tools.ToolResult, error) {
	cards := ledger.ListCards(CardFilter{
		ProjectID: strings.TrimSpace(args.ProjectID),
		Status:    strings.TrimSpace(args.Status),
		Urgency:   strings.TrimSpace(args.Urgency),
	})
	out := make([]compactCard, 0, len(cards))
	for _, card := range cards {
		out = append(out, compact(card))
	}
	return tools.ToolResult{Structured: map[string]any{"cards": out, "count": len(out)}}, nil
}

// statusTaskboard returns a read-only snapshot of board / execution counts for
// a project (the query behind the cron watchdog directive). It never mutates
// the ledger.
func statusTaskboard(ledger toolLedger, args taskboardArgs) (tools.ToolResult, error) {
	sc := ledger.StatusCounts(strings.TrimSpace(args.ProjectID))
	return tools.ToolResult{Structured: map[string]any{"status_counts": sc, "counts": sc.CountMap()}}, nil
}

func getTaskboardCard(ledger toolLedger, args taskboardArgs) (tools.ToolResult, error) {
	id, err := requireCardID(args)
	if err != nil {
		return tools.ToolResult{}, err
	}
	card, err := ledger.GetCard(id)
	if err != nil {
		return tools.ToolResult{}, err
	}
	return tools.ToolResult{Structured: map[string]any{"card": card}}, nil
}

func createTaskboardCard(ledger toolLedger, args taskboardArgs) (tools.ToolResult, error) {
	title := strings.TrimSpace(derefString(args.Title))
	if title == "" {
		return tools.ToolResult{}, fmt.Errorf("taskboard: title is required for create")
	}
	card, err := ledger.CreateCard(CreateCardInput{
		ProjectID:    strings.TrimSpace(args.ProjectID),
		Title:        title,
		Description:  derefString(args.Description),
		Prompt:       derefString(args.Prompt),
		Urgency:      args.Urgency,
		TemplateID:   strings.TrimSpace(args.TemplateID),
		WorkDir:      strings.TrimSpace(args.WorkDir),
		Checklist:    args.Checklist,
		TouchedPaths: args.TouchedPaths,
		Research:     args.Research,
		CreatedBy:    agentActor,
	})
	if err != nil {
		return tools.ToolResult{}, err
	}
	return tools.ToolResult{Structured: map[string]any{"card": compact(card), "version": card.Version}}, nil
}

func updateTaskboardCard(ledger toolLedger, args taskboardArgs) (tools.ToolResult, error) {
	id, version, err := taskboardMutationTarget(args)
	if err != nil {
		return tools.ToolResult{}, err
	}
	var urgency *string
	if value := strings.TrimSpace(args.Urgency); value != "" {
		urgency = &value
	}
	var templateID *string
	if value := strings.TrimSpace(args.TemplateID); value != "" {
		templateID = &value
	}
	var touchedPaths *[]string
	if len(args.TouchedPaths) > 0 {
		value := args.TouchedPaths
		touchedPaths = &value
	}
	card, err := writeWithRetry(ledger, id, version, func(v int) (Card, error) {
		return ledger.UpdateCard(id, v, agentActor, UpdateCardInput{
			Title: args.Title, Description: args.Description, Prompt: args.Prompt,
			Urgency: urgency, Blocked: args.Blocked, TemplateID: templateID,
			TouchedPaths: touchedPaths, Research: args.Research,
		})
	})
	if err != nil {
		return tools.ToolResult{}, err
	}
	return cardMutationResult(card), nil
}

func mutateTaskboardCard(ledger toolLedger, actor string, args taskboardArgs) (tools.ToolResult, error) {
	id, version, err := taskboardMutationTarget(args)
	if err != nil {
		return tools.ToolResult{}, err
	}
	card, err := writeWithRetry(ledger, id, version, func(v int) (Card, error) {
		switch strings.TrimSpace(args.Action) {
		case actionMove:
			return ledger.MoveCard(id, v, strings.TrimSpace(args.To), actor)
		case actionCommentAdd:
			return ledger.AddComment(id, v, agentActor, args.Text)
		case actionDelete:
			return ledger.SoftDeleteCard(id, v, agentActor)
		default:
			return Card{}, fmt.Errorf("taskboard: unknown mutation action %q", args.Action)
		}
	})
	if err != nil {
		return tools.ToolResult{}, err
	}
	if strings.TrimSpace(args.Action) == actionDelete {
		return tools.ToolResult{Structured: map[string]any{"card_id": card.ID, "deleted": true}}, nil
	}
	return cardMutationResult(card), nil
}

func updateTaskboardChecklist(ledger toolLedger, args taskboardArgs) (tools.ToolResult, error) {
	id, version, err := taskboardMutationTarget(args)
	if err != nil {
		return tools.ToolResult{}, err
	}
	var card Card
	switch strings.TrimSpace(args.CheckAction) {
	case "add":
		card, err = ledger.ChecklistAdd(id, version, agentActor, args.ChecklistItems())
	case "check":
		if args.Index == nil {
			return tools.ToolResult{}, fmt.Errorf("taskboard: check requires item index")
		}
		card, err = ledger.ChecklistCheck(id, version, agentActor, *args.Index, args.Evidence)
	case "uncheck":
		if args.Index == nil {
			return tools.ToolResult{}, fmt.Errorf("taskboard: uncheck requires item index")
		}
		card, err = ledger.ChecklistUncheck(id, version, agentActor, *args.Index)
	default:
		return tools.ToolResult{}, fmt.Errorf("taskboard: unknown checklist action %q", args.CheckAction)
	}
	if err != nil {
		return tools.ToolResult{}, err
	}
	done, total := card.ChecklistProgress()
	return tools.ToolResult{Structured: map[string]any{
		"card": compact(card), "version": card.Version,
		"checklist_done": done, "checklist_total": total,
	}}, nil
}

func dispatchTaskboardCard(ctx context.Context, ledger toolLedger, executor Executor, args taskboardArgs) (tools.ToolResult, error) {
	id, err := requireCardID(args)
	if err != nil {
		return tools.ToolResult{}, err
	}
	if executor == nil {
		return tools.ToolResult{}, fmt.Errorf("taskboard: dispatch unavailable (no executor configured)")
	}
	card, err := ledger.GetCard(id)
	if err != nil {
		return tools.ToolResult{}, err
	}
	if err := ledger.PrecheckDispatchConflicts(card); err != nil {
		return tools.ToolResult{}, err
	}
	executionID, sessionID, err := executor.Execute(ctx, card)
	if err != nil {
		return tools.ToolResult{}, err
	}
	return tools.ToolResult{Structured: map[string]any{"execution_id": executionID, "session_id": sessionID}}, nil
}

func observeTaskboardExecution(ctx context.Context, executor Executor, args taskboardArgs) (tools.ToolResult, error) {
	exec, ok := executor.(ObservedExecutor)
	if !ok {
		return tools.ToolResult{}, fmt.Errorf("taskboard: %s unavailable (executor does not support observability)", args.Action)
	}
	switch strings.TrimSpace(args.Action) {
	case actionObserve:
		id, executionID, err := taskboardExecutionTarget(args)
		if err != nil {
			return tools.ToolResult{}, err
		}
		observation, live, err := exec.Observe(ctx, id, executionID)
		if err != nil {
			return tools.ToolResult{}, err
		}
		return tools.ToolResult{Structured: map[string]any{"observation": observation, "live": live}}, nil
	case actionReconcile:
		report, err := exec.Reconcile(ctx)
		if err != nil {
			return tools.ToolResult{}, err
		}
		return tools.ToolResult{Structured: map[string]any{"reconcile_report": report}}, nil
	case actionRecover:
		id, executionID, err := taskboardExecutionTarget(args)
		if err != nil {
			return tools.ToolResult{}, err
		}
		sessionID, err := exec.Recover(ctx, id, executionID, args.Text)
		if err != nil {
			return tools.ToolResult{}, err
		}
		return tools.ToolResult{Structured: map[string]any{"session_id": sessionID, "message": "recovery message submitted"}}, nil
	case actionRetry:
		id, executionID, err := taskboardExecutionTarget(args)
		if err != nil {
			return tools.ToolResult{}, err
		}
		turnID, err := exec.Retry(ctx, id, executionID)
		if err != nil {
			return tools.ToolResult{}, err
		}
		return tools.ToolResult{Structured: map[string]any{"turn_id": turnID, "message": "retry submitted"}}, nil
	default:
		return tools.ToolResult{}, fmt.Errorf("taskboard: unknown execution action %q", args.Action)
	}
}

func reportTaskboardTouchedPaths(ledger toolLedger, actor string, args taskboardArgs) (tools.ToolResult, error) {
	id, version, err := taskboardMutationTarget(args)
	if err != nil {
		return tools.ToolResult{}, err
	}
	if len(args.ObservedPaths) == 0 {
		return tools.ToolResult{}, fmt.Errorf("taskboard: observed_paths is required for report_touched")
	}
	card, err := writeWithRetry(ledger, id, version, func(v int) (Card, error) {
		return ledger.ReportObservedPaths(id, v, actor, args.ObservedPaths)
	})
	if err != nil {
		return tools.ToolResult{}, err
	}
	report := ledger.CheckCardPathConflicts(card)
	return tools.ToolResult{Structured: map[string]any{
		"card": compact(card), "version": card.Version, "observed_paths": card.ObservedPaths,
		"conflict_report": report, "conflicts": report.HasConflicts(),
	}}, nil
}

func precheckTaskboardMerge(ledger toolLedger, actor string, args taskboardArgs) (tools.ToolResult, error) {
	id, version, err := taskboardMutationTarget(args)
	if err != nil {
		return tools.ToolResult{}, err
	}
	card, report, err := writeWithRetryMerge(ledger, id, version, func(v int) (Card, ConflictReport, error) {
		return ledger.MergePrecheck(id, v, actor)
	})
	if err != nil {
		return tools.ToolResult{}, err
	}
	return tools.ToolResult{Structured: map[string]any{
		"card": compact(card), "version": card.Version, "merge_report": report,
		"conflicts": report.HasConflicts(),
	}}, nil
}

func taskboardMutationTarget(args taskboardArgs) (string, int, error) {
	id, err := requireCardID(args)
	if err != nil {
		return "", 0, err
	}
	version, err := requireVersion(args)
	if err != nil {
		return "", 0, err
	}
	return id, version, nil
}

func taskboardExecutionTarget(args taskboardArgs) (string, string, error) {
	id, err := requireCardID(args)
	if err != nil {
		return "", "", err
	}
	executionID := strings.TrimSpace(args.ExecutionID)
	if executionID == "" {
		return "", "", fmt.Errorf("taskboard: execution_id is required for %s", args.Action)
	}
	return id, executionID, nil
}

func cardMutationResult(card Card) tools.ToolResult {
	return tools.ToolResult{Structured: map[string]any{"card": compact(card), "version": card.Version}}
}
