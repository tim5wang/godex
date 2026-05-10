package tools

import (
	"context"
	"fmt"
	"strings"

	"github.com/tim5wang/godex/internal/platform/tooling"
)

type bashArgs struct {
	Command               string `json:"command"`
	AllowUnlistedCommands bool   `json:"_allow_unlisted_commands,omitempty"`
}

// NewBashTool creates a new bash tool.
func NewBashTool(workspace string, tempDir ...string) Tool {
	outputDir := ""
	if len(tempDir) > 0 {
		outputDir = tempDir[0]
	}
	return NewBashToolWithExecution(workspace, outputDir, tooling.ExecutionConfig{})
}

func NewBashToolWithExecution(workspace, tempDir string, execution tooling.ExecutionConfig) Tool {
	executor := tooling.NewWorkspaceExecutorWithTempDirAndExecution(workspace, tempDir, execution)
	return NewTypedTool(SpecFromDefinition(tooling.BashDefinition(), nil), func(ctx context.Context, args bashArgs) (ToolResult, error) {
		if strings.TrimSpace(args.Command) == "" {
			return ToolResult{}, fmt.Errorf("missing command argument")
		}
		options := shellCommandOptionsForContext(SessionContextFromContext(ctx), tooling.ShellCommandOptions{
			AllowUnlistedCommands: args.AllowUnlistedCommands,
		})
		output, err := executor.RunShellBudgetedWithOptions(ctx, args.Command, options)
		result := ToolResult{
			Text: output.ModelText(),
			Metadata: map[string]interface{}{
				"output_bytes":      output.Bytes,
				"output_truncated":  output.Truncated,
				"exit_code":         output.ExitCode,
				"execution_backend": executor.ExecutionBackend(),
			},
		}
		if output.FilePath != "" {
			result.ArtifactPaths = []string{output.FilePath}
			result.Metadata["output_path"] = output.FilePath
		}
		return result, err
	})
}
