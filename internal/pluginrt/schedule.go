package pluginrt

import (
	"context"
	"fmt"
	"strings"
	"time"

	cronlib "github.com/robfig/cron/v3"
)

// ScheduleSpec describes one recurring plugin callback. Exactly one of
// CronExpr (standard 5-field, robfig/cron ParseStandard) or Every (fixed
// interval) must be set.
type ScheduleSpec struct {
	CronExpr string
	Every    time.Duration
}

func (s ScheduleSpec) validate() error {
	hasCron := strings.TrimSpace(s.CronExpr) != ""
	hasEvery := s.Every > 0
	if hasCron == hasEvery {
		return fmt.Errorf("schedule requires exactly one of cron_expr or every")
	}
	if hasCron {
		if _, err := cronlib.ParseStandard(strings.TrimSpace(s.CronExpr)); err != nil {
			return fmt.Errorf("invalid cron expression %q: %w", s.CronExpr, err)
		}
	}
	return nil
}

func (s ScheduleSpec) expression() string {
	if strings.TrimSpace(s.CronExpr) != "" {
		return strings.TrimSpace(s.CronExpr)
	}
	return fmt.Sprintf("@every %dms", s.Every.Milliseconds())
}

// scheduleCron lazily starts the shared scheduler. robfig/cron runs jobs on
// its own goroutines, so plugin callbacks must be goroutine-safe.
func (m *Manager) scheduleCron() *cronlib.Cron {
	m.schedMu.Lock()
	defer m.schedMu.Unlock()
	if m.schedCron == nil {
		m.schedCron = cronlib.New()
		m.schedCron.Start()
	}
	return m.schedCron
}

// registerSchedule adds one recurring callback for pluginID. Reversal is via
// removeSchedules (called from the effect ledger on deactivation).
func (m *Manager) registerSchedule(pluginID, name string, spec ScheduleSpec, fn func(ctx context.Context)) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("plugin %s schedule name is required", pluginID)
	}
	if err := spec.validate(); err != nil {
		return fmt.Errorf("plugin %s schedule %s: %w", pluginID, name, err)
	}
	if fn == nil {
		return fmt.Errorf("plugin %s schedule %s: callback is required", pluginID, name)
	}
	c := m.scheduleCron()
	entryID, err := c.AddFunc(spec.expression(), func() {
		fn(context.Background())
	})
	if err != nil {
		return fmt.Errorf("plugin %s schedule %s: %w", pluginID, name, err)
	}
	m.schedMu.Lock()
	defer m.schedMu.Unlock()
	if m.schedEntries[pluginID] == nil {
		m.schedEntries[pluginID] = make(map[string]cronlib.EntryID)
	}
	m.schedEntries[pluginID][name] = entryID
	return nil
}

// removeSchedules removes every schedule registered by pluginID (idempotent).
func (m *Manager) removeSchedules(pluginID string) {
	m.schedMu.Lock()
	entries := m.schedEntries[pluginID]
	delete(m.schedEntries, pluginID)
	c := m.schedCron
	m.schedMu.Unlock()
	if c == nil {
		return
	}
	for _, entryID := range entries {
		c.Remove(entryID)
	}
}
