package backend

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	pkgregistry "github.com/tim5wang/godex/internal/core/packages"
	"github.com/tim5wang/godex/internal/domain/events"
	"github.com/tim5wang/godex/internal/domain/security"
	"github.com/tim5wang/godex/internal/tools"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func (s *Service) SecuritySummary(ctx context.Context) (security.CIKSummary, error) {
	_ = ctx
	cfg := s.cfg
	if cfg == nil {
		return security.CIKSummary{}, fmt.Errorf("missing config")
	}
	recent, _ := s.SecurityAudit(context.Background(), 10)
	policy := security.SecurityPolicy{
		InteractiveApprovalEnabled: cfg.Tools.Permissions.InteractiveApprovalEnabled,
		InteractiveApprovalMode:    cfg.Tools.Permissions.InteractiveApprovalMode,
		ApprovalSources:            append([]string{}, cfg.Tools.Permissions.InteractiveApprovalSources...),
		ApprovalTools:              append([]string{}, cfg.Tools.Permissions.InteractiveApprovalTools...),
		PendingTTLSeconds:          cfg.Tools.Permissions.PendingTTLSeconds,
		TrustedPathPrefixes:        append([]string{}, cfg.Tools.Permissions.TrustedPathPrefixes...),
		TrustedCommandPrefixes:     append([]string{}, cfg.Tools.Permissions.TrustedCommandPrefixes...),
		BlockAutomationMutations:   cfg.Tools.Permissions.BlockAutomationMutations,
		MemoryIdentityReview:       true,
		PackageInstallReview:       true,
		SubagentWorkspaceIsolation: true,
	}
	capabilityItems := []string{}
	if cfg.Tools.WebSearch.Enabled {
		capabilityItems = append(capabilityItems, "web_search")
	}
	if cfg.Tools.WebFetch.Enabled {
		capabilityItems = append(capabilityItems, "web_fetch")
	}
	if cfg.Tools.Browser.Enabled {
		capabilityItems = append(capabilityItems, "browser")
	}
	if cfg.Cron.Enabled {
		capabilityItems = append(capabilityItems, "cron")
	}
	if cfg.Heartbeat.Enabled {
		capabilityItems = append(capabilityItems, "heartbeat")
	}
	identityItems := []string{"team.lead_name=" + cfg.LeadName}
	if cfg.Feishu.Enabled {
		identityItems = append(identityItems, "feishu channel")
	}
	if cfg.Weixin.Enabled {
		identityItems = append(identityItems, "weixin channel")
	}
	knowledgeItems := []string{"memory", "skills", "history_search"}
	if cfg.Tools.History.Enabled {
		knowledgeItems = append(knowledgeItems, "session archives")
	}
	return security.CIKSummary{
		GeneratedAt: s.now(),
		Policy:      policy,
		Capability:  buildRiskSummary("capability", capabilityItems, cfg.Tools.Permissions.InteractiveApprovalEnabled),
		Identity:    buildRiskSummary("identity", identityItems, cfg.Tools.Permissions.InteractiveApprovalEnabled),
		Knowledge:   buildRiskSummary("knowledge", knowledgeItems, cfg.Tools.History.Enabled),
		Recent:      recent,
	}, nil
}

// SecurityAudit returns recent security audit events.
func (s *Service) SecurityAudit(ctx context.Context, limit int) ([]security.SecurityEvent, error) {
	_ = ctx
	if limit <= 0 {
		limit = 50
	}
	path := filepath.Join(s.cfg.StateDir, securityAuditFileName)
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer file.Close()
	var eventsOut []security.SecurityEvent
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64<<10), 2<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var event security.SecurityEvent
		if err := json.Unmarshal([]byte(line), &event); err == nil {
			eventsOut = append(eventsOut, event)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if len(eventsOut) > limit {
		eventsOut = eventsOut[len(eventsOut)-limit:]
	}
	for i, j := 0, len(eventsOut)-1; i < j; i, j = i+1, j-1 {
		eventsOut[i], eventsOut[j] = eventsOut[j], eventsOut[i]
	}
	return eventsOut, nil
}

// ListPackages returns installed declaration-only Godex packages.
func (s *Service) appendSecurityEvent(event security.SecurityEvent) {
	if s == nil || s.cfg == nil {
		return
	}
	if strings.TrimSpace(event.ID) == "" {
		event.ID = randomSuffix(8)
	}
	if event.At.IsZero() {
		event.At = s.now()
	}
	data, err := json.Marshal(event)
	if err != nil {
		return
	}
	data = append(data, '\n')
	path := filepath.Join(s.cfg.StateDir, securityAuditFileName)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return
	}
	_, writeErr := file.Write(data)
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil {
		return
	}
}

func (s *Service) appendPermissionAuditEvent(action, severity, sessionID string, resolution tools.PermissionResolution) {
	if s == nil {
		return
	}
	req := resolution.Request
	metadata := map[string]string{
		"request_id": strings.TrimSpace(resolution.RequestID),
		"scope":      strings.TrimSpace(string(resolution.Scope)),
		"decision":   strings.TrimSpace(string(resolution.Decision)),
		"tool":       strings.TrimSpace(req.ToolName),
		"action":     strings.TrimSpace(req.Action),
		"source":     strings.TrimSpace(req.Source),
		"risk":       tools.PermissionRiskSummary(req),
	}
	if command := strings.TrimSpace(req.Command); command != "" {
		metadata["command"] = command
	}
	if len(req.Paths) > 0 {
		metadata["paths"] = strings.Join(req.Paths, ",")
	}
	summary := strings.TrimSpace(tools.PermissionIntentSummary(tools.PendingPermission{Request: req}))
	if summary == "" {
		summary = action + " " + strings.TrimSpace(resolution.RequestID)
	}
	s.appendSecurityEvent(security.SecurityEvent{
		At:        resolution.ResolvedAt,
		Category:  "capability",
		Action:    action,
		Severity:  severity,
		SessionID: strings.TrimSpace(sessionID),
		Source:    strings.TrimSpace(req.Source),
		Summary:   summary,
		Metadata:  metadata,
	})
}

// AppendSecurityEvent records an audit-relevant event from runtime adapters.
func (s *Service) AppendSecurityEvent(event security.SecurityEvent) {
	s.appendSecurityEvent(event)
}

func (s *Service) packageToolHealth() (pkgregistry.ToolHealthSummary, error) {
	sessions, err := s.ListSessions(context.Background(), SessionListFilter{})
	if err != nil {
		return pkgregistry.ToolHealthSummary{}, err
	}
	if len(sessions) > 30 {
		sessions = sessions[:30]
	}
	byTool := map[string]*pkgregistry.ToolStat{}
	summary := pkgregistry.ToolHealthSummary{InspectedSessions: len(sessions)}
	for _, session := range sessions {
		for _, event := range s.readSessionTimeline(session.SessionID) {
			if event.Type != events.EventToolCallFinished {
				continue
			}
			name, errText := toolPayloadNameAndError(event.Payload)
			if name == "" {
				continue
			}
			row := byTool[name]
			if row == nil {
				row = &pkgregistry.ToolStat{Name: name}
				byTool[name] = row
			}
			row.Total++
			summary.TotalRuns++
			if errText != "" {
				row.Failure++
				row.LastFailure = normalizeFailureReason(errText)
				summary.FailureRuns++
			} else {
				row.Success++
				summary.SuccessRuns++
			}
		}
	}
	rows := make([]pkgregistry.ToolStat, 0, len(byTool))
	for _, row := range byTool {
		row.SuccessRate = percentFloat(row.Success, row.Total)
		rows = append(rows, *row)
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Total == rows[j].Total {
			return rows[i].Name < rows[j].Name
		}
		return rows[i].Total > rows[j].Total
	})
	summary.ByTool = rows
	summary.SuccessRate = percentFloat(summary.SuccessRuns, summary.TotalRuns)
	return summary, nil
}

func toolPayloadNameAndError(payload any) (string, string) {
	switch value := payload.(type) {
	case events.ToolCallPayload:
		return strings.TrimSpace(value.Name), strings.TrimSpace(value.Error)
	case map[string]any:
		return stringFromAny(value["name"]), stringFromAny(value["error"])
	default:
		data, _ := json.Marshal(payload)
		var decoded map[string]any
		if err := json.Unmarshal(data, &decoded); err != nil {
			return "", ""
		}
		return stringFromAny(decoded["name"]), stringFromAny(decoded["error"])
	}
}

func stringFromAny(value any) string {
	if text, ok := value.(string); ok {
		return strings.TrimSpace(text)
	}
	return ""
}

func normalizeFailureReason(value string) string {
	for _, line := range strings.Split(value, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if len(line) > 120 {
			return line[:117] + "..."
		}
		return line
	}
	return "Unknown failure"
}

func percentFloat(value, total int) float64 {
	if total <= 0 {
		return 0
	}
	return float64(value*1000/total) / 10
}

func knownToolBundles() []string {
	return []string{"core_code", "planning", "background", "task_board", "team", "subagent", "mcp", "web", "browser", "desktop", "packages"}
}

func buildRiskSummary(axis string, items []string, guarded bool) security.RiskSummary {
	score := len(items)
	level := "low"
	if score >= 6 {
		level = "high"
	} else if score >= 3 {
		level = "medium"
	}
	advice := []string{}
	if !guarded {
		advice = append(advice, "enable review gates for untrusted sources")
	}
	return security.RiskSummary{
		Axis:   axis,
		Level:  level,
		Score:  score,
		Items:  append([]string{}, items...),
		Advice: advice,
	}
}

// ContextSummary returns a non-mutating prompt-budget summary for one session.
