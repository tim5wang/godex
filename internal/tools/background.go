package tools

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/tim5wang/godex/internal/core/background"
	"github.com/tim5wang/godex/internal/platform/tooling"
)

type backgroundRunArgs struct {
	Command               string `json:"command"`
	Timeout               int    `json:"timeout,omitempty"`
	AllowUnlistedCommands bool   `json:"_allow_unlisted_commands,omitempty"`
}

type checkBackgroundArgs struct {
	TaskID     string `json:"task_id"`
	TailLines  int    `json:"tail_lines,omitempty"`
	Offset     int64  `json:"offset,omitempty"`
	LimitBytes int64  `json:"limit_bytes,omitempty"`
	Query      string `json:"query,omitempty"`
}

// NewBackgroundRunTool creates a new background run tool.
func NewBackgroundRunTool(mgr *background.Manager, workspace string, tempDir ...string) Tool {
	outputDir := ""
	if len(tempDir) > 0 {
		outputDir = tempDir[0]
	}
	return NewBackgroundRunToolWithExecution(mgr, workspace, outputDir, tooling.ExecutionConfig{})
}

func NewBackgroundRunToolWithExecution(mgr *background.Manager, workspace, tempDir string, execution tooling.ExecutionConfig) Tool {
	executor := tooling.NewWorkspaceExecutorWithTempDirAndExecution(workspace, tempDir, execution)
	return NewTypedTool(SpecFromDefinition(tooling.BackgroundRunDefinition(), nil), func(ctx context.Context, args backgroundRunArgs) (ToolResult, error) {
		if args.Command == "" {
			return ToolResult{}, fmt.Errorf("missing command argument")
		}

		taskID := fmt.Sprintf("bg_%d", time.Now().UnixNano())
		runtimeCtx := SessionContextFromContext(ctx)
		options := shellCommandOptionsForContext(runtimeCtx, tooling.ShellCommandOptions{
			AllowUnlistedCommands: args.AllowUnlistedCommands,
		})
		cmd, argv, err := executor.BuildArgvCommandWithOptions(args.Command, options)
		if err != nil {
			return ToolResult{}, err
		}

		var timeout time.Duration
		if args.Timeout > 0 {
			timeout = time.Duration(args.Timeout) * time.Second
		}
		task, err := mgr.StartWithOptions(taskID, cmd, timeout, background.OutputOptions{
			SpillDir:  filepath.Join(executor.TempDir, "background"),
			SessionID: runtimeCtx.SessionID,
			TurnID:    runtimeCtx.Metadata["turn_id"],
			Command:   args.Command,
			Argv:      argv,
		})
		if err != nil {
			return ToolResult{}, err
		}

		return ToolResult{Structured: map[string]interface{}{
			"task_id":           taskID,
			"argv":              argv,
			"status":            task.Status,
			"running":           mgr.IsRunning(taskID),
			"start_time":        task.StartTime.Format(time.RFC3339),
			"output_log_path":   task.OutputLogPath,
			"execution_backend": executor.ExecutionBackend(),
		}}, nil
	})
}

// NewCheckBackgroundTool creates a new check background tool.
func NewCheckBackgroundTool(mgr *background.Manager) Tool {
	spec := NewToolSpec("check_background", "Check the status of a background task", map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"task_id":     map[string]string{"type": "string"},
			"tail_lines":  map[string]interface{}{"type": "integer", "description": "Return the last N output lines."},
			"offset":      map[string]interface{}{"type": "integer", "description": "Byte offset for paged output reads."},
			"limit_bytes": map[string]interface{}{"type": "integer", "description": "Maximum bytes to return; capped by runtime."},
			"query":       map[string]interface{}{"type": "string", "description": "Search output log lines containing this text."},
		},
		"required": []string{"task_id"},
	}, nil)
	return NewTypedTool(spec, func(ctx context.Context, args checkBackgroundArgs) (ToolResult, error) {
		_ = ctx
		if args.TaskID == "" {
			return ToolResult{}, fmt.Errorf("missing task_id argument")
		}

		if mgr.IsRunning(args.TaskID) {
			result := map[string]interface{}{
				"task_id": args.TaskID,
				"status":  background.StatusRunning,
			}
			if args.TailLines > 0 || args.Offset > 0 || args.LimitBytes > 0 || args.Query != "" {
				read, err := mgr.ReadOutput(args.TaskID, background.OutputReadOptions{
					Offset:     args.Offset,
					LimitBytes: args.LimitBytes,
					TailLines:  args.TailLines,
					Query:      args.Query,
				})
				if err == nil {
					result["output"] = read.Output
					result["output_path"] = read.OutputPath
					result["output_bytes"] = read.TotalBytes
					result["offset"] = read.Offset
					result["truncated"] = read.Truncated
				}
			}
			return ToolResult{Structured: result}, nil
		}

		task, err := mgr.Get(args.TaskID)
		if err != nil {
			return ToolResult{}, err
		}
		select {
		case <-task.Done:
		default:
		}
		output := task.Output
		outputPath := task.OutputPath
		outputBytes := task.OutputBytes
		if args.TailLines > 0 || args.Offset > 0 || args.LimitBytes > 0 || args.Query != "" {
			read, err := mgr.ReadOutput(args.TaskID, background.OutputReadOptions{
				Offset:     args.Offset,
				LimitBytes: args.LimitBytes,
				TailLines:  args.TailLines,
				Query:      args.Query,
			})
			if err != nil {
				return ToolResult{}, err
			}
			output = read.Output
			outputPath = read.OutputPath
			outputBytes = read.TotalBytes
		}
		return ToolResult{Structured: map[string]interface{}{
			"task_id":          args.TaskID,
			"status":           task.Status,
			"output":           output,
			"output_path":      outputPath,
			"output_truncated": task.OutputTruncated,
			"output_bytes":     outputBytes,
			"output_log_path":  task.OutputLogPath,
			"exit_code":        task.ExitCode,
			"error":            backgroundError(task.Error),
			"rerun_hint":       backgroundRerunHint(task),
		}}, nil
	})
}

func backgroundError(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func backgroundRerunHint(task *background.Task) string {
	if task == nil || task.Status != background.StatusInterrupted {
		return ""
	}
	if task.Command != "" {
		return "Previous process was interrupted before completion; rerun with background_run command: " + task.Command
	}
	if len(task.Argv) > 0 {
		return "Previous process was interrupted before completion; rerun with background_run argv: " + fmt.Sprint(task.Argv)
	}
	return "Previous process was interrupted before completion; start the background task again if it is still needed."
}
