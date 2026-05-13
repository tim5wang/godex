package agent

import (
	"context"

	"github.com/tim5wang/godex/internal/core/conversation"
	"github.com/tim5wang/godex/internal/domain/automation"
	"github.com/tim5wang/godex/internal/tools"
)

func (a *Agent) handleTool(ctx context.Context, name string, input map[string]interface{}) (string, error) {
	result, err := a.handleToolResult(ctx, name, input)
	if err != nil {
		return "", err
	}
	return result.Output, nil
}

func (a *Agent) handleToolResult(ctx context.Context, name string, input map[string]interface{}) (conversation.ToolExecutionResult, error) {
	result, err := a.toolHandler.HandleResult(ctx, name, input)
	if err != nil {
		execution := conversation.ToolExecutionResult{
			ArtifactPaths: append([]string{}, result.ArtifactPaths...),
		}
		if toolResultHasModelOutput(result) {
			output, outputErr := result.OutputString()
			if outputErr != nil {
				return conversation.ToolExecutionResult{}, outputErr
			}
			execution.Output = output
		}
		return execution, err
	}
	if name == "history_search" {
		if state := historyRecallTurnStateFromContext(ctx); state != nil {
			state.consumeAutomaticExposure()
		}
	}
	output, err := result.OutputString()
	if err != nil {
		return conversation.ToolExecutionResult{}, err
	}
	return conversation.ToolExecutionResult{
		Output:        output,
		ArtifactPaths: append([]string{}, result.ArtifactPaths...),
	}, nil
}

// RunPackageSmokeCommand executes one explicitly requested package smoke command
// through the normal tool handler and permission chain.
func (a *Agent) RunPackageSmokeCommand(ctx context.Context, runtimeCtx automation.SessionContext, command string) (tools.ToolResult, error) {
	ctx = tools.WithSessionContext(ctx, runtimeCtx)
	return a.toolHandler.HandleResult(ctx, "bash", map[string]interface{}{"command": command})
}

func toolResultHasModelOutput(result tools.ToolResult) bool {
	return result.Text != "" || result.Structured != nil
}
