package packages

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/tim5wang/godex/internal/platform/fsutil"
	"github.com/tim5wang/godex/internal/platform/tooling"
	"github.com/tim5wang/godex/internal/platform/workspacefs"
	"gopkg.in/yaml.v3"
)

const (
	ManifestFileName  = "godex.package.yaml"
	registryFileName  = "registry.json"
	smokeRunsFileName = "smoke_runs.json"
)

// Manifest is the declaration-only package format.
type Manifest struct {
	Name               string      `yaml:"name" json:"name"`
	Version            string      `yaml:"version" json:"version"`
	Description        string      `yaml:"description" json:"description,omitempty"`
	Resources          Resources   `yaml:"resources" json:"resources"`
	App                AppManifest `yaml:"app" json:"app,omitempty"`
	Permissions        []string    `yaml:"permissions" json:"permissions,omitempty"`
	Capabilities       []string    `yaml:"capabilities" json:"capabilities,omitempty"`
	Provides           []string    `yaml:"provides" json:"provides,omitempty"`
	Requires           []string    `yaml:"requires" json:"requires,omitempty"`
	Runtime            RuntimeDecl `yaml:"runtime" json:"runtime,omitempty"`
	ToolPolicy         []string    `yaml:"tool_policy" json:"tool_policy,omitempty"`
	SmokeTests         []SmokeTest `yaml:"smoke_tests" json:"smoke_tests,omitempty"`
	RecommendedBundles []string    `yaml:"recommended_bundles" json:"recommended_bundles,omitempty"`
}

// RuntimeDecl declares an executable plugin payload (阶段 B / P3): the package
// stays a declarative resource bundle, but may optionally ship a WASM module
// executed by the pluginrt wasm adapter. Install records digest/trust; the
// kernel owns the instance lifecycle.
type RuntimeDecl struct {
	Kind   string `yaml:"kind" json:"kind,omitempty"`
	Module string `yaml:"module" json:"module,omitempty"`
	ABI    string `yaml:"abi" json:"abi,omitempty"`
}

type SmokeTest struct {
	Name                string   `json:"name" yaml:"name"`
	Command             string   `json:"command" yaml:"command"`
	WorkingDir          string   `json:"working_dir,omitempty" yaml:"working_dir,omitempty"`
	TimeoutSeconds      int      `json:"timeout_seconds,omitempty" yaml:"timeout_seconds,omitempty"`
	RequiredPermissions []string `json:"required_permissions,omitempty" yaml:"required_permissions,omitempty"`
	ExpectedExitCode    *int     `json:"expected_exit_code,omitempty" yaml:"expected_exit_code,omitempty"`
}

type Resources struct {
	Skills   []string `yaml:"skills" json:"skills,omitempty"`
	Prompts  []string `yaml:"prompts" json:"prompts,omitempty"`
	Commands []string `yaml:"commands" json:"commands,omitempty"`
	Roles    []string `yaml:"roles" json:"roles,omitempty"`
	Docs     []string `yaml:"docs" json:"docs,omitempty"`
	Assets   []string `yaml:"assets" json:"assets,omitempty"`
}

type AppManifest struct {
	Kind   string         `yaml:"kind" json:"kind,omitempty"`
	ID     string         `yaml:"id" json:"id,omitempty"`
	Label  string         `yaml:"label" json:"label,omitempty"`
	Config map[string]any `yaml:"config" json:"config,omitempty"`
}

type Registry struct {
	Packages []Entry `json:"packages"`
}

type Entry struct {
	Name               string      `json:"name"`
	Version            string      `json:"version"`
	Description        string      `json:"description,omitempty"`
	Source             string      `json:"source"`
	Digest             string      `json:"digest"`
	Path               string      `json:"path"`
	InstalledAt        time.Time   `json:"installed_at"`
	Resources          Resources   `json:"resources"`
	App                AppManifest `json:"app,omitempty"`
	Permissions        []string    `json:"permissions,omitempty"`
	Capabilities       []string    `json:"capabilities,omitempty"`
	Provides           []string    `json:"provides,omitempty"`
	Requires           []string    `json:"requires,omitempty"`
	Runtime            RuntimeDecl `json:"runtime,omitempty"`
	ToolPolicy         []string    `json:"tool_policy,omitempty"`
	SmokeTests         []SmokeTest `json:"smoke_tests,omitempty"`
	RecommendedBundles []string    `json:"recommended_bundles,omitempty"`
	Trust              string      `json:"trust"`
}

type Prompt struct {
	PackageName string `json:"package_name"`
	Name        string `json:"name"`
	Path        string `json:"path"`
	Content     string `json:"content,omitempty"`
}

// Command describes a package-provided slash-command style workflow. Commands
// are declaration-only: the runtime may render their prompt or dispatch them
// through a safe GoDex executor, but package install never executes them.
type Command struct {
	PackageName        string   `json:"package_name" yaml:"-"`
	Name               string   `json:"name" yaml:"name"`
	Namespace          string   `json:"namespace,omitempty" yaml:"namespace,omitempty"`
	Description        string   `json:"description,omitempty" yaml:"description,omitempty"`
	Mode               string   `json:"mode,omitempty" yaml:"mode,omitempty"`
	PromptPath         string   `json:"prompt_path,omitempty" yaml:"prompt_path,omitempty"`
	Prompt             string   `json:"prompt,omitempty" yaml:"prompt,omitempty"`
	Aliases            []string `json:"aliases,omitempty" yaml:"aliases,omitempty"`
	Roles              []string `json:"roles,omitempty" yaml:"roles,omitempty"`
	WriteScope         []string `json:"write_scope,omitempty" yaml:"write_scope,omitempty"`
	Permissions        []string `json:"permissions,omitempty" yaml:"permissions,omitempty"`
	Capabilities       []string `json:"capabilities,omitempty" yaml:"capabilities,omitempty"`
	ToolPolicy         []string `json:"tool_policy,omitempty" yaml:"tool_policy,omitempty"`
	RecommendedBundles []string `json:"recommended_bundles,omitempty" yaml:"recommended_bundles,omitempty"`
	Path               string   `json:"path" yaml:"-"`
}

type SmokeRun struct {
	RunID           string    `json:"run_id"`
	PackageName     string    `json:"package_name"`
	SmokeName       string    `json:"smoke_name"`
	SessionID       string    `json:"session_id,omitempty"`
	Status          string    `json:"status"`
	Output          string    `json:"output,omitempty"`
	ArtifactPaths   []string  `json:"artifact_paths,omitempty"`
	PendingApproval bool      `json:"pending_approval,omitempty"`
	RequestID       string    `json:"request_id,omitempty"`
	Error           string    `json:"error,omitempty"`
	StartedAt       time.Time `json:"started_at,omitempty"`
	CompletedAt     time.Time `json:"completed_at,omitempty"`
}

type smokeRunRegistry struct {
	Runs []SmokeRun `json:"runs"`
}

// Role describes a named subagent role installed by a package.
type Role struct {
	PackageName    string   `json:"package_name" yaml:"-"`
	ID             string   `json:"id" yaml:"id"`
	Name           string   `json:"name" yaml:"name"`
	Description    string   `json:"description,omitempty" yaml:"description,omitempty"`
	BasePrompt     string   `json:"base_prompt,omitempty" yaml:"base_prompt,omitempty"`
	DefaultBundles []string `json:"default_bundles,omitempty" yaml:"default_bundles,omitempty"`
	Tools          []string `json:"tools,omitempty" yaml:"tools,omitempty"`
	WriteEnabled   bool     `json:"write_enabled,omitempty" yaml:"write_enabled,omitempty"`
	WriteScope     []string `json:"write_scope,omitempty" yaml:"write_scope,omitempty"`
	Capabilities   []string `json:"capabilities,omitempty" yaml:"capabilities,omitempty"`
	ToolPolicy     []string `json:"tool_policy,omitempty" yaml:"tool_policy,omitempty"`
	ModelHint      string   `json:"model_hint,omitempty" yaml:"model_hint,omitempty"`
	BudgetHint     string   `json:"budget_hint,omitempty" yaml:"budget_hint,omitempty"`
	Display        Display  `json:"display,omitempty" yaml:"display,omitempty"`
	Path           string   `json:"path" yaml:"-"`
}

type Display struct {
	Label string `json:"label,omitempty" yaml:"label,omitempty"`
	Color string `json:"color,omitempty" yaml:"color,omitempty"`
	Icon  string `json:"icon,omitempty" yaml:"icon,omitempty"`
}

type Manager struct {
	packagesDir string
	skillsDir   string
	now         func() time.Time
}

func NewManager(stateDir, skillsDir string) *Manager {
	packagesDir := filepath.Join(stateDir, "packages")
	if strings.TrimSpace(skillsDir) != "" {
		packagesDir = filepath.Join(filepath.Dir(skillsDir), "packages")
	}
	return &Manager{
		packagesDir: packagesDir,
		skillsDir:   skillsDir,
		now:         time.Now,
	}
}

func (m *Manager) List() ([]Entry, error) {
	registry, err := m.readRegistry()
	if err != nil {
		return nil, err
	}
	items := append([]Entry{}, registry.Packages...)
	sort.Slice(items, func(i, j int) bool {
		return items[i].Name < items[j].Name
	})
	return items, nil
}

func (m *Manager) Install(source string) (Entry, error) {
	source = strings.TrimSpace(source)
	if source == "" {
		return Entry{}, fmt.Errorf("missing package source")
	}
	stage, cleanup, err := prepareSource(source)
	if err != nil {
		return Entry{}, err
	}
	defer cleanup()
	return m.InstallPrepared(stage, source)
}

// InstallPrepared installs a package from an already materialized package
// directory while recording sourceLabel as the package source. It is intended
// for deterministic importers that generate a GoDex package before install.
//
// The install is transactional: the new content is staged under its own
// digest-addressed directory, dependency requirements are validated against the
// installed set (with this candidate replacing any same-name entry), and only
// after the registry is atomically swapped are stale directories for the same
// package name garbage-collected. A failure before the registry swap leaves the
// previously installed copy and its directory intact.
func (m *Manager) InstallPrepared(sourceRoot, sourceLabel string) (Entry, error) {
	sourceRoot = strings.TrimSpace(sourceRoot)
	if sourceRoot == "" {
		return Entry{}, fmt.Errorf("missing package source directory")
	}
	sourceLabel = strings.TrimSpace(sourceLabel)
	if sourceLabel == "" {
		sourceLabel = sourceRoot
	}
	manifest, err := readManifest(filepath.Join(sourceRoot, ManifestFileName))
	if err != nil {
		return Entry{}, err
	}
	if strings.TrimSpace(manifest.Name) == "" {
		return Entry{}, fmt.Errorf("package manifest missing name")
	}
	if _, err := parseRequires(manifest.Requires); err != nil {
		return Entry{}, fmt.Errorf("package %s: %v", manifest.Name, err)
	}
	if err := rejectSymlinks(sourceRoot); err != nil {
		return Entry{}, fmt.Errorf("package %s: %v", manifest.Name, err)
	}
	if err := validateResourcePaths(manifest.Resources, sourceRoot); err != nil {
		return Entry{}, fmt.Errorf("package %s: %v", manifest.Name, err)
	}
	if err := validateRuntimeDecl(manifest.Runtime, sourceRoot); err != nil {
		return Entry{}, fmt.Errorf("package %s: %v", manifest.Name, err)
	}
	digest, err := digestDir(sourceRoot)
	if err != nil {
		return Entry{}, err
	}
	short := digest
	if len(short) > 12 {
		short = short[:12]
	}
	target := filepath.Join(m.packagesDir, fmt.Sprintf("%s@%s", safeName(manifest.Name), short))

	// Build the candidate entry before touching anything on disk.
	candidate := Entry{
		Name:               manifest.Name,
		Version:            manifest.Version,
		Description:        manifest.Description,
		Source:             sourceLabel,
		Digest:             digest,
		Path:               target,
		InstalledAt:        m.now(),
		Resources:          manifest.Resources,
		App:                NormalizeAppManifest(manifest.App),
		Permissions:        append([]string{}, manifest.Permissions...),
		Capabilities:       cleanStringList(manifest.Capabilities),
		Provides:           cleanStringList(manifest.Provides),
		Requires:           cleanStringList(manifest.Requires),
		Runtime:            normalizeRuntimeDecl(manifest.Runtime, sourceRoot),
		ToolPolicy:         cleanStringList(manifest.ToolPolicy),
		SmokeTests:         normalizeSmokeTests(manifest.SmokeTests),
		RecommendedBundles: append([]string{}, manifest.RecommendedBundles...),
		Trust:              trustForSource(sourceLabel),
	}

	// Validate the complete proposed registry before staging. This catches not
	// only the candidate's requirements but consumers broken by an upgrade.
	registry, err := m.readRegistry()
	if err != nil {
		return Entry{}, err
	}
	proposed := append([]Entry{}, registry.Packages...)
	replacedCandidate := false
	for i := range proposed {
		if proposed[i].Name == candidate.Name {
			proposed[i] = candidate
			replacedCandidate = true
			break
		}
	}
	if !replacedCandidate {
		proposed = append(proposed, candidate)
	}
	if report := ValidateDependencyGraph(proposed); !report.Empty() {
		return Entry{}, fmt.Errorf("package %s dependency check failed: %s", manifest.Name, report.Error())
	}

	// Stage the new content into its digest-addressed directory. When the
	// digest is unchanged (reinstall of identical content) the target already
	// exists and is the live directory; remember that so a failed activation
	// can restore it instead of leaving the registry pointing at a gap.
	preExisting := false
	if _, statErr := os.Stat(target); statErr == nil {
		preExisting = true
	}
	if err := os.RemoveAll(target); err != nil {
		return Entry{}, err
	}
	if err := copyDir(sourceRoot, target); err != nil {
		_ = os.RemoveAll(target)
		return Entry{}, err
	}
	rollback := true
	defer func() {
		if !rollback {
			return
		}
		if preExisting {
			// Restore the previous live directory from the identical source
			// content (same digest) so the old registry entry stays valid.
			_ = os.RemoveAll(target)
			_ = copyDir(sourceRoot, target)
			return
		}
		_ = os.RemoveAll(target)
	}()

	// Snapshot the previous same-name entry for activation rollback.
	var oldEntry Entry
	hadOld := false
	for _, item := range registry.Packages {
		if item.Name == candidate.Name {
			oldEntry = item
			hadOld = true
			break
		}
	}

	// Atomic registry swap: replace or append the candidate entry. This is the
	// commit point: from here on the registry describes the new content.
	replaced := false
	for i, item := range registry.Packages {
		if item.Name == candidate.Name {
			registry.Packages[i] = candidate
			replaced = true
			break
		}
	}
	if !replaced {
		registry.Packages = append(registry.Packages, candidate)
	}
	if err := m.writeRegistry(registry); err != nil {
		return Entry{}, err
	}

	// Activate the new content: link skills from the staged directory. On
	// failure, roll back the registry swap and restore the previous skill
	// links from the still-intact old directory.
	if err := m.linkSkills(target, manifest); err != nil {
		_ = m.restoreRegistryEntry(oldEntry, hadOld, candidate.Name)
		if hadOld {
			_ = m.linkSkills(oldEntry.Path, manifestFromEntry(oldEntry))
		}
		return Entry{}, err
	}
	rollback = false

	// Garbage-collect stale directories for the same package name (e.g. the
	// digest-addressed directory of a previously installed version).
	_ = m.collectStaleDirs(candidate.Name, target)
	return candidate, nil
}

// restoreRegistryEntry rewrites the registry to point back at the previous
// same-name entry (used to roll back a failed activation).
func (m *Manager) restoreRegistryEntry(oldEntry Entry, hadOld bool, name string) error {
	registry, err := m.readRegistry()
	if err != nil {
		return err
	}
	replaced := false
	next := make([]Entry, 0, len(registry.Packages))
	for _, item := range registry.Packages {
		if item.Name != name {
			next = append(next, item)
			continue
		}
		if hadOld {
			next = append(next, oldEntry)
			replaced = true
		}
	}
	if hadOld && !replaced {
		next = append(next, oldEntry)
	}
	registry.Packages = next
	return m.writeRegistry(registry)
}

// manifestFromEntry reconstructs a Manifest from a registry entry so that
// skill links can be re-established from an installed directory.
func manifestFromEntry(entry Entry) Manifest {
	return Manifest{
		Name:      entry.Name,
		Version:   entry.Version,
		Resources: entry.Resources,
	}
}

// collectStaleDirs removes digest-addressed package directories for name other
// than keep (best effort, used after a successful registry swap).
func (m *Manager) collectStaleDirs(name, keep string) error {
	prefix := safeName(name) + "@"
	entries, err := os.ReadDir(m.packagesDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), prefix) {
			continue
		}
		path := filepath.Join(m.packagesDir, entry.Name())
		if filepath.Clean(path) == filepath.Clean(keep) {
			continue
		}
		_ = os.RemoveAll(path)
	}
	return nil
}

// parseRequires validates every entry of a requires list.
func parseRequires(items []string) ([]Requirement, error) {
	out := make([]Requirement, 0, len(items))
	for _, raw := range items {
		req, err := ParseRequirement(raw)
		if err != nil {
			return nil, err
		}
		out = append(out, req)
	}
	return out, nil
}

// validateRuntimeDecl checks an optional runtime declaration against the
// source directory. Only kind "wasm" is supported in the MVP; the module path
// must be package-relative and must exist.
func validateRuntimeDecl(decl RuntimeDecl, sourceRoot string) error {
	kind := strings.ToLower(strings.TrimSpace(decl.Kind))
	if kind == "" {
		return nil
	}
	if kind != "wasm" {
		return fmt.Errorf("unsupported runtime kind %q (only \"wasm\" is supported)", decl.Kind)
	}
	module := strings.TrimSpace(decl.Module)
	if module == "" {
		return fmt.Errorf("runtime kind %q requires a module path", kind)
	}
	path, err := resolvePackagePath(sourceRoot, module)
	if err != nil {
		return fmt.Errorf("runtime module %q: %w", module, err)
	}
	if info, err := os.Stat(path); err != nil {
		return fmt.Errorf("runtime module missing: %s", module)
	} else if info.IsDir() {
		return fmt.Errorf("runtime module is a directory: %s", module)
	}
	if abi := strings.TrimSpace(decl.ABI); abi != "" && abi != wasmABI {
		return fmt.Errorf("unsupported wasm ABI %q (expected %s)", abi, wasmABI)
	}
	return nil
}

// wasmABI is the ABI the wasm runtime implements.
const wasmABI = "godex:plugin@0.1"

// normalizeRuntimeDecl fills defaults for a validated runtime declaration.
func normalizeRuntimeDecl(decl RuntimeDecl, sourceRoot string) RuntimeDecl {
	if strings.ToLower(strings.TrimSpace(decl.Kind)) != "wasm" {
		return RuntimeDecl{}
	}
	decl.Kind = "wasm"
	decl.Module = filepath.ToSlash(filepath.Clean(strings.TrimSpace(decl.Module)))
	if strings.TrimSpace(decl.ABI) == "" {
		decl.ABI = wasmABI
	}
	_ = sourceRoot
	return decl
}

// RuntimeModulePath returns the absolute path of the package's WASM module
// ("" when the package has no runtime declaration).
func (m *Manager) RuntimeModulePath(item Entry) string {
	if strings.ToLower(strings.TrimSpace(item.Runtime.Kind)) != "wasm" {
		return ""
	}
	path, err := resolvePackagePath(item.Path, item.Runtime.Module)
	if err != nil {
		return ""
	}
	return path
}

func NormalizeAppManifest(app AppManifest) AppManifest {
	app.Kind = strings.ToLower(strings.TrimSpace(app.Kind))
	if strings.TrimSpace(app.ID) != "" {
		app.ID = safeName(app.ID)
	} else {
		app.ID = ""
	}
	app.Label = strings.TrimSpace(app.Label)
	if app.Config == nil {
		app.Config = nil
	}
	if app.Kind == "" && app.ID == "" && app.Label == "" && len(app.Config) == 0 {
		return AppManifest{}
	}
	if app.Kind == "" {
		app.Kind = "builtin"
	}
	return app
}

func AppManifestEmpty(app AppManifest) bool {
	return strings.TrimSpace(app.Kind) == "" && strings.TrimSpace(app.ID) == "" && strings.TrimSpace(app.Label) == "" && len(app.Config) == 0
}

func AppManifestIssues(app AppManifest) []string {
	app = NormalizeAppManifest(app)
	if AppManifestEmpty(app) {
		return nil
	}
	var issues []string
	if app.Kind != "builtin" {
		issues = append(issues, "unsupported package app kind: "+app.Kind)
	}
	if app.ID == "" {
		issues = append(issues, "package app missing id")
	}
	if app.Kind == "builtin" && app.ID != "" && !knownBuiltinPackageApp(app.ID) {
		issues = append(issues, "unknown builtin package app id: "+app.ID)
	}
	return issues
}

func knownBuiltinPackageApp(id string) bool {
	switch strings.ToLower(strings.TrimSpace(id)) {
	case "notes":
		return true
	default:
		return false
	}
}

func (m *Manager) Remove(name string) (Entry, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return Entry{}, fmt.Errorf("missing package name")
	}
	registry, err := m.readRegistry()
	if err != nil {
		return Entry{}, err
	}
	var removed Entry
	next := make([]Entry, 0, len(registry.Packages))
	for _, item := range registry.Packages {
		if item.Name == name {
			removed = item
			continue
		}
		next = append(next, item)
	}
	if removed.Name == "" {
		return Entry{}, fmt.Errorf("package not installed: %s", name)
	}
	// Validate the complete remaining graph, including capability requirements.
	if report := ValidateDependencyGraph(next); !report.Empty() {
		return Entry{}, fmt.Errorf("cannot remove package %s: %s", name, report.Error())
	}
	// Unlink skills in reverse registration order so uninstall cleanly
	// reverses the effects recorded at install time.
	skillPaths := append([]string{}, removed.Resources.Skills...)
	for i := len(skillPaths) - 1; i >= 0; i-- {
		_ = os.RemoveAll(filepath.Join(m.skillsDir, filepath.Base(filepath.Dir(skillPaths[i]))))
	}
	_ = os.RemoveAll(removed.Path)
	registry.Packages = next
	if err := m.writeRegistry(registry); err != nil {
		return Entry{}, err
	}
	return removed, nil
}

func (m *Manager) Reinstall(name string) (Entry, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return Entry{}, fmt.Errorf("missing package name")
	}
	registry, err := m.readRegistry()
	if err != nil {
		return Entry{}, err
	}
	for _, item := range registry.Packages {
		if item.Name == name {
			if strings.TrimSpace(item.Source) == "" {
				return Entry{}, fmt.Errorf("package %s has no recorded source", name)
			}
			return m.Install(item.Source)
		}
	}
	return Entry{}, fmt.Errorf("package not installed: %s", name)
}

func (m *Manager) Get(name string) (Entry, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return Entry{}, fmt.Errorf("missing package name")
	}
	items, err := m.List()
	if err != nil {
		return Entry{}, err
	}
	for _, item := range items {
		if item.Name == name {
			return item, nil
		}
	}
	return Entry{}, fmt.Errorf("package not installed: %s", name)
}

func (m *Manager) GetSmokeTest(packageName, smokeName string) (Entry, SmokeTest, error) {
	item, err := m.Get(packageName)
	if err != nil {
		return Entry{}, SmokeTest{}, err
	}
	smokeName = strings.TrimSpace(smokeName)
	for _, smoke := range item.SmokeTests {
		if smoke.Name == smokeName {
			return item, smoke, nil
		}
	}
	return Entry{}, SmokeTest{}, fmt.Errorf("package smoke test not found: %s/%s", packageName, smokeName)
}

func (m *Manager) RecordSmokeRun(run SmokeRun) error {
	run.PackageName = strings.TrimSpace(run.PackageName)
	run.SmokeName = strings.TrimSpace(run.SmokeName)
	if run.RunID == "" {
		run.RunID = newSmokeRunID(run.PackageName, run.SmokeName, m.now())
	}
	registry, err := m.readSmokeRunRegistry()
	if err != nil {
		return err
	}
	replaced := false
	for i := range registry.Runs {
		if registry.Runs[i].RunID == run.RunID {
			registry.Runs[i] = run
			replaced = true
			break
		}
	}
	if !replaced {
		registry.Runs = append(registry.Runs, run)
	}
	if len(registry.Runs) > 100 {
		registry.Runs = registry.Runs[len(registry.Runs)-100:]
	}
	return m.writeSmokeRunRegistry(registry)
}

func (m *Manager) ListSmokeRuns(packageName string) ([]SmokeRun, error) {
	registry, err := m.readSmokeRunRegistry()
	if err != nil {
		return nil, err
	}
	packageName = strings.TrimSpace(packageName)
	out := make([]SmokeRun, 0, len(registry.Runs))
	for _, run := range registry.Runs {
		if packageName == "" || run.PackageName == packageName {
			out = append(out, run)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].StartedAt.After(out[j].StartedAt)
	})
	return out, nil
}

func (m *Manager) ListPrompts(includeContent bool) ([]Prompt, error) {
	items, err := m.List()
	if err != nil {
		return nil, err
	}
	var prompts []Prompt
	for _, item := range items {
		for _, rel := range item.Resources.Prompts {
			path, err := resolvePackagePath(item.Path, rel)
			if err != nil {
				return nil, fmt.Errorf("package %s prompt %q: %w", item.Name, rel, err)
			}
			prompt := Prompt{PackageName: item.Name, Name: strings.TrimSuffix(filepath.Base(rel), filepath.Ext(rel)), Path: path}
			if includeContent {
				data, err := os.ReadFile(path)
				if err == nil {
					prompt.Content = string(data)
				}
			}
			prompts = append(prompts, prompt)
		}
	}
	sort.Slice(prompts, func(i, j int) bool {
		if prompts[i].PackageName == prompts[j].PackageName {
			return prompts[i].Name < prompts[j].Name
		}
		return prompts[i].PackageName < prompts[j].PackageName
	})
	return prompts, nil
}

// ListCommands returns structured command declarations from installed packages.
func (m *Manager) ListCommands(includeContent bool) ([]Command, error) {
	items, err := m.List()
	if err != nil {
		return nil, err
	}
	var commands []Command
	for _, item := range items {
		for _, rel := range item.Resources.Commands {
			command, err := readCommandResource(item, rel, includeContent)
			if err != nil {
				return nil, err
			}
			commands = append(commands, command)
		}
	}
	sort.Slice(commands, func(i, j int) bool {
		if commands[i].PackageName == commands[j].PackageName {
			if commands[i].Namespace == commands[j].Namespace {
				return commands[i].Name < commands[j].Name
			}
			return commands[i].Namespace < commands[j].Namespace
		}
		return commands[i].PackageName < commands[j].PackageName
	})
	return commands, nil
}

// ListRoles returns named subagent role declarations from installed packages.
func (m *Manager) ListRoles(includeContent bool) ([]Role, error) {
	items, err := m.List()
	if err != nil {
		return nil, err
	}
	var roles []Role
	for _, item := range items {
		for _, rel := range item.Resources.Roles {
			role, err := readRoleResource(item, rel, includeContent)
			if err != nil {
				return nil, err
			}
			roles = append(roles, role)
		}
	}
	sort.Slice(roles, func(i, j int) bool {
		if roles[i].PackageName == roles[j].PackageName {
			return roles[i].ID < roles[j].ID
		}
		return roles[i].PackageName < roles[j].PackageName
	})
	return roles, nil
}

// GetRole returns one installed named subagent role by id or display name.
func (m *Manager) GetRole(id string, includeContent bool) (Role, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return Role{}, fmt.Errorf("missing role id")
	}
	roles, err := m.ListRoles(includeContent)
	if err != nil {
		return Role{}, err
	}
	for _, role := range roles {
		if strings.EqualFold(role.ID, id) || strings.EqualFold(role.Name, id) {
			return role, nil
		}
	}
	return Role{}, fmt.Errorf("package role not found: %s", id)
}

func readCommandResource(item Entry, rel string, includeContent bool) (Command, error) {
	path := filepath.Join(item.Path, filepath.Clean(rel))
	command := Command{
		PackageName: item.Name,
		Namespace:   safeName(item.Name),
		Name:        resourceName(rel),
		Mode:        "prompt_only",
		Path:        path,
	}
	if isMarkdownResource(rel) {
		command.PromptPath = rel
		if includeContent {
			data, err := os.ReadFile(path)
			if err != nil {
				return Command{}, err
			}
			command.Prompt = strings.TrimSpace(string(data))
		}
		return command, nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return Command{}, err
	}
	decoder := yaml.NewDecoder(strings.NewReader(string(data)))
	decoder.KnownFields(true)
	if err := decoder.Decode(&command); err != nil {
		return Command{}, fmt.Errorf("read command resource %s: %w", rel, err)
	}
	command.PackageName = item.Name
	command.Path = path
	if strings.TrimSpace(command.Name) == "" {
		command.Name = resourceName(rel)
	}
	if strings.TrimSpace(command.Namespace) == "" {
		command.Namespace = safeName(item.Name)
	}
	if strings.TrimSpace(command.Mode) == "" {
		command.Mode = "prompt_only"
	}
	command.Aliases = cleanStringList(command.Aliases)
	command.Roles = cleanStringList(command.Roles)
	command.WriteScope = cleanStringList(command.WriteScope)
	command.Permissions = cleanStringList(command.Permissions)
	command.Capabilities = cleanStringList(command.Capabilities)
	command.ToolPolicy = cleanStringList(command.ToolPolicy)
	command.RecommendedBundles = cleanStringList(command.RecommendedBundles)
	command.PromptPath = strings.TrimSpace(command.PromptPath)
	command.Prompt = strings.TrimSpace(command.Prompt)
	if includeContent && command.Prompt == "" && command.PromptPath != "" {
		promptPath := filepath.Join(item.Path, filepath.Clean(command.PromptPath))
		promptData, err := os.ReadFile(promptPath)
		if err != nil {
			return Command{}, err
		}
		command.Prompt = strings.TrimSpace(string(promptData))
	}
	return command, nil
}

func readRoleResource(item Entry, rel string, includeContent bool) (Role, error) {
	path := filepath.Join(item.Path, filepath.Clean(rel))
	role := Role{
		PackageName: item.Name,
		ID:          safeName(item.Name) + ":" + resourceName(rel),
		Name:        resourceName(rel),
		Path:        path,
	}
	if isMarkdownResource(rel) {
		if includeContent {
			data, err := os.ReadFile(path)
			if err != nil {
				return Role{}, err
			}
			role.BasePrompt = strings.TrimSpace(string(data))
		}
		return role, nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return Role{}, err
	}
	decoder := yaml.NewDecoder(strings.NewReader(string(data)))
	decoder.KnownFields(true)
	if err := decoder.Decode(&role); err != nil {
		return Role{}, fmt.Errorf("read role resource %s: %w", rel, err)
	}
	role.PackageName = item.Name
	role.Path = path
	if strings.TrimSpace(role.ID) == "" {
		role.ID = safeName(item.Name) + ":" + resourceName(rel)
	}
	if strings.TrimSpace(role.Name) == "" {
		role.Name = resourceName(rel)
	}
	role.DefaultBundles = cleanStringList(role.DefaultBundles)
	role.Tools = cleanStringList(role.Tools)
	role.WriteScope = cleanStringList(role.WriteScope)
	role.Capabilities = cleanStringList(role.Capabilities)
	role.ToolPolicy = cleanStringList(role.ToolPolicy)
	role.ModelHint = strings.TrimSpace(role.ModelHint)
	role.BudgetHint = strings.TrimSpace(role.BudgetHint)
	role.BasePrompt = strings.TrimSpace(role.BasePrompt)
	if !includeContent {
		role.BasePrompt = ""
	}
	return role, nil
}

func resourceName(rel string) string {
	base := filepath.Base(filepath.Clean(rel))
	ext := filepath.Ext(base)
	name := strings.TrimSuffix(base, ext)
	return safeName(name)
}

func isMarkdownResource(rel string) bool {
	return strings.EqualFold(filepath.Ext(filepath.Clean(rel)), ".md")
}

func cleanStringList(items []string) []string {
	out := make([]string, 0, len(items))
	seen := map[string]struct{}{}
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		out = append(out, item)
	}
	return out
}

func (m *Manager) readRegistry() (Registry, error) {
	data, err := os.ReadFile(filepath.Join(m.packagesDir, registryFileName))
	if err != nil {
		if os.IsNotExist(err) {
			return Registry{}, nil
		}
		return Registry{}, err
	}
	var registry Registry
	if err := json.Unmarshal(data, &registry); err != nil {
		return Registry{}, err
	}
	return registry, nil
}

func (m *Manager) writeRegistry(registry Registry) error {
	return fsutil.WriteJSONAtomic(filepath.Join(m.packagesDir, registryFileName), registry, 0644)
}

func (m *Manager) readSmokeRunRegistry() (smokeRunRegistry, error) {
	data, err := os.ReadFile(filepath.Join(m.packagesDir, smokeRunsFileName))
	if err != nil {
		if os.IsNotExist(err) {
			return smokeRunRegistry{}, nil
		}
		return smokeRunRegistry{}, err
	}
	var registry smokeRunRegistry
	if err := json.Unmarshal(data, &registry); err != nil {
		return smokeRunRegistry{}, err
	}
	return registry, nil
}

func (m *Manager) writeSmokeRunRegistry(registry smokeRunRegistry) error {
	return fsutil.WriteJSONAtomic(filepath.Join(m.packagesDir, smokeRunsFileName), registry, 0644)
}

func (m *Manager) linkSkills(root string, manifest Manifest) error {
	for _, rel := range manifest.Resources.Skills {
		src := filepath.Join(root, filepath.Clean(rel))
		if filepath.Base(src) != "SKILL.md" {
			continue
		}
		name := filepath.Base(filepath.Dir(src))
		if name == "." || name == string(filepath.Separator) || name == "" {
			continue
		}
		dst := filepath.Join(m.skillsDir, name)
		if err := os.RemoveAll(dst); err != nil {
			return err
		}
		if err := os.MkdirAll(dst, 0755); err != nil {
			return err
		}
		if err := copyFile(src, filepath.Join(dst, "SKILL.md")); err != nil {
			return err
		}
	}
	return nil
}

func readManifest(path string) (Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Manifest{}, err
	}
	var manifest Manifest
	decoder := yaml.NewDecoder(strings.NewReader(string(data)))
	decoder.KnownFields(true)
	if err := decoder.Decode(&manifest); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

func normalizeSmokeTests(items []SmokeTest) []SmokeTest {
	out := make([]SmokeTest, 0, len(items))
	seen := map[string]struct{}{}
	for _, item := range items {
		item.Name = safeName(item.Name)
		item.Command = strings.TrimSpace(item.Command)
		item.WorkingDir = strings.TrimSpace(item.WorkingDir)
		item.RequiredPermissions = cleanStringList(item.RequiredPermissions)
		if item.TimeoutSeconds < 0 {
			item.TimeoutSeconds = 0
		}
		if item.Name == "" {
			item.Name = fmt.Sprintf("smoke-%d", len(out)+1)
		}
		if _, ok := seen[item.Name]; ok {
			item.Name = fmt.Sprintf("%s-%d", item.Name, len(out)+1)
		}
		seen[item.Name] = struct{}{}
		out = append(out, item)
	}
	return out
}

func SmokeQuickCheck(item Entry, smoke SmokeTest) []string {
	var issues []string
	if strings.TrimSpace(smoke.Name) == "" {
		issues = append(issues, "smoke missing name")
	}
	if strings.TrimSpace(smoke.Command) == "" {
		issues = append(issues, "smoke missing command")
	} else if err := tooling.ValidateShellCommand(smoke.Command); err != nil {
		issues = append(issues, "smoke command rejected by shell guard: "+err.Error())
	}
	if smoke.TimeoutSeconds < 0 {
		issues = append(issues, "smoke timeout_seconds must be non-negative")
	}
	for _, permission := range smoke.RequiredPermissions {
		if !knownPermission(permission) {
			issues = append(issues, "smoke unknown permission: "+permission)
		}
	}
	if wd := strings.TrimSpace(smoke.WorkingDir); wd != "" {
		if filepath.IsAbs(wd) {
			issues = append(issues, "smoke working_dir must be package-relative")
		} else {
			root, err := workspacefs.New(item.Path)
			if err != nil {
				issues = append(issues, fmt.Sprintf("smoke working_dir %s: %v", wd, err))
			} else if _, err := root.Stat(wd); err != nil {
				if os.IsNotExist(err) {
					issues = append(issues, "smoke working_dir missing: "+wd)
				} else if strings.Contains(err.Error(), "escapes workspace") {
					issues = append(issues, "smoke working_dir escapes package: "+wd)
				} else {
					issues = append(issues, fmt.Sprintf("smoke working_dir %s: %v", wd, err))
				}
			}
			if root != nil {
				_ = root.Close()
			}
		}
	}
	return issues
}

func newSmokeRunID(packageName, smokeName string, now time.Time) string {
	sum := sha256.Sum256([]byte(packageName + "\x00" + smokeName + "\x00" + now.Format(time.RFC3339Nano)))
	return "smoke_" + hex.EncodeToString(sum[:])[:12]
}

// NewSmokeRunID returns the stable ID shape used for one recorded smoke run.
func NewSmokeRunID(packageName, smokeName string, now time.Time) string {
	return newSmokeRunID(packageName, smokeName, now)
}

func prepareSource(source string) (string, func(), error) {
	if info, err := os.Stat(source); err == nil && info.IsDir() {
		return source, func() {}, nil
	}
	tmp, err := os.MkdirTemp("", "godex-package-*")
	if err != nil {
		return "", func() {}, err
	}
	cleanup := func() { _ = os.RemoveAll(tmp) }
	cloneURL := source
	if isOwnerRepo(source) {
		cloneURL = "https://github.com/" + source + ".git"
	}
	cmd := exec.Command("git", "clone", "--depth", "1", cloneURL, tmp)
	if out, err := cmd.CombinedOutput(); err != nil {
		cleanup()
		return "", func() {}, fmt.Errorf("clone package: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return tmp, cleanup, nil
}

func isOwnerRepo(source string) bool {
	parts := strings.Split(source, "/")
	return len(parts) == 2 && !strings.Contains(source, "://") && !strings.HasPrefix(source, ".")
}

func trustForSource(source string) string {
	if strings.Contains(source, "://") || isOwnerRepo(source) {
		return "remote_review"
	}
	return "local"
}

func resolvePackagePath(root, relative string) (string, error) {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	relative = strings.TrimSpace(relative)
	if relative == "" || filepath.IsAbs(relative) {
		return "", fmt.Errorf("path must be package-relative")
	}
	candidate := filepath.Join(rootAbs, filepath.Clean(filepath.FromSlash(relative)))
	rel, err := filepath.Rel(rootAbs, candidate)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path escapes package root")
	}
	resolvedRoot, err := filepath.EvalSymlinks(rootAbs)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		// Declarations may intentionally reference a resource that is reported as
		// missing by the quality checker. Lexical containment is already proven;
		// existing package trees are separately rejected if they contain symlinks.
		if os.IsNotExist(err) {
			return candidate, nil
		}
		return "", err
	}
	resolvedRel, err := filepath.Rel(resolvedRoot, resolved)
	if err != nil || resolvedRel == ".." || strings.HasPrefix(resolvedRel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path escapes package root through symbolic link")
	}
	return candidate, nil
}

func validateResourcePaths(resources Resources, root string) error {
	groups := [][]string{resources.Skills, resources.Prompts, resources.Commands, resources.Roles, resources.Docs, resources.Assets}
	for _, items := range groups {
		for _, item := range items {
			if _, err := resolvePackagePath(root, item); err != nil {
				return fmt.Errorf("resource %q: %w", item, err)
			}
		}
	}
	return nil
}

func rejectSymlinks(root string) error {
	return filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			rel, _ := filepath.Rel(root, path)
			return fmt.Errorf("symbolic links are not allowed in packages: %s", filepath.ToSlash(rel))
		}
		return nil
	})
}

func digestDir(root string) (string, error) {
	hash := sha256.New()
	var files []string
	if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if entry.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		files = append(files, path)
		return nil
	}); err != nil {
		return "", err
	}
	sort.Strings(files)
	for _, path := range files {
		rel, _ := filepath.Rel(root, path)
		hash.Write([]byte(filepath.ToSlash(rel)))
		file, err := os.Open(path)
		if err != nil {
			return "", err
		}
		_, copyErr := io.Copy(hash, file)
		closeErr := file.Close()
		if copyErr != nil {
			return "", copyErr
		}
		if closeErr != nil {
			return "", closeErr
		}
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func copyDir(src, dst string) error {
	return filepath.WalkDir(src, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("symbolic links are not allowed in packages: %s", filepath.ToSlash(rel))
		}
		if entry.IsDir() {
			if entry.Name() == ".git" {
				return filepath.SkipDir
			}
			return os.MkdirAll(target, 0755)
		}
		return copyFile(path, target)
	})
}

func copyFile(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

func safeName(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	replacer := strings.NewReplacer("/", "-", "\\", "-", " ", "-", ":", "-")
	value = replacer.Replace(value)
	if value == "" {
		return "package"
	}
	return value
}
