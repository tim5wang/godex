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
	execute, err := a.prepareToolResult(ctx, name, input)
	if execute == nil {
		return conversation.ToolExecutionResult{}, err
	}
	return execute(ctx)
}

func (a *Agent) prepareToolResult(ctx context.Context, name string, input map[string]interface{}) (conversation.PreparedTool, error) {
	return a.prepareToolExecution(ctx, name, input, false)
}

func (a *Agent) prepareParallelToolResult(ctx context.Context, name string, input map[string]interface{}) (conversation.PreparedTool, error) {
	return a.prepareToolExecution(ctx, name, input, true)
}

func (a *Agent) prepareToolExecution(ctx context.Context, name string, input map[string]interface{}, parallel bool) (conversation.PreparedTool, error) {
	if tools.SandboxIDFromContext(ctx) == "" {
		ctx = tools.WithSandboxID(ctx, a.SandboxID())
	}
	prepare := a.toolHandler.PrepareResult
	if parallel {
		prepare = a.toolHandler.PrepareReadOnlyResult
	}
	execute, prepareErr := prepare(ctx, name, input)
	if execute == nil {
		return nil, prepareErr
	}
	return func(runCtx context.Context) (conversation.ToolExecutionResult, error) {
		if tools.SandboxIDFromContext(runCtx) == "" {
			runCtx = tools.WithSandboxID(runCtx, a.SandboxID())
		}
		result, err := execute(runCtx)
		if err != nil {
			execution := conversation.ToolExecutionResult{
				ExitCode:      toolExitCode(result),
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
		if name == "memory" {
			switch input["action"] {
			case "remember", "update", "forget", "accept":
				a.mu.Lock()
				a.memoryRecallInjectCount = 0
				a.mu.Unlock()
			}
		}
		output, err := result.OutputString()
		if err != nil {
			return conversation.ToolExecutionResult{}, err
		}
		return conversation.ToolExecutionResult{
			Output:        output,
			ExitCode:      toolExitCode(result),
			ArtifactPaths: append([]string{}, result.ArtifactPaths...),
		}, nil
	}, prepareErr
}

// RunPackageSmokeCommand executes one explicitly requested package smoke command
// through the normal tool handler and permission chain.
func (a *Agent) RunPackageSmokeCommand(ctx context.Context, runtimeCtx automation.SessionContext, command string) (tools.ToolResult, error) {
	ctx = tools.WithSessionContext(ctx, runtimeCtx)
	if tools.SandboxIDFromContext(ctx) == "" {
		ctx = tools.WithSandboxID(ctx, a.SandboxID())
	}
	return a.toolHandler.HandleResult(ctx, "bash", map[string]interface{}{"command": command})
}

func toolResultHasModelOutput(result tools.ToolResult) bool {
	return result.Text != "" || result.Structured != nil
}

func toolExitCode(result tools.ToolResult) *int {
	if code, ok := result.Metadata["exit_code"].(int); ok {
		return &code
	}
	return nil
}
