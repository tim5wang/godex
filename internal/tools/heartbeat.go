package tools

import (
	"context"
	"fmt"
	"strings"

	"github.com/tim5wang/godex/internal/domain/automation"
)

const defaultHeartbeatLogLimit = 20

// HeartbeatManager exposes runtime heartbeat resources to the always-active heartbeat tool.
type HeartbeatManager interface {
	GetRule() (automation.HeartbeatRule, error)
	SetRule(input automation.HeartbeatSetInput) (automation.HeartbeatRule, error)
	Toggle(enabled bool) (automation.HeartbeatRule, error)
	TestNow(ctx context.Context) (automation.HeartbeatRunLog, error)
	ListRunLogs(limit int) ([]automation.HeartbeatRunLog, error)
}

type heartbeatArgs struct {
	Action           string                     `json:"action"`
	Enabled          *bool                      `json:"enabled,omitempty"`
	IntervalSeconds  *int                       `json:"interval_seconds,omitempty"`
	Timezone         *string                    `json:"timezone,omitempty"`
	ActiveHoursStart *string                    `json:"active_hours_start,omitempty"`
	ActiveHoursEnd   *string                    `json:"active_hours_end,omitempty"`
	SessionMode      *string                    `json:"session_mode,omitempty"`
	DeliveryTarget   *automation.DeliveryTarget `json:"delivery_target,omitempty"`
	PromptOverride   *string                    `json:"prompt_override,omitempty"`
	WatchdogScript   *string                    `json:"watchdog_script,omitempty"`
	Limit            int                        `json:"limit,omitempty"`
}

// NewHeartbeatTool creates a new heartbeat tool.
func NewHeartbeatTool(manager HeartbeatManager) Tool {
	return NewTypedTool(NewToolSpec("heartbeat", "Inspect, configure, toggle, test, and review logs for the proactive heartbeat loop.", map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"action": map[string]interface{}{
				"type":        "string",
				"description": "Heartbeat action to perform",
				"enum":        []string{"get", "set", "toggle", "test", "logs"},
			},
			"enabled": map[string]interface{}{
				"type":        "boolean",
				"description": "Enable or disable heartbeat",
			},
			"interval_seconds": map[string]interface{}{
				"type":        "integer",
				"description": "Heartbeat interval in seconds",
			},
			"timezone": map[string]interface{}{
				"type":        "string",
				"description": "IANA timezone such as Asia/Shanghai",
			},
			"active_hours_start": map[string]interface{}{
				"type":        "string",
				"description": "Daily start time in HH:MM",
			},
			"active_hours_end": map[string]interface{}{
				"type":        "string",
				"description": "Daily end time in HH:MM",
			},
			"session_mode": map[string]interface{}{
				"type":        "string",
				"description": "Whether runs reuse one heartbeat session or get isolated sessions",
				"enum":        []string{"shared", "isolated"},
			},
			"delivery_target": deliveryTargetSchema(),
			"prompt_override": map[string]interface{}{
				"type":        "string",
				"description": "Optional full prompt override for the heartbeat run",
			},
			"watchdog_script": map[string]interface{}{
				"type":        "string",
				"description": "Optional pre-run shell script: exit 0 runs the agent, non-zero skips this tick",
			},
			"limit": map[string]interface{}{
				"type":        "integer",
				"description": "Optional log limit for logs action",
			},
		},
		"required": []string{"action"},
	}, nil), func(ctx context.Context, args heartbeatArgs) (ToolResult, error) {
		if manager == nil {
			return ToolResult{}, fmt.Errorf("heartbeat service is unavailable")
		}
		action := strings.ToLower(strings.TrimSpace(args.Action))
		runtimeCtx := SessionContextFromContext(ctx)

		switch action {
		case "get":
			rule, err := manager.GetRule()
			if err != nil {
				return ToolResult{}, err
			}
			return ToolResult{Structured: map[string]interface{}{"action": "get", "rule": rule}}, nil
		case "logs":
			limit := args.Limit
			if limit <= 0 {
				limit = defaultHeartbeatLogLimit
			}
			logs, err := manager.ListRunLogs(limit)
			if err != nil {
				return ToolResult{}, err
			}
			return ToolResult{Structured: map[string]interface{}{"action": "logs", "limit": limit, "runs": logs}}, nil
		case "test":
			run, err := manager.TestNow(ctx)
			if err != nil {
				return ToolResult{}, err
			}
			rule, err := manager.GetRule()
			if err != nil {
				return ToolResult{}, err
			}
			return ToolResult{Structured: map[string]interface{}{"action": "test", "rule": rule, "run": run}}, nil
		case "toggle":
			if args.Enabled == nil {
				return ToolResult{}, fmt.Errorf("toggle requires enabled")
			}
			rule, err := manager.Toggle(*args.Enabled)
			if err != nil {
				return ToolResult{}, err
			}
			return ToolResult{Structured: map[string]interface{}{"action": "toggle", "rule": rule}}, nil
		case "set":
			input := automation.HeartbeatSetInput{
				CreatedFromSession: runtimeCtx.SessionID,
			}
			createdBy := strings.TrimSpace(runtimeCtx.Sender)
			if createdBy == "" {
				createdBy = strings.TrimSpace(runtimeCtx.Source)
			}
			if createdBy == "" {
				createdBy = "agent"
			}
			input.CreatedBy = createdBy
			input.Enabled = args.Enabled
			input.IntervalSeconds = args.IntervalSeconds
			input.Timezone = args.Timezone
			input.ActiveHoursStart = args.ActiveHoursStart
			input.ActiveHoursEnd = args.ActiveHoursEnd
			input.SessionMode = args.SessionMode
			input.PromptOverride = args.PromptOverride
			input.WatchdogScript = args.WatchdogScript
			if args.DeliveryTarget != nil {
				target := args.DeliveryTarget.Clone()
				input.DeliveryTarget = &target
			} else if !runtimeCtx.DefaultDelivery.IsZero() {
				currentRule, err := manager.GetRule()
				if err == nil && currentRule.DeliveryTarget.IsZero() {
					target := runtimeCtx.DefaultDelivery.Clone()
					input.DeliveryTarget = &target
				}
			}
			rule, err := manager.SetRule(input)
			if err != nil {
				return ToolResult{}, err
			}
			return ToolResult{Structured: map[string]interface{}{"action": "set", "rule": rule}}, nil
		default:
			return ToolResult{}, fmt.Errorf("unsupported heartbeat action %q", action)
		}
	})
}
