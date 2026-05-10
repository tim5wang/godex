package teamtools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/tim5wang/godex/internal/core/teammate"
	"github.com/tim5wang/godex/internal/domain/message"
	"github.com/tim5wang/godex/internal/tools"
)

type emptyArgs struct{}

type sendMessageArgs struct {
	To      string `json:"to"`
	Content string `json:"content"`
	MsgType string `json:"msg_type,omitempty"`
}

type contentArgs struct {
	Content string `json:"content"`
}

type teammateArgs struct {
	Teammate string `json:"teammate"`
}

type planApprovalArgs struct {
	RequestID string `json:"request_id"`
	Teammate  string `json:"teammate"`
	Approve   bool   `json:"approve"`
	Feedback  string `json:"feedback,omitempty"`
}

// NewReadInboxTool creates a new read inbox tool.
func NewReadInboxTool(bus *message.Bus, leadName string) tools.Tool {
	if leadName == "" {
		leadName = "lead"
	}
	return tools.NewTypedTool(tools.NewToolSpec("read_inbox", "Read and drain the lead's inbox", map[string]interface{}{
		"type":       "object",
		"properties": map[string]interface{}{},
	}, nil), func(ctx context.Context, args emptyArgs) (tools.ToolResult, error) {
		_ = ctx
		messages := bus.ReadInbox(leadName)
		if len(messages) == 0 {
			return tools.ToolResult{Text: "[]"}, nil
		}
		return tools.ToolResult{Structured: messages}, nil
	})
}

// NewSendMessageTool creates a new send message tool.
func NewSendMessageTool(bus *message.Bus, leadName string) tools.Tool {
	if leadName == "" {
		leadName = "lead"
	}
	return tools.NewTypedTool(tools.NewToolSpec("send_message", "Send a message to a teammate", map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"to":       map[string]string{"type": "string"},
			"content":  map[string]string{"type": "string"},
			"msg_type": map[string]string{"type": "string"},
		},
		"required": []string{"to", "content"},
	}, nil), func(ctx context.Context, args sendMessageArgs) (tools.ToolResult, error) {
		_ = ctx
		if args.To == "" {
			return tools.ToolResult{}, fmt.Errorf("missing to argument")
		}
		if args.Content == "" {
			return tools.ToolResult{}, fmt.Errorf("missing content argument")
		}
		msgType := message.MsgTypeMessage
		if args.MsgType != "" {
			msgType = message.MsgType(args.MsgType)
		}
		if err := bus.Send(message.Message{
			Type:    msgType,
			From:    leadName,
			To:      args.To,
			Content: args.Content,
		}); err != nil {
			return tools.ToolResult{}, err
		}
		return tools.ToolResult{Text: "OK"}, nil
	})
}

// NewBroadcastTool creates a new broadcast tool.
func NewBroadcastTool(bus *message.Bus, mgr *teammate.Manager, leadName string) tools.Tool {
	if leadName == "" {
		leadName = "lead"
	}
	return tools.NewTypedTool(tools.NewToolSpec("broadcast", "Send a message to all teammates", map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"content": map[string]string{"type": "string"},
		},
		"required": []string{"content"},
	}, nil), func(ctx context.Context, args contentArgs) (tools.ToolResult, error) {
		_ = ctx
		if args.Content == "" {
			return tools.ToolResult{}, fmt.Errorf("missing content argument")
		}
		teammateNames := teammateNames(mgr.List())
		if len(teammateNames) == 0 {
			return tools.ToolResult{Text: "No teammates available"}, nil
		}
		if err := bus.Broadcast(leadName, args.Content, teammateNames); err != nil {
			return tools.ToolResult{}, err
		}
		return tools.ToolResult{Text: fmt.Sprintf("Broadcast sent to %d teammate(s)", len(teammateNames))}, nil
	})
}

// NewShutdownRequestTool creates a new shutdown request tool.
func NewShutdownRequestTool(mgr *teammate.Manager) tools.Tool {
	return tools.NewTypedTool(tools.NewToolSpec("shutdown_request", "Request a teammate to shut down", map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"teammate": map[string]string{"type": "string"},
		},
		"required": []string{"teammate"},
	}, nil), func(ctx context.Context, args teammateArgs) (tools.ToolResult, error) {
		_ = ctx
		if args.Teammate == "" {
			return tools.ToolResult{}, fmt.Errorf("missing teammate argument")
		}
		if err := mgr.ShutdownTeammate(args.Teammate); err != nil {
			return tools.ToolResult{}, err
		}
		return tools.ToolResult{Text: "OK"}, nil
	})
}

// NewListTool creates a new list teammates tool.
func NewListTool(mgr *teammate.Manager) tools.Tool {
	return tools.NewTypedTool(tools.NewToolSpec("list_teammates", "List all teammates", map[string]interface{}{
		"type":       "object",
		"properties": map[string]interface{}{},
	}, nil), func(ctx context.Context, args emptyArgs) (tools.ToolResult, error) {
		_ = ctx
		teammates := mgr.List()
		result := make([]map[string]interface{}, 0, len(teammates))
		for _, tm := range teammates {
			result = append(result, map[string]interface{}{
				"name":   tm.Name,
				"role":   tm.Role,
				"status": tm.Status,
			})
		}
		return tools.ToolResult{Structured: result}, nil
	})
}

// NewPlanApprovalTool creates a new plan approval tool.
func NewPlanApprovalTool(bus *message.Bus, mgr *teammate.Manager, leadName string) tools.Tool {
	if leadName == "" {
		leadName = "lead"
	}
	return tools.NewTypedTool(tools.NewToolSpec("plan_approval", "Approve or reject a teammate's plan", map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"request_id": map[string]string{"type": "string"},
			"teammate":   map[string]string{"type": "string"},
			"approve":    map[string]string{"type": "boolean"},
			"feedback":   map[string]string{"type": "string"},
		},
		"required": []string{"request_id", "teammate", "approve"},
	}, nil), func(ctx context.Context, args planApprovalArgs) (tools.ToolResult, error) {
		_ = ctx
		if args.RequestID == "" {
			return tools.ToolResult{}, fmt.Errorf("missing request_id argument")
		}
		if args.Teammate == "" {
			return tools.ToolResult{}, fmt.Errorf("missing teammate argument")
		}
		content := map[string]interface{}{
			"request_id": args.RequestID,
			"approve":    args.Approve,
			"feedback":   args.Feedback,
		}
		contentData, _ := json.Marshal(content)
		if _, err := mgr.Get(args.Teammate); err != nil {
			return tools.ToolResult{}, err
		}
		msg := message.Message{
			Type:    message.MsgTypePlanApprovalResponse,
			From:    leadName,
			To:      args.Teammate,
			Content: string(contentData),
		}
		if err := bus.Send(msg); err != nil {
			return tools.ToolResult{}, err
		}
		if args.Approve {
			return tools.ToolResult{Text: "Plan approved"}, nil
		}
		return tools.ToolResult{Text: "Plan rejected"}, nil
	})
}

func teammateNames(teammates []*teammate.Teammate) []string {
	names := make([]string, 0, len(teammates))
	for _, tm := range teammates {
		names = append(names, tm.Name)
	}
	return names
}
