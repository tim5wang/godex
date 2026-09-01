package commands

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/tim5wang/godex/internal/agent"
	"github.com/tim5wang/godex/internal/core/memory"
	"github.com/tim5wang/godex/internal/core/notes"
)

func (s *Service) executeMemory(a *agent.Agent, cmd Command) (Result, error) {
	mgr := a.MemoryMgr()
	if mgr == nil {
		return Result{Name: "memory", Output: "Memory runtime is unavailable in this process."}, nil
	}
	if len(cmd.Args) == 0 {
		cmd.Args = []string{"list"}
	}
	switch strings.ToLower(strings.TrimSpace(cmd.Args[0])) {
	case "list":
		items, err := mgr.List()
		if err != nil {
			return Result{}, err
		}
		return Result{Name: "memory", Output: renderMemoryList(items)}, nil
	case "get":
		if len(cmd.Args) < 2 {
			return Result{}, fmt.Errorf("usage: /memory get <id-or-title>")
		}
		record, err := mgr.Get(strings.Join(cmd.Args[1:], " "))
		if err != nil {
			return Result{}, err
		}
		return Result{Name: "memory", Output: renderStoredMemory(*record)}, nil
	case "search":
		if len(cmd.Args) < 2 {
			return Result{}, fmt.Errorf("usage: /memory search <query>")
		}
		items, err := mgr.Search(memory.SearchOptions{Query: strings.Join(cmd.Args[1:], " ")})
		if err != nil {
			return Result{}, err
		}
		return Result{Name: "memory", Output: renderMemorySearch(items)}, nil
	case "candidates":
		items, err := mgr.ListCandidates()
		if err != nil {
			return Result{}, err
		}
		return Result{Name: "memory", Output: renderMemoryCandidates(items)}, nil
	case "accept":
		if len(cmd.Args) < 2 {
			return Result{}, fmt.Errorf("usage: /memory accept <fingerprint>")
		}
		entry, err := mgr.AcceptCandidate(strings.TrimSpace(cmd.Args[1]))
		if err != nil {
			return Result{}, err
		}
		return Result{Name: "memory", Output: renderMemoryAccept(entry)}, nil
	case "dismiss":
		if len(cmd.Args) < 2 {
			return Result{}, fmt.Errorf("usage: /memory dismiss <fingerprint>")
		}
		candidate, err := mgr.DismissCandidate(strings.TrimSpace(cmd.Args[1]))
		if err != nil {
			return Result{}, err
		}
		return Result{Name: "memory", Output: renderMemoryDismiss(candidate)}, nil
	case "digest":
		if len(cmd.Args) > 1 {
			return Result{}, fmt.Errorf("command /memory digest does not accept arguments")
		}
		return s.executeMemoryDigest(a)
	case "log":
		return s.executeMemoryLog(a, Command{Name: "memory-log", Args: cmd.Args[1:]})
	case "restore":
		return s.executeMemoryRestore(a, Command{Name: "memory-restore", Args: cmd.Args[1:]})
	default:
		return Result{}, fmt.Errorf("unknown /memory subcommand %q", cmd.Args[0])
	}
}

func (s *Service) executeNote(ctx context.Context, cmd Command) (Result, error) {
	manager := notes.NewManager(s.notesDir())
	if len(cmd.Args) == 0 {
		return Result{Name: "note", Output: "Usage: /note list|search [query] [--tag tag], /note create <title> [--tags a,b] -- <markdown>, /note append [id] -- <markdown>, or /note update [id] -- <markdown>"}, nil
	}
	switch strings.ToLower(strings.TrimSpace(cmd.Args[0])) {
	case "list", "search":
		opts := parseNoteSearchArgs(cmd.Args[1:])
		items, err := manager.List(opts)
		if err != nil {
			return Result{}, err
		}
		return Result{Name: "note", Output: renderNotesList(items)}, nil
	case "get":
		if len(cmd.Args) < 2 {
			return Result{}, fmt.Errorf("usage: /note get <id>")
		}
		item, err := manager.Get(cmd.Args[1])
		if err != nil {
			return Result{}, err
		}
		return Result{Name: "note", Output: renderNote(item)}, nil
	case "create", "new":
		input := parseNoteCreateArgs(cmd.Args[1:])
		item, err := manager.Save(input)
		if err != nil {
			return Result{}, err
		}
		return Result{Name: "note", Output: fmt.Sprintf("Created note %s at %s.", item.ID, item.Path)}, nil
	case "append":
		noteID, content := parseNoteMutationArgs(cmd.Args[1:], currentNoteID(ctx, cmd))
		if noteID == "" {
			return Result{}, fmt.Errorf("usage: /note append <id> -- <markdown>")
		}
		if strings.TrimSpace(content) == "" {
			return Result{}, fmt.Errorf("note append content is required")
		}
		item, err := manager.Get(noteID)
		if err != nil {
			return Result{}, err
		}
		nextContent := strings.TrimSpace(item.Content)
		if nextContent != "" {
			nextContent += "\n\n"
		}
		nextContent += strings.TrimSpace(content)
		updated, err := manager.Save(notes.SaveInput{ID: item.ID, Title: item.Title, Summary: item.Summary, Tags: item.Tags, Content: nextContent})
		if err != nil {
			return Result{}, err
		}
		return Result{Name: "note", Output: fmt.Sprintf("Appended note %s at %s.", updated.ID, updated.Path), RefreshSnapshot: true}, nil
	case "update", "edit":
		noteID, content := parseNoteMutationArgs(cmd.Args[1:], currentNoteID(ctx, cmd))
		if noteID == "" {
			return Result{}, fmt.Errorf("usage: /note update <id> -- <markdown>")
		}
		if strings.TrimSpace(content) == "" {
			return Result{}, fmt.Errorf("note update content is required")
		}
		item, err := manager.Get(noteID)
		if err != nil {
			return Result{}, err
		}
		updated, err := manager.Save(notes.SaveInput{ID: item.ID, Title: item.Title, Summary: item.Summary, Tags: item.Tags, Content: strings.TrimSpace(content)})
		if err != nil {
			return Result{}, err
		}
		return Result{Name: "note", Output: fmt.Sprintf("Updated note %s at %s.", updated.ID, updated.Path), RefreshSnapshot: true}, nil
	default:
		input := parseNoteCreateArgs(cmd.Args)
		item, err := manager.Save(input)
		if err != nil {
			return Result{}, err
		}
		return Result{Name: "note", Output: fmt.Sprintf("Created note %s at %s.", item.ID, item.Path)}, nil
	}
}

func (s *Service) notesDir() string {
	s.mu.RLock()
	cfg := s.cfg
	s.mu.RUnlock()
	if cfg == nil {
		return filepath.Join(".godex", "notes")
	}
	if strings.TrimSpace(cfg.HomeDir) != "" {
		return filepath.Join(cfg.HomeDir, "notes")
	}
	return filepath.Join(cfg.StateDir, "notes")
}

func (s *Service) executeMemoryDigest(a *agent.Agent) (Result, error) {
	mgr := a.MemoryMgr()
	if mgr == nil {
		return Result{Name: "memory-digest", Output: "Memory runtime is unavailable in this process."}, nil
	}

	s.mu.RLock()
	analyze := s.analyze
	cfg := s.cfg
	s.mu.RUnlock()

	report, err := analyze(buildInsightsInput(collectInsightsSnapshot(a)))
	if err != nil {
		return Result{}, err
	}
	extractor := memory.NewExtractor(mgr, cfg.TempDir)
	added, err := extractor.CaptureInsightsReport(report)
	if err != nil {
		return Result{}, err
	}

	markdown := report.Markdown()
	reportPath := filepath.Join(cfg.TempDir, "memory-digest-latest.md")
	if writeErr := os.WriteFile(reportPath, []byte(markdown), 0644); writeErr != nil {
		output := renderMemoryDigest(markdown, added, "")
		return Result{Name: "memory-digest", Output: output, RefreshSnapshot: len(added) > 0}, fmt.Errorf("write memory digest report: %w", writeErr)
	}
	return Result{
		Name:            "memory-digest",
		Output:          renderMemoryDigest(markdown, added, reportPath),
		ArtifactPath:    reportPath,
		RefreshSnapshot: len(added) > 0,
	}, nil
}

func (s *Service) executeMemoryLog(a *agent.Agent, cmd Command) (Result, error) {
	mgr := a.MemoryMgr()
	if mgr == nil {
		return Result{Name: "memory-log", Output: "Memory runtime is unavailable in this process."}, nil
	}
	limit := 20
	if len(cmd.Args) > 0 {
		parsed, err := strconv.Atoi(strings.TrimSpace(cmd.Args[0]))
		if err != nil || parsed <= 0 {
			return Result{}, fmt.Errorf("usage: /memory-log [limit]")
		}
		limit = parsed
	}
	items, err := mgr.ListAudit(limit)
	if err != nil {
		return Result{}, err
	}
	return Result{Name: "memory-log", Output: renderMemoryAuditLog(items)}, nil
}

func (s *Service) executeMemoryRestore(a *agent.Agent, cmd Command) (Result, error) {
	mgr := a.MemoryMgr()
	if mgr == nil {
		return Result{Name: "memory-restore", Output: "Memory runtime is unavailable in this process."}, nil
	}
	if len(cmd.Args) < 1 {
		return Result{}, fmt.Errorf("usage: /memory-restore <audit-id> [before|after]")
	}
	target := "before"
	if len(cmd.Args) > 1 {
		target = strings.TrimSpace(cmd.Args[1])
	}
	entry, err := mgr.RestoreAudit(strings.TrimSpace(cmd.Args[0]), target)
	if err != nil {
		return Result{}, err
	}
	return Result{Name: "memory-restore", Output: renderMemoryRestore(entry, target), RefreshSnapshot: true}, nil
}

func renderMemoryList(items []memory.Entry) string {
	if len(items) == 0 {
		return "No durable memories yet."
	}
	lines := []string{"Durable memories:"}
	for _, item := range items {
		line := fmt.Sprintf("- %s [%s] updated %s", item.Title, item.Type, formatMemoryTime(item.UpdatedAt))
		if len(item.Tags) > 0 {
			line += " tags=" + strings.Join(item.Tags, ",")
		}
		if item.Source != "" {
			line += " source=" + item.Source
		}
		line += " id=" + item.ID
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

func renderNotesList(items []notes.Note) string {
	if len(items) == 0 {
		return "No notes yet."
	}
	lines := []string{"Notes:"}
	for _, item := range items {
		line := fmt.Sprintf("- %s id=%s updated=%s", item.Title, item.ID, formatMemoryTime(item.UpdatedAt))
		if len(item.Tags) > 0 {
			line += " tags=" + strings.Join(item.Tags, ",")
		}
		if item.Summary != "" {
			line += " — " + item.Summary
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

func renderNote(item notes.Note) string {
	lines := []string{
		item.Title,
		"id: " + item.ID,
		"path: " + item.Path,
		"updated: " + formatMemoryTime(item.UpdatedAt),
	}
	if len(item.Tags) > 0 {
		lines = append(lines, "tags: "+strings.Join(item.Tags, ", "))
	}
	if item.Summary != "" {
		lines = append(lines, "summary: "+item.Summary)
	}
	if strings.TrimSpace(item.Content) != "" {
		lines = append(lines, "", strings.TrimSpace(item.Content))
	}
	return strings.Join(lines, "\n")
}

func parseNoteSearchArgs(args []string) notes.SearchOptions {
	filtered := make([]string, 0, len(args))
	var tag string
	for i := 0; i < len(args); i++ {
		arg := strings.TrimSpace(args[i])
		switch {
		case arg == "--tag" && i+1 < len(args):
			tag = args[i+1]
			i++
		case strings.HasPrefix(arg, "--tag="):
			tag = strings.TrimPrefix(arg, "--tag=")
		default:
			filtered = append(filtered, args[i])
		}
	}
	return notes.SearchOptions{
		Query: strings.TrimSpace(strings.Join(filtered, " ")),
		Tag:   strings.TrimSpace(tag),
	}
}

func parseNoteCreateArgs(args []string) notes.SaveInput {
	filtered := make([]string, 0, len(args))
	var tags []string
	for i := 0; i < len(args); i++ {
		arg := strings.TrimSpace(args[i])
		switch {
		case arg == "--tags" && i+1 < len(args):
			tags = append(tags, splitCommandTags(args[i+1])...)
			i++
		case strings.HasPrefix(arg, "--tags="):
			tags = append(tags, splitCommandTags(strings.TrimPrefix(arg, "--tags="))...)
		default:
			filtered = append(filtered, args[i])
		}
	}
	raw := strings.TrimSpace(strings.Join(filtered, " "))
	if raw == "" {
		return notes.SaveInput{Tags: tags}
	}
	if before, after, ok := strings.Cut(raw, " -- "); ok {
		title := strings.TrimSpace(before)
		content := strings.TrimSpace(after)
		if content != "" && !strings.HasPrefix(content, "#") {
			content = "# " + title + "\n\n" + content
		}
		return notes.SaveInput{Title: title, Content: content, Tags: tags}
	}
	return notes.SaveInput{Title: raw, Content: "# " + raw, Tags: tags}
}

func parseNoteMutationArgs(args []string, fallbackID string) (string, string) {
	raw := strings.TrimSpace(strings.Join(args, " "))
	if raw == "" {
		return strings.TrimSpace(fallbackID), ""
	}
	if strings.HasPrefix(raw, "-- ") {
		return strings.TrimSpace(fallbackID), strings.TrimSpace(strings.TrimPrefix(raw, "-- "))
	}
	before, after, ok := strings.Cut(raw, " -- ")
	if !ok {
		return strings.TrimSpace(fallbackID), raw
	}
	before = strings.TrimSpace(before)
	if before == "" {
		before = fallbackID
	}
	return strings.TrimSpace(before), strings.TrimSpace(after)
}

func currentNoteID(ctx context.Context, cmd Command) string {
	if value := strings.TrimSpace(cmd.Metadata["note_id"]); value != "" {
		return value
	}
	if strings.EqualFold(cmd.Metadata["app_object_type"], "note") {
		if value := strings.TrimSpace(cmd.Metadata["app_object_id"]); value != "" {
			return value
		}
	}
	current, ok := CurrentSessionContext(ctx)
	if !ok {
		return ""
	}
	if value := strings.TrimSpace(current.Metadata["note_id"]); value != "" {
		return value
	}
	if strings.EqualFold(current.Metadata["app_object_type"], "note") {
		return strings.TrimSpace(current.Metadata["app_object_id"])
	}
	return ""
}

func splitCommandTags(value string) []string {
	parts := strings.FieldsFunc(value, func(r rune) bool {
		return r == ',' || r == ';'
	})
	tags := make([]string, 0, len(parts))
	for _, part := range parts {
		if tag := strings.TrimSpace(part); tag != "" {
			tags = append(tags, tag)
		}
	}
	return tags
}

func renderStoredMemory(item memory.StoredMemory) string {
	lines := []string{
		item.Title,
		fmt.Sprintf("id: %s", item.ID),
		fmt.Sprintf("type: %s", item.Type),
		fmt.Sprintf("updated: %s", formatMemoryTime(item.UpdatedAt)),
	}
	if !item.CreatedAt.IsZero() {
		lines = append(lines, fmt.Sprintf("created: %s", formatMemoryTime(item.CreatedAt)))
	}
	if item.File != "" {
		lines = append(lines, "file: "+item.File)
	}
	if item.Source != "" {
		lines = append(lines, "source: "+item.Source)
	}
	if len(item.Tags) > 0 {
		lines = append(lines, "tags: "+strings.Join(item.Tags, ", "))
	}
	if item.Summary != "" {
		lines = append(lines, "summary: "+item.Summary)
	}
	if strings.TrimSpace(item.Content) != "" {
		lines = append(lines, "", strings.TrimSpace(item.Content))
	}
	return strings.Join(lines, "\n")
}

func renderMemorySearch(items []memory.StoredMemory) string {
	if len(items) == 0 {
		return "No durable memories matched that query."
	}
	lines := []string{"Memory search results:"}
	for _, item := range items {
		line := fmt.Sprintf("- %s [%s] — %s", item.Title, item.Type, item.Summary)
		if len(item.Tags) > 0 {
			line += " tags=" + strings.Join(item.Tags, ",")
		}
		if item.Source != "" {
			line += " source=" + item.Source
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

func renderMemoryCandidates(items []memory.Candidate) string {
	if len(items) == 0 {
		return "No pending memory candidates."
	}
	lines := []string{"Pending memory candidates:"}
	for _, item := range items {
		line := fmt.Sprintf("- %s [%s] — %s", item.Title, item.Type, item.Summary)
		if item.Source != "" {
			line += " source=" + item.Source
		}
		if !item.CreatedAt.IsZero() {
			line += " created=" + formatMemoryTime(item.CreatedAt)
		}
		line += " fingerprint=" + item.Fingerprint
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

func renderMemoryAccept(item *memory.Entry) string {
	if item == nil {
		return "Accepted memory candidate."
	}
	return fmt.Sprintf("Accepted memory candidate: %s [%s] id=%s", item.Title, item.Type, item.ID)
}

func renderMemoryDismiss(item *memory.Candidate) string {
	if item == nil {
		return "Dismissed memory candidate."
	}
	return fmt.Sprintf("Dismissed memory candidate: %s [%s] fingerprint=%s", item.Title, item.Type, item.Fingerprint)
}

func renderMemoryAuditLog(items []memory.AuditLogEntry) string {
	if len(items) == 0 {
		return "No durable memory audit entries yet."
	}
	lines := []string{"Durable memory audit log:"}
	for _, item := range items {
		title := strings.TrimSpace(item.Title)
		if title == "" {
			title = "(candidate only)"
		}
		line := fmt.Sprintf("- %s %s [%s] %s", item.ID, item.Action, item.Type, title)
		if item.MemoryID != "" {
			line += " memory_id=" + item.MemoryID
		}
		if item.CandidateFingerprint != "" {
			line += " candidate=" + item.CandidateFingerprint
		}
		if !item.CreatedAt.IsZero() {
			line += " at=" + formatMemoryTime(item.CreatedAt)
		}
		if item.Message != "" {
			line += " — " + item.Message
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

func renderMemoryDigest(markdown string, added []memory.Candidate, reportPath string) string {
	lines := []string{"Memory digest completed."}
	if len(added) == 0 {
		lines = append(lines, "No new durable-memory candidates were added.")
	} else {
		lines = append(lines, fmt.Sprintf("Added %d durable-memory candidate(s):", len(added)))
		for _, item := range added {
			line := fmt.Sprintf("- %s [%s] — %s fingerprint=%s", item.Title, item.Type, item.Summary, item.Fingerprint)
			if item.Source != "" {
				line += " source=" + item.Source
			}
			lines = append(lines, line)
		}
	}
	if strings.TrimSpace(reportPath) != "" {
		lines = append(lines, "Saved digest report to "+reportPath)
	}
	if strings.TrimSpace(markdown) != "" {
		lines = append(lines, "", strings.TrimSpace(markdown))
	}
	return strings.Join(lines, "\n")
}

func renderMemoryRestore(item *memory.AuditLogEntry, target string) string {
	if item == nil {
		return "Restored memory audit snapshot."
	}
	target = strings.TrimSpace(target)
	if target == "" {
		target = "before"
	}
	title := item.Title
	if title == "" {
		title = item.MemoryID
	}
	return fmt.Sprintf("Restored %s snapshot from %s: %s [%s]", target, item.ID, title, item.Type)
}
