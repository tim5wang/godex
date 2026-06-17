package skill

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/tim5wang/godex/internal/core/protocol"
	"github.com/tim5wang/godex/internal/platform/stringutil"
)

const (
	normalizedFileName      = "SKILL.normalized.json"
	coreFileName            = "SKILL.core.md"
	normalizedSchemaVersion = 2
)

var (
	namedAgentPattern   = regexp.MustCompile(`\b[a-z]{2,}-[a-z0-9-]+\b`)
	slashCommandPattern = regexp.MustCompile(`(?m)^/[-a-z0-9_]+`)
	allowedToolsPattern = regexp.MustCompile(`(?im)^allowed-tools:\s*(.+)$`)
	hooksPattern        = regexp.MustCompile(`(?im)^hooks:\s*(.+)$`)
)

// NormalizedDocument is the machine-readable on-disk artifact produced from a
// raw skill so later loads can avoid re-interpreting the full source.
type NormalizedDocument struct {
	SchemaVersion      int           `json:"schema_version"`
	SourceHash         string        `json:"source_hash"`
	GeneratedBy        string        `json:"generated_by,omitempty"`
	Name               string        `json:"name"`
	Summary            string        `json:"summary"`
	WhenToUse          []string      `json:"when_to_use,omitempty"`
	ArgumentHint       string        `json:"argument_hint,omitempty"`
	Paths              []string      `json:"paths,omitempty"`
	RecommendedBundles []string      `json:"recommended_bundles,omitempty"`
	Sections           []string      `json:"sections,omitempty"`
	Requires           Requirements  `json:"requires,omitempty"`
	Compatibility      Compatibility `json:"compatibility"`
	Warnings           []string      `json:"warnings,omitempty"`
}

// NormalizationInput is passed to an optional LLM fallback normalizer.
type NormalizationInput struct {
	Name  string
	Path  string
	Raw   string
	Skill *Skill
}

// FallbackNormalizer enriches low-structure third-party skills when
// deterministic parsing is insufficient.
type FallbackNormalizer interface {
	Normalize(ctx context.Context, input NormalizationInput) (*NormalizedDocument, string, error)
}

// ModelCaller is the minimal interface needed for LLM-backed normalization.
type ModelCaller interface {
	Call(ctx context.Context, req protocol.Request) (*protocol.Response, error)
}

// LLMNormalizer uses a model call as a fallback adapter for poorly structured skills.
type LLMNormalizer struct {
	caller    ModelCaller
	model     string
	maxTokens int
}

type jsonStringList []string

func (l *jsonStringList) UnmarshalJSON(data []byte) error {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" || trimmed == "null" {
		*l = nil
		return nil
	}

	var single string
	if err := json.Unmarshal(data, &single); err == nil {
		single = strings.TrimSpace(single)
		if single == "" {
			*l = nil
			return nil
		}
		*l = []string{single}
		return nil
	}

	var many []string
	if err := json.Unmarshal(data, &many); err == nil {
		result := make([]string, 0, len(many))
		for _, item := range many {
			if trimmed := strings.TrimSpace(item); trimmed != "" {
				result = append(result, trimmed)
			}
		}
		*l = result
		return nil
	}

	var generic []interface{}
	if err := json.Unmarshal(data, &generic); err == nil {
		result := make([]string, 0, len(generic))
		for _, item := range generic {
			text := strings.TrimSpace(fmt.Sprintf("%v", item))
			if text != "" && text != "<nil>" {
				result = append(result, text)
			}
		}
		*l = result
		return nil
	}

	return fmt.Errorf("expected string or string array")
}

// NewLLMNormalizer creates an optional LLM fallback normalizer.
func NewLLMNormalizer(caller ModelCaller, model string, maxTokens int) *LLMNormalizer {
	if caller == nil || model == "" {
		return nil
	}
	if maxTokens <= 0 {
		maxTokens = 2048
	}
	return &LLMNormalizer{
		caller:    caller,
		model:     model,
		maxTokens: maxTokens,
	}
}

// Normalize converts a loosely structured skill into normalized metadata and a
// concise core document.
func (n *LLMNormalizer) Normalize(ctx context.Context, input NormalizationInput) (*NormalizedDocument, string, error) {
	if n == nil || n.caller == nil {
		return nil, "", fmt.Errorf("llm normalizer is not configured")
	}

	system := strings.Join([]string{
		"You normalize third-party SKILL.md files into structured metadata for a local coding agent runtime.",
		"Return JSON only, with no markdown fences.",
		"Preserve the original intent, but keep the summary concise and practical.",
		"Do not invent capabilities not supported by the source skill text.",
	}, "\n")

	userPrompt := fmt.Sprintf(`Normalize this skill into JSON with keys:
name, summary, when_to_use, argument_hint, paths, recommended_bundles, sections, requires, compatibility, warnings, core.

Rules:
- recommended_bundles may only contain: core_code, planning, background, task_board, team, subagent, mcp.
- compatibility.status must be one of: native_supported, degraded_supported, unsupported.
- requires may include: mcp, named_subagents, slash_command_runtime, allowed_tools, context_fork, hooks, shell_hints, os_hints.
- core should be a concise markdown block suitable for immediate session activation.
- When uncertain, prefer degraded_supported rather than inventing native support.

Skill name: %s
Skill path: %s

Raw SKILL.md:
%s`, input.Name, input.Path, input.Raw)

	resp, err := n.caller.Call(ctx, protocol.Request{
		Model:     n.model,
		MaxTokens: n.maxTokens,
		System:    system,
		Messages:  protocol.ToAPIMessages([]protocol.Message{protocol.NewTextMessage(protocol.RoleUser, userPrompt)}),
	})
	if err != nil {
		return nil, "", err
	}

	raw := strings.TrimSpace(protocol.BlocksText(resp.Content))
	raw = trimJSONFence(raw)

	var payload struct {
		Name               string         `json:"name"`
		Summary            string         `json:"summary"`
		WhenToUse          jsonStringList `json:"when_to_use"`
		ArgumentHint       string         `json:"argument_hint"`
		Paths              jsonStringList `json:"paths"`
		RecommendedBundles jsonStringList `json:"recommended_bundles"`
		Sections           jsonStringList `json:"sections"`
		Requires           Requirements   `json:"requires"`
		Compatibility      Compatibility  `json:"compatibility"`
		Warnings           jsonStringList `json:"warnings"`
		Core               string         `json:"core"`
	}
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return nil, "", err
	}

	doc := &NormalizedDocument{
		SchemaVersion:      normalizedSchemaVersion,
		SourceHash:         sourceHash(input.Raw),
		Name:               payload.Name,
		Summary:            strings.TrimSpace(payload.Summary),
		WhenToUse:          append([]string{}, payload.WhenToUse...),
		ArgumentHint:       payload.ArgumentHint,
		Paths:              append([]string{}, payload.Paths...),
		RecommendedBundles: append([]string{}, payload.RecommendedBundles...),
		Sections:           append([]string{}, payload.Sections...),
		Requires:           payload.Requires,
		Compatibility:      payload.Compatibility,
		Warnings:           append([]string{}, payload.Warnings...),
	}
	return doc, strings.TrimSpace(payload.Core), nil
}

func trimJSONFence(value string) string {
	value = strings.TrimSpace(value)
	value = strings.TrimPrefix(value, "```json")
	value = strings.TrimPrefix(value, "```")
	value = strings.TrimSuffix(value, "```")
	return strings.TrimSpace(value)
}

func (l *Loader) SetFallbackNormalizer(normalizer FallbackNormalizer) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.fallbackNormalizer = normalizer
}

func (l *Loader) normalizeIfNeeded(skillPath string, parsed *Skill, persist bool) error {
	parsed.SourceHash = sourceHash(parsed.Content)

	if normalized, ok := l.cachedNormalized(skillPath, parsed.SourceHash); ok {
		applyNormalized(parsed, normalized)
		return nil
	}

	if normalized, ok, err := readNormalizedArtifacts(skillPath, parsed.SourceHash); err != nil {
		return err
	} else if ok {
		l.rememberNormalized(skillPath, normalized)
		applyNormalized(parsed, normalized)
		return nil
	}

	doc, core := buildDeterministicNormalized(parsed)
	doc.SourceHash = parsed.SourceHash
	if doc.SchemaVersion == 0 {
		doc.SchemaVersion = normalizedSchemaVersion
	}
	artifacts := normalizedArtifacts{Document: doc, Core: core}
	l.rememberNormalized(skillPath, artifacts)
	if persist {
		if err := writeNormalizedArtifacts(skillPath, doc, core); err != nil {
			return err
		}
	}
	applyNormalized(parsed, artifacts)
	return nil
}

// NormalizeSkill explicitly runs the configured fallback normalizer for a skill
// and persists the normalized artifacts. It is intentionally not called by
// Load, Catalog, or Install so marketplace browsing and chat startup never
// trigger model calls implicitly.
func (l *Loader) NormalizeSkill(ctx context.Context, name string) (*Skill, error) {
	skillID, err := l.ResolveID(name)
	if err != nil {
		return nil, err
	}
	skillPath, err := ResolvePath(l.skillsDir, skillID)
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(skillPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read skill %q: %w", skillID, err)
	}
	parsed, err := parseSkill(skillID, skillPath, string(data))
	if err != nil {
		return nil, err
	}

	l.mu.RLock()
	normalizer := l.fallbackNormalizer
	l.mu.RUnlock()
	if normalizer == nil {
		return nil, newSkillInvalidRequestError("LLM skill normalizer is not configured")
	}

	if ctx == nil {
		ctx = context.Background()
	}
	enriched, llmCore, err := normalizer.Normalize(ctx, NormalizationInput{
		Name:  parsed.Name,
		Path:  skillPath,
		Raw:   parsed.Content,
		Skill: parsed,
	})
	if err != nil {
		return nil, fmt.Errorf("normalize skill %q: %w", skillID, err)
	}
	if enriched == nil {
		return nil, newSkillInvalidRequestError("LLM skill normalizer returned no metadata")
	}

	doc := *enriched
	doc.SourceHash = sourceHash(parsed.Content)
	doc.GeneratedBy = "llm"
	if doc.SchemaVersion == 0 {
		doc.SchemaVersion = normalizedSchemaVersion
	}
	core := strings.TrimSpace(llmCore)
	if core == "" {
		core = strings.TrimSpace(parsed.Core)
	}
	if err := writeNormalizedArtifacts(skillPath, doc, core); err != nil {
		return nil, err
	}

	artifacts := normalizedArtifacts{Document: doc, Core: core}
	l.rememberNormalized(skillPath, artifacts)
	applyNormalized(parsed, artifacts)
	applyRuntimeCompatibility(parsed)
	applyInstallMemory(parsed, readInstallMemoryForSkillPath(skillPath))

	l.mu.Lock()
	l.skills[skillID] = parsed
	l.mu.Unlock()
	return parsed, nil
}

type normalizedArtifacts struct {
	Document NormalizedDocument
	Core     string
}

func sourceHash(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func readNormalizedArtifacts(skillPath, sourceHash string) (normalizedArtifacts, bool, error) {
	docPath, corePath := normalizedArtifactPaths(skillPath)

	docBytes, err := os.ReadFile(docPath)
	if err != nil {
		if os.IsNotExist(err) {
			return normalizedArtifacts{}, false, nil
		}
		return normalizedArtifacts{}, false, err
	}
	coreBytes, err := os.ReadFile(corePath)
	if err != nil {
		if os.IsNotExist(err) {
			return normalizedArtifacts{}, false, nil
		}
		return normalizedArtifacts{}, false, err
	}

	var doc NormalizedDocument
	if err := json.Unmarshal(docBytes, &doc); err != nil {
		return normalizedArtifacts{}, false, err
	}
	if doc.SchemaVersion != 0 && doc.SchemaVersion != normalizedSchemaVersion {
		return normalizedArtifacts{}, false, nil
	}
	if sourceHash != "" && doc.SourceHash != sourceHash {
		return normalizedArtifacts{}, false, nil
	}
	if strings.TrimSpace(doc.GeneratedBy) == "" {
		doc.GeneratedBy = "deterministic"
	}
	return normalizedArtifacts{Document: doc, Core: strings.TrimSpace(string(coreBytes))}, true, nil
}

func writeNormalizedArtifacts(skillPath string, doc NormalizedDocument, core string) error {
	docPath, corePath := normalizedArtifactPaths(skillPath)
	doc.SchemaVersion = normalizedSchemaVersion

	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(docPath, data, 0644); err != nil {
		return err
	}
	if err := os.WriteFile(corePath, []byte(strings.TrimSpace(core)+"\n"), 0644); err != nil {
		return err
	}
	return nil
}

func (l *Loader) cachedNormalized(skillPath, sourceHash string) (normalizedArtifacts, bool) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	if l.normalizedCache == nil {
		return normalizedArtifacts{}, false
	}
	artifacts, ok := l.normalizedCache[normalizedCacheKey(skillPath, sourceHash)]
	return artifacts, ok
}

func (l *Loader) rememberNormalized(skillPath string, artifacts normalizedArtifacts) {
	key := normalizedCacheKey(skillPath, artifacts.Document.SourceHash)
	if key == "" {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.normalizedCache == nil {
		l.normalizedCache = make(map[string]normalizedArtifacts)
	}
	l.normalizedCache[key] = artifacts
}

func normalizedCacheKey(skillPath, sourceHash string) string {
	skillPath = strings.TrimSpace(skillPath)
	sourceHash = strings.TrimSpace(sourceHash)
	if skillPath == "" || sourceHash == "" {
		return ""
	}
	return skillPath + "\x00" + sourceHash
}

func normalizedArtifactPaths(skillPath string) (string, string) {
	dir := filepath.Dir(skillPath)
	if strings.EqualFold(filepath.Base(skillPath), "SKILL.md") {
		return filepath.Join(dir, normalizedFileName), filepath.Join(dir, coreFileName)
	}

	base := filepath.Base(skillPath)
	ext := filepath.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	return filepath.Join(dir, stem+".normalized.json"), filepath.Join(dir, stem+".core.md")
}

func applyNormalized(parsed *Skill, normalized normalizedArtifacts) {
	doc := normalized.Document
	if doc.Name != "" {
		parsed.Name = doc.Name
	}
	if doc.Summary != "" {
		parsed.Description = doc.Summary
	}
	if len(doc.WhenToUse) > 0 {
		parsed.WhenToUse = append([]string{}, doc.WhenToUse...)
	}
	if doc.ArgumentHint != "" {
		parsed.ArgumentHint = doc.ArgumentHint
	}
	if len(doc.Paths) > 0 {
		parsed.Paths = append([]string{}, doc.Paths...)
	}
	if len(doc.RecommendedBundles) > 0 {
		parsed.RecommendedBundles = append([]string{}, doc.RecommendedBundles...)
	}
	if len(doc.Sections) > 0 {
		parsed.SectionOrder = append([]string{}, doc.Sections...)
	}
	parsed.Requires = doc.Requires
	parsed.Compatibility = doc.Compatibility
	parsed.Warnings = append([]string{}, doc.Warnings...)
	parsed.SourceHash = doc.SourceHash
	parsed.NormalizationSource = strings.TrimSpace(doc.GeneratedBy)
	if parsed.NormalizationSource == "" {
		parsed.NormalizationSource = "deterministic"
	}
	if strings.TrimSpace(normalized.Core) != "" {
		parsed.Core = strings.TrimSpace(normalized.Core)
	}
}

func buildDeterministicNormalized(parsed *Skill) (NormalizedDocument, string) {
	requires, warnings := analyzeRequirements(parsed)
	compat := analyzeCompatibility(requires)
	for _, warning := range warnings {
		compat.Notes = stringutil.AppendUnique(compat.Notes, warning)
	}
	recommendedBundles := deriveRecommendedBundles(parsed, requires)
	core := strings.TrimSpace(parsed.Core)
	if core == "" {
		core = buildDeterministicCore(parsed, compat, warnings)
	}

	doc := NormalizedDocument{
		SourceHash:         sourceHash(parsed.Content),
		GeneratedBy:        "deterministic",
		Name:               parsed.Name,
		Summary:            parsed.Description,
		WhenToUse:          fallbackWhenToUse(parsed),
		ArgumentHint:       parsed.ArgumentHint,
		Paths:              append([]string{}, parsed.Paths...),
		RecommendedBundles: recommendedBundles,
		Sections:           append([]string{}, parsed.SectionOrder...),
		Requires:           requires,
		Compatibility:      compat,
		Warnings:           warnings,
	}
	return doc, core
}

func fallbackWhenToUse(parsed *Skill) []string {
	if len(parsed.WhenToUse) > 0 {
		return append([]string{}, parsed.WhenToUse...)
	}
	if parsed.Description != "" && !strings.HasPrefix(parsed.Description, "Skill: ") {
		return []string{parsed.Description}
	}
	return nil
}

func analyzeRequirements(parsed *Skill) (Requirements, []string) {
	text := strings.ToLower(parsed.Content)
	req := Requirements{}
	warnings := make([]string, 0, 8)

	if strings.Contains(text, "mcp__") || strings.Contains(text, "mcp ") || strings.Contains(text, "mcp_") {
		req.MCP = true
		warnings = append(warnings, "References MCP resources or tools.")
	}

	if strings.Contains(text, "subagent") && namedAgentPattern.MatchString(text) {
		req.NamedSubagents = true
		warnings = append(warnings, "Assumes named specialist subagents.")
	}

	if strings.Contains(text, "slash command") || slashCommandPattern.MatchString(parsed.Content) {
		req.SlashCommandRuntime = true
		warnings = append(warnings, "Assumes slash-command runtime semantics.")
	}

	if matches := allowedToolsPattern.FindStringSubmatch(parsed.Content); len(matches) == 2 {
		req.AllowedTools = append(req.AllowedTools, parseAllowedTools(matches[1])...)
	}
	if strings.Contains(text, "context: fork") {
		req.ContextFork = true
		warnings = append(warnings, "Requests forked execution context.")
	}
	if strings.Contains(text, "hook") || strings.Contains(text, "hooks") {
		req.Hooks = true
		warnings = append(warnings, "Mentions hook-based runtime behavior.")
	}
	if matches := hooksPattern.FindStringSubmatch(parsed.Content); len(matches) == 2 {
		req.HookNames = append(req.HookNames, splitCSVLike(matches[1])...)
	}
	for _, hookName := range []string{"on_start", "on_complete", "on_error"} {
		if strings.Contains(text, hookName) {
			req.Hooks = true
			req.HookNames = stringutil.AppendUnique(req.HookNames, hookName)
		}
	}

	if strings.Contains(text, "bash") || strings.Contains(text, ".sh") || strings.Contains(text, "shell script") {
		req.ShellHints = stringutil.AppendUnique(req.ShellHints, "bash")
	}
	if strings.Contains(text, "powershell") || strings.Contains(text, ".ps1") {
		req.ShellHints = stringutil.AppendUnique(req.ShellHints, "powershell")
	}
	if strings.Contains(parsed.Content, "C:\\") || strings.Contains(text, "windows") {
		req.OSHints = stringutil.AppendUnique(req.OSHints, "windows")
	}
	if strings.Contains(text, "darwin") || strings.Contains(text, "macos") {
		req.OSHints = stringutil.AppendUnique(req.OSHints, "darwin")
	}
	if strings.Contains(text, "linux") {
		req.OSHints = stringutil.AppendUnique(req.OSHints, "linux")
	}

	req.AllowedTools = stringutil.Unique(req.AllowedTools)
	req.Executables = stringutil.Unique(executableHintsFromAllowedTools(req.AllowedTools))
	req.HookNames = stringutil.Unique(req.HookNames)
	warnings = stringutil.Unique(warnings)
	return req, warnings
}

func analyzeCompatibility(req Requirements) Compatibility {
	compat := Compatibility{Status: CompatibilityNativeSupported}

	if req.MCP {
		compat.Status = CompatibilityDegradedSupported
		compat.MissingCapabilities = append(compat.MissingCapabilities, "mcp")
	}
	if req.NamedSubagents {
		compat.Status = CompatibilityDegradedSupported
		compat.MissingCapabilities = append(compat.MissingCapabilities, "named_subagents")
	}
	if req.SlashCommandRuntime {
		compat.Status = CompatibilityDegradedSupported
		compat.MissingCapabilities = append(compat.MissingCapabilities, "slash_command_runtime")
	}
	if req.Hooks {
		compat.Status = CompatibilityDegradedSupported
		compat.MissingCapabilities = append(compat.MissingCapabilities, "hooks")
	}
	if req.ContextFork {
		compat.Status = CompatibilityDegradedSupported
		compat.MissingCapabilities = append(compat.MissingCapabilities, "context_fork")
	}
	if len(req.OSHints) > 0 {
		compat.Status = CompatibilityDegradedSupported
		compat.Notes = append(compat.Notes, "Contains OS-specific assumptions.")
	}
	if len(req.ShellHints) > 0 {
		compat.Notes = append(compat.Notes, "Contains shell-specific execution guidance.")
	}
	compat.MissingCapabilities = stringutil.Unique(compat.MissingCapabilities)
	compat.Notes = stringutil.Unique(compat.Notes)
	return compat
}

func deriveRecommendedBundles(parsed *Skill, req Requirements) []string {
	bundles := append([]string{}, parsed.RecommendedBundles...)
	text := strings.ToLower(parsed.Content)

	if strings.Contains(text, "bash") || strings.Contains(text, "read_file") || strings.Contains(text, "write_file") || strings.Contains(text, "edit_file") || len(req.ShellHints) > 0 {
		bundles = append(bundles, "core_code")
	}
	if strings.Contains(text, "background") || strings.Contains(text, "smoke_test") || strings.Contains(text, "long-running") {
		bundles = append(bundles, "background")
	}
	if strings.Contains(text, "task_") || strings.Contains(text, "task board") {
		bundles = append(bundles, "task_board")
	}
	if strings.Contains(text, "message") || strings.Contains(text, "inbox") || strings.Contains(text, "teammate") {
		bundles = append(bundles, "team")
	}
	if strings.Contains(text, "todo") || strings.Contains(text, "plan") {
		bundles = append(bundles, "planning")
	}
	if strings.Contains(text, "subagent") || req.NamedSubagents || req.ContextFork {
		bundles = append(bundles, "subagent")
	}
	if req.MCP {
		bundles = append(bundles, "mcp")
	}
	bundles = append(bundles, mapAllowedToolsToBundles(req.AllowedTools)...)
	return stringutil.Unique(bundles)
}

func buildDeterministicCore(parsed *Skill, compat Compatibility, warnings []string) string {
	lines := []string{
		fmt.Sprintf("Skill `%s` has been normalized from a third-party or legacy source.", parsed.Name),
	}
	if parsed.Description != "" {
		lines = append(lines, "", "Summary: "+parsed.Description)
	}
	if len(parsed.WhenToUse) > 0 {
		lines = append(lines, "", "When to use:")
		for _, item := range parsed.WhenToUse {
			lines = append(lines, "- "+item)
		}
	}
	lines = append(lines, "", "Compatibility: "+string(compat.Status))
	if len(compat.MissingCapabilities) > 0 {
		lines = append(lines, "Missing capabilities: "+strings.Join(compat.MissingCapabilities, ", "))
	}
	if len(warnings) > 0 {
		lines = append(lines, "", "Warnings:")
		for _, warning := range warnings {
			lines = append(lines, "- "+warning)
		}
	}
	return strings.Join(lines, "\n")
}

func splitCSVLike(input string) []string {
	fields := strings.FieldsFunc(input, func(r rune) bool {
		return r == ',' || r == ';'
	})
	result := make([]string, 0, len(fields))
	for _, field := range fields {
		field = strings.Trim(field, " `\"'")
		if field != "" {
			result = append(result, field)
		}
	}
	return stringutil.Unique(result)
}

func parseAllowedTools(input string) []string {
	input = strings.TrimSpace(input)
	if input == "" {
		return nil
	}
	parts := splitCSVLike(input)
	if len(parts) == 1 && parts[0] == input {
		parts = strings.Fields(input)
	}
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			result = append(result, part)
		}
	}
	return stringutil.Unique(result)
}

func executableHintsFromAllowedTools(items []string) []string {
	executables := make([]string, 0, len(items))
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		lower := strings.ToLower(item)
		switch {
		case strings.HasPrefix(lower, "bash("), strings.HasPrefix(lower, "sh("), strings.HasPrefix(lower, "zsh("):
			open := strings.Index(item, "(")
			close := strings.LastIndex(item, ")")
			if open < 0 || close <= open {
				continue
			}
			raw := strings.TrimSpace(item[open+1 : close])
			raw = strings.TrimSuffix(raw, ":*")
			raw = strings.TrimSpace(raw)
			if raw == "" {
				continue
			}
			segments := strings.Fields(raw)
			if len(segments) > 0 {
				executables = append(executables, filepath.Base(segments[0]))
			}
		case strings.HasPrefix(lower, "powershell("):
			executables = append(executables, "pwsh")
		case strings.EqualFold(item, "bash"):
			executables = append(executables, "bash")
		case strings.EqualFold(item, "powershell"):
			executables = append(executables, "pwsh")
		}
	}
	return stringutil.Unique(executables)
}

func mapAllowedToolsToBundles(toolNames []string) []string {
	bundles := make([]string, 0, len(toolNames))
	for _, toolName := range toolNames {
		normalized := strings.ToLower(strings.TrimSpace(toolName))
		if open := strings.Index(normalized, "("); open > 0 {
			normalized = normalized[:open]
		}
		switch normalized {
		case "bash", "read_file", "write_file", "edit_file":
			bundles = append(bundles, "core_code")
		case "background":
			bundles = append(bundles, "background")
		case "task":
			bundles = append(bundles, "task_board")
		case "subagent":
			bundles = append(bundles, "subagent")
		case "read_inbox", "send_message", "broadcast", "shutdown_request", "list_teammates", "plan_approval":
			bundles = append(bundles, "team")
		case "list_mcp_resources", "read_mcp_resource":
			bundles = append(bundles, "mcp")
		case "desktop":
			bundles = append(bundles, "desktop")
		}
	}
	return stringutil.Unique(bundles)
}
