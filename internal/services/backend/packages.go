package backend

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/tim5wang/godex/internal/agent"
	pkgregistry "github.com/tim5wang/godex/internal/core/packages"
	"github.com/tim5wang/godex/internal/domain/automation"
	"github.com/tim5wang/godex/internal/domain/events"
	"github.com/tim5wang/godex/internal/domain/message"
	"github.com/tim5wang/godex/internal/domain/security"
	"github.com/tim5wang/godex/internal/tools"
)

func (s *Service) ListPackages(ctx context.Context) ([]pkgregistry.Entry, error) {
	_ = ctx
	return pkgregistry.NewManager(s.cfg.StateDir, s.cfg.SkillsDir).List()
}

// InstallPackage installs one Godex package and activates its optional runtime
// in every currently open session. Future sessions activate it at startup.
func (s *Service) InstallPackage(ctx context.Context, source string) (pkgregistry.Entry, error) {
	entry, err := pkgregistry.NewManager(s.cfg.StateDir, s.cfg.SkillsDir).Install(source)
	if err != nil {
		return pkgregistry.Entry{}, err
	}
	if err := s.reconcilePackageRuntimes(ctx); err != nil {
		return pkgregistry.Entry{}, fmt.Errorf("package %s installed but runtime activation failed: %w", entry.Name, err)
	}
	s.appendSecurityEvent(security.SecurityEvent{
		At:       s.now(),
		Category: "capability",
		Action:   "install_package",
		Severity: "warning",
		Summary:  "Installed package " + entry.Name,
		Metadata: map[string]string{
			"package": entry.Name,
			"source":  entry.Source,
			"digest":  entry.Digest,
			"trust":   entry.Trust,
		},
	})
	return entry, nil
}

// ReinstallPackage reinstalls one package from its recorded source without
// removing the currently installed copy unless the reinstall succeeds.
func (s *Service) ReinstallPackage(ctx context.Context, name string) (pkgregistry.Entry, error) {
	entry, err := pkgregistry.NewManager(s.cfg.StateDir, s.cfg.SkillsDir).Reinstall(name)
	if err != nil {
		return pkgregistry.Entry{}, err
	}
	if err := s.reconcilePackageRuntimes(ctx); err != nil {
		return pkgregistry.Entry{}, fmt.Errorf("package %s reinstalled but runtime reload failed: %w", entry.Name, err)
	}
	s.appendSecurityEvent(security.SecurityEvent{
		At:       s.now(),
		Category: "capability",
		Action:   "reinstall_package",
		Severity: "warning",
		Summary:  "Reinstalled package " + entry.Name,
		Metadata: map[string]string{
			"package": entry.Name,
			"source":  entry.Source,
			"digest":  entry.Digest,
			"version": entry.Version,
		},
	})
	return entry, nil
}

// RemovePackage removes one installed Godex package.
func (s *Service) RemovePackage(ctx context.Context, name string) (pkgregistry.Entry, error) {
	entry, err := pkgregistry.NewManager(s.cfg.StateDir, s.cfg.SkillsDir).Remove(name)
	if err != nil {
		return pkgregistry.Entry{}, err
	}
	if err := s.reconcilePackageRuntimes(ctx); err != nil {
		return pkgregistry.Entry{}, fmt.Errorf("package %s removed but runtime deactivation failed: %w", entry.Name, err)
	}
	s.appendSecurityEvent(security.SecurityEvent{
		At:       s.now(),
		Category: "capability",
		Action:   "remove_package",
		Severity: "info",
		Summary:  "Removed package " + entry.Name,
		Metadata: map[string]string{
			"package": entry.Name,
			"digest":  entry.Digest,
		},
	})
	return entry, nil
}

func (s *Service) reconcilePackageRuntimes(ctx context.Context) error {
	s.mu.Lock()
	agents := make([]*agent.Agent, 0, len(s.sessions))
	for _, session := range s.sessions {
		if session != nil && session.agent != nil {
			agents = append(agents, session.agent)
		}
	}
	s.mu.Unlock()
	for _, runtimeAgent := range agents {
		if err := runtimeAgent.ActivateInstalledPackageRuntimes(ctx); err != nil {
			return err
		}
	}
	return nil
}

// ListPrompts returns prompt templates installed by packages.
func (s *Service) ListPrompts(ctx context.Context, includeContent bool) ([]pkgregistry.Prompt, error) {
	_ = ctx
	return pkgregistry.NewManager(s.cfg.StateDir, s.cfg.SkillsDir).ListPrompts(includeContent)
}

// ListPackageCommands returns package-provided slash-command workflow declarations.
func (s *Service) ListPackageCommands(ctx context.Context, includeContent bool) ([]pkgregistry.Command, error) {
	_ = ctx
	return pkgregistry.NewManager(s.cfg.StateDir, s.cfg.SkillsDir).ListCommands(includeContent)
}

// ListPackageRoles returns package-provided named subagent role declarations.
func (s *Service) ListPackageRoles(ctx context.Context, includeContent bool) ([]pkgregistry.Role, error) {
	_ = ctx
	return pkgregistry.NewManager(s.cfg.StateDir, s.cfg.SkillsDir).ListRoles(includeContent)
}

// PackageQuality returns declaration health plus recent tool reliability.
func (s *Service) PackageQuality(ctx context.Context) (pkgregistry.QualityReport, error) {
	_ = ctx
	toolHealth, err := s.packageToolHealth()
	if err != nil {
		return pkgregistry.QualityReport{}, err
	}
	report, err := pkgregistry.NewManager(s.cfg.StateDir, s.cfg.SkillsDir).BuildQualityReport(s.now().Format(time.RFC3339), toolHealth, knownToolBundles())
	if err != nil {
		return pkgregistry.QualityReport{}, err
	}
	return report, nil
}

// RunPackageSmoke runs one explicitly selected package smoke declaration
// through a backend session and the normal shell permission path.
func (s *Service) RunPackageSmoke(ctx context.Context, packageName, smokeName, sessionID string) (pkgregistry.SmokeRun, error) {
	manager := pkgregistry.NewManager(s.cfg.StateDir, s.cfg.SkillsDir)
	entry, smoke, err := manager.GetSmokeTest(packageName, smokeName)
	if err != nil {
		return pkgregistry.SmokeRun{}, err
	}

	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		opened, err := s.OpenSession(ctx, SessionLocator{
			Channel: "web",
			Key:     "package-smoke",
			UserID:  "package-smoke",
			Metadata: map[string]string{
				"purpose": "package_smoke",
			},
		})
		if err != nil {
			return pkgregistry.SmokeRun{}, err
		}
		sessionID = opened.SessionID
	}
	session, err := s.requireSession(sessionID)
	if err != nil {
		return pkgregistry.SmokeRun{}, err
	}

	now := s.now()
	run := pkgregistry.SmokeRun{
		RunID:       pkgregistry.NewSmokeRunID(entry.Name, smoke.Name, now),
		PackageName: entry.Name,
		SmokeName:   smoke.Name,
		SessionID:   sessionID,
		Status:      "running",
		StartedAt:   now,
	}
	if recordErr := manager.RecordSmokeRun(run); recordErr != nil {
		return pkgregistry.SmokeRun{}, recordErr
	}

	complete := func(status string, result tools.ToolResult, runErr error) (pkgregistry.SmokeRun, error) {
		run.Status = status
		run.CompletedAt = s.now()
		run.ArtifactPaths = append([]string{}, result.ArtifactPaths...)
		if output, outputErr := result.OutputString(); outputErr == nil {
			run.Output = output
		}
		if runErr != nil {
			run.Error = runErr.Error()
		}
		_ = manager.RecordSmokeRun(run)
		s.appendSecurityEvent(security.SecurityEvent{
			At:        run.CompletedAt,
			Category:  "capability",
			Action:    "run_package_smoke",
			Severity:  smokeRunSeverity(run),
			SessionID: sessionID,
			Summary:   fmt.Sprintf("Package smoke %s/%s %s", run.PackageName, run.SmokeName, run.Status),
			Metadata: map[string]string{
				"package": run.PackageName,
				"smoke":   run.SmokeName,
				"run_id":  run.RunID,
				"status":  run.Status,
			},
		})
		return run, runErr
	}

	if issues := pkgregistry.SmokeQuickCheck(entry, smoke); len(issues) > 0 {
		run.Output = strings.Join(issues, "\n")
		run.Error = strings.Join(issues, "; ")
		return complete("invalid", tools.ToolResult{Text: run.Output}, nil)
	}

	release, err := session.acquire(ctx)
	if err != nil {
		run.Status = "failed"
		run.Error = err.Error()
		run.CompletedAt = s.now()
		_ = manager.RecordSmokeRun(run)
		return run, err
	}
	released := false
	defer func() {
		if !released {
			release()
		}
	}()

	execCtx := ctx
	cancel := func() {}
	if smoke.TimeoutSeconds > 0 {
		execCtx, cancel = context.WithTimeout(ctx, time.Duration(smoke.TimeoutSeconds)*time.Second)
	}
	defer cancel()

	turnID := session.nextTurnID(now)
	command := packageSmokeShellCommand(entry, smoke)
	runtimeCtx := automation.SessionContext{
		SessionID:      sessionID,
		LocatorChannel: session.locator.Channel,
		LocatorKey:     session.locator.Key,
		LocatorUserID:  session.locator.UserID,
		Source:         string(message.SourceCommand),
		Sender:         "package-smoke",
		Metadata: map[string]string{
			"package": entry.Name,
			"smoke":   smoke.Name,
			"run_id":  run.RunID,
		},
	}
	result, execErr := session.agent.RunPackageSmokeCommand(execCtx, runtimeCtx, command)
	status := packageSmokeStatus(result, execErr, smoke.ExpectedExitCode)
	var pending tools.ErrPermissionPending
	if execErr != nil && errors.As(execErr, &pending) {
		run.PendingApproval = true
		run.RequestID = pending.RequestID
		status = "pending_approval"
	}
	run.Status = status
	run.CompletedAt = s.now()
	run.ArtifactPaths = append([]string{}, result.ArtifactPaths...)
	if output, outputErr := result.OutputString(); outputErr == nil {
		run.Output = output
	}
	if execErr != nil {
		run.Error = execErr.Error()
	}
	if recordErr := manager.RecordSmokeRun(run); recordErr != nil && execErr == nil {
		execErr = recordErr
		run.Error = recordErr.Error()
	}
	persistErr := s.persistSession(session, run.CompletedAt)
	release()
	released = true
	if persistErr != nil && execErr == nil {
		execErr = persistErr
		run.Error = persistErr.Error()
		run.Status = "failed"
		_ = manager.RecordSmokeRun(run)
	}

	session.events.Emit(events.Event{
		SessionID: sessionID,
		TurnID:    turnID,
		Type:      events.EventCommandCompleted,
		Timestamp: run.CompletedAt,
		Payload: events.CommandPayload{
			Name:            "package_smoke",
			Output:          run.Output,
			RefreshSnapshot: true,
			DispatchMode:    "smoke",
			DispatchStatus:  run.Status,
			DispatchError:   run.Error,
			Error:           errorString(execErr),
		},
	})
	session.events.Emit(events.Event{
		SessionID: sessionID,
		TurnID:    turnID,
		Type:      events.EventSnapshotReady,
		Timestamp: run.CompletedAt,
		Payload: events.SnapshotPayload{
			UpdatedAt: run.CompletedAt,
			Running:   false,
		},
	})
	_ = s.writeSessionTimeline(session)
	s.appendSecurityEvent(security.SecurityEvent{
		At:        run.CompletedAt,
		Category:  "capability",
		Action:    "run_package_smoke",
		Severity:  smokeRunSeverity(run),
		SessionID: sessionID,
		Summary:   fmt.Sprintf("Package smoke %s/%s %s", run.PackageName, run.SmokeName, run.Status),
		Metadata: map[string]string{
			"package": run.PackageName,
			"smoke":   run.SmokeName,
			"run_id":  run.RunID,
			"status":  run.Status,
		},
	})
	return run, nil
}

func packageSmokeShellCommand(entry pkgregistry.Entry, smoke pkgregistry.SmokeTest) string {
	workingDir := strings.TrimSpace(smoke.WorkingDir)
	dir := entry.Path
	if workingDir != "" {
		dir = filepath.Join(entry.Path, filepath.Clean(workingDir))
	}
	return "cd " + shellQuote(dir) + " && " + strings.TrimSpace(smoke.Command)
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func packageSmokeStatus(result tools.ToolResult, err error, expected *int) string {
	want := 0
	if expected != nil {
		want = *expected
	}
	if got, ok := toolResultExitCode(result); ok {
		if got == want {
			return "passed"
		}
		return "failed"
	}
	if err == nil && want == 0 {
		return "passed"
	}
	return "failed"
}

func toolResultExitCode(result tools.ToolResult) (int, bool) {
	if result.Metadata == nil {
		return 0, false
	}
	value, ok := result.Metadata["exit_code"]
	if !ok {
		return 0, false
	}
	switch typed := value.(type) {
	case int:
		return typed, true
	case int64:
		return int(typed), true
	case float64:
		return int(typed), true
	default:
		return 0, false
	}
}

func smokeRunSeverity(run pkgregistry.SmokeRun) string {
	switch run.Status {
	case "passed":
		return "info"
	case "pending_approval":
		return "warning"
	default:
		return "warning"
	}
}
