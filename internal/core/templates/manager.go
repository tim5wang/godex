package templates

import (
	"embed"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	pkgregistry "github.com/tim5wang/godex/internal/core/packages"
	"gopkg.in/yaml.v3"
)

//go:embed builtin/*.yaml
var builtinFS embed.FS

// Manager resolves and stores agent templates.
//
// Layout under the state directory:
//
//	<stateDir>/agent-templates/user/*.yaml   user-defined, CRUD-able
//
// Builtin templates ship embedded in the binary (read-only); package roles
// are derived read-only from the installed package registry at List/Get time.
type Manager struct {
	stateDir string
	skills   string
}

// NewManager creates a template manager rooted at the service state dir.
// skillsDir is used to validate template skill references.
func NewManager(stateDir, skillsDir string) *Manager {
	return &Manager{stateDir: stateDir, skills: skillsDir}
}

func (m *Manager) userDir() string {
	return filepath.Join(m.stateDir, "agent-templates", "user")
}

// List returns all templates: builtin, user (overriding same-ID builtin),
// then package-derived read-only templates, sorted by ID.
func (m *Manager) List() ([]AgentTemplate, error) {
	out := map[string]AgentTemplate{}

	entries, err := builtinFS.ReadDir("builtin")
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".yaml") {
			continue
		}
		t, err := decodeTemplateFS("builtin/" + name)
		if err != nil {
			return nil, fmt.Errorf("decode builtin template %s: %w", name, err)
		}
		t.Source = SourceBuiltin
		out[t.ID] = t
	}

	userEntries, err := os.ReadDir(m.userDir())
	if err == nil {
		for _, e := range userEntries {
			name := e.Name()
			if !strings.HasSuffix(name, ".yaml") || e.IsDir() {
				continue
			}
			t, err := decodeTemplateFile(filepath.Join(m.userDir(), name))
			if err != nil {
				return nil, fmt.Errorf("decode user template %s: %w", name, err)
			}
			t.Source = SourceUser
			out[t.ID] = t
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}

	// Package roles surface as derived read-only templates (pkg:<pkg>/<role>).
	// Registry errors are non-fatal: an unreadable package dir must not break
	// the template listing.
	if roles, err := pkgregistry.NewManager(m.stateDir, m.skills).ListRoles(false); err == nil {
		for _, role := range roles {
			t := roleToTemplate(role)
			if t.ID != "" {
				out[t.ID] = t
			}
		}
	}

	ids := make([]string, 0, len(out))
	for id := range out {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	list := make([]AgentTemplate, 0, len(ids))
	for _, id := range ids {
		list = append(list, out[id])
	}
	return list, nil
}

// Get resolves a single template by ID, searching user, builtin, then
// package-derived templates.
func (m *Manager) Get(id string) (AgentTemplate, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return AgentTemplate{}, fmt.Errorf("template id is empty")
	}
	list, err := m.List()
	if err != nil {
		return AgentTemplate{}, err
	}
	for _, t := range list {
		if t.ID == id {
			return t, nil
		}
	}
	return AgentTemplate{}, fmt.Errorf("template %q not found", id)
}

// Save writes a user template. IDs that collide with builtin or package
// templates are rejected: user templates must not shadow read-only sources.
func (m *Manager) Save(t AgentTemplate) error {
	t.ID = sanitizeID(t.ID)
	if t.ID == "" {
		return fmt.Errorf("template id is required")
	}
	if existing, err := m.Get(t.ID); err == nil && existing.IsBuiltIn() {
		return fmt.Errorf("template id %q collides with a read-only template", t.ID)
	}
	if err := os.MkdirAll(m.userDir(), 0o755); err != nil {
		return err
	}
	data, err := yaml.Marshal(&t)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(m.userDir(), t.ID+".yaml"), data, 0o644)
}

// Delete removes a user template. Builtin and package templates are rejected.
func (m *Manager) Delete(id string) error {
	id = sanitizeID(id)
	path := filepath.Join(m.userDir(), id+".yaml")
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("template %q not found or not user-defined", id)
	}
	return os.Remove(path)
}

// Resolve fetches a template and normalizes it for runtime use (source
// marker, lowercased profile). Skill reference validation is best-effort:
// missing skills are dropped with a warning list so a stale reference never
// breaks session creation.
func (m *Manager) Resolve(id string) (AgentTemplate, []string, error) {
	t, err := m.Get(id)
	if err != nil {
		return AgentTemplate{}, nil, err
	}
	if t.Profile != "" {
		t.Profile = strings.ToLower(strings.TrimSpace(t.Profile))
		if t.Profile != ProfileGeneral && t.Profile != ProfileCoding {
			t.Profile = ""
		}
	}
	t, warnings := m.filterSkills(t)
	return t, warnings, nil
}

// filterSkills drops skill names that are not installed under the skills
// dir, returning the updated template and warnings for the dropped ones.
func (m *Manager) filterSkills(t AgentTemplate) (AgentTemplate, []string) {
	if len(t.Skills) == 0 || m.skills == "" {
		return t, nil
	}
	kept := make([]string, 0, len(t.Skills))
	var warnings []string
	for _, s := range t.Skills {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if _, err := os.Stat(filepath.Join(m.skills, s)); err != nil {
			warnings = append(warnings, "skill not installed: "+s)
			continue
		}
		kept = append(kept, s)
	}
	t.Skills = kept
	return t, warnings
}

// roleToTemplate derives a read-only template from an installed package role
// (design §4.3). Display.Icon emoji maps onto Avatar; otherwise the UI falls
// back to the initial-letter avatar with Display.Color.
func roleToTemplate(role pkgregistry.Role) AgentTemplate {
	if strings.TrimSpace(role.ID) == "" || strings.TrimSpace(role.PackageName) == "" {
		return AgentTemplate{}
	}
	return AgentTemplate{
		ID:           PackageTemplateID(role.PackageName, role.ID),
		Name:         firstNonEmpty(role.Name, role.ID),
		Description:  role.Description,
		Avatar:       role.Display.Icon,
		Color:        role.Display.Color,
		Bundles:      role.DefaultBundles,
		Tools:        role.Tools,
		WriteEnabled: role.WriteEnabled,
		WriteScope:   role.WriteScope,
		BasePrompt:   role.BasePrompt,
		ModelHint:    role.ModelHint,
		BudgetHint:   role.BudgetHint,
		Source:       SourcePackage,
	}
}

// PackageTemplateID is the ID convention for package-derived templates.
func PackageTemplateID(pkg, role string) string {
	return "pkg:" + pkg + "/" + role
}

func decodeTemplateFS(name string) (AgentTemplate, error) {
	data, err := builtinFS.ReadFile(name)
	if err != nil {
		return AgentTemplate{}, err
	}
	var t AgentTemplate
	if err := yaml.Unmarshal(data, &t); err != nil {
		return AgentTemplate{}, err
	}
	return t, nil
}

func decodeTemplateFile(path string) (AgentTemplate, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return AgentTemplate{}, err
	}
	var t AgentTemplate
	if err := yaml.Unmarshal(data, &t); err != nil {
		return AgentTemplate{}, err
	}
	return t, nil
}

// sanitizeID restricts user template IDs to filesystem-safe characters.
func sanitizeID(id string) string {
	id = strings.TrimSpace(strings.ToLower(id))
	var b strings.Builder
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	return strings.Trim(b.String(), "-.")
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
