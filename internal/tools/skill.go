package tools

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/tim5wang/godex/internal/core/skill"
)

// SkillActivation describes the result of activating a skill core.
type SkillActivation struct {
	ID                 string              `json:"id"`
	Name               string              `json:"name"`
	Status             string              `json:"status"`
	Description        string              `json:"description,omitempty"`
	LoadedSections     []string            `json:"loaded_sections,omitempty"`
	AvailableSections  []string            `json:"available_sections,omitempty"`
	RecommendedBundles []string            `json:"recommended_bundles,omitempty"`
	Compatibility      skill.Compatibility `json:"compatibility"`
	SkillKind          string              `json:"skill_kind,omitempty"`
	SuiteID            string              `json:"suite_id,omitempty"`
	ChildSkillCount    int                 `json:"child_skill_count,omitempty"`
	ChildSkillIDs      []string            `json:"child_skill_ids,omitempty"`
	ChildSkillHint     string              `json:"child_skill_hint,omitempty"`
}

// SkillExpansion describes the result of loading additional skill sections.
type SkillExpansion struct {
	ID                 string              `json:"id"`
	Name               string              `json:"name"`
	Status             string              `json:"status"`
	ExpandedSections   []string            `json:"expanded_sections,omitempty"`
	LoadedSections     []string            `json:"loaded_sections,omitempty"`
	AvailableSections  []string            `json:"available_sections,omitempty"`
	RecommendedBundles []string            `json:"recommended_bundles,omitempty"`
	Compatibility      skill.Compatibility `json:"compatibility"`
}

// SkillInstallResult describes a newly installed skill source.
type SkillInstallResult struct {
	ID                 string               `json:"id"`
	Name               string               `json:"name"`
	Status             string               `json:"status"`
	Source             string               `json:"source"`
	SourceOrigin       string               `json:"source_origin,omitempty"`
	Trust              string               `json:"trust,omitempty"`
	Version            string               `json:"version,omitempty"`
	Categories         []string             `json:"categories,omitempty"`
	InstalledPath      string               `json:"installed_path"`
	Description        string               `json:"description,omitempty"`
	Sections           []string             `json:"sections,omitempty"`
	RecommendedBundles []string             `json:"recommended_bundles,omitempty"`
	Compatibility      skill.Compatibility  `json:"compatibility"`
	Warnings           []string             `json:"warnings,omitempty"`
	InstallMemory      *skill.InstallMemory `json:"install_memory,omitempty"`
}

// SkillRemoveResult describes a removed skill source.
type SkillRemoveResult struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Status      string `json:"status"`
	RemovedPath string `json:"removed_path"`
	WasActive   bool   `json:"was_active,omitempty"`
}

// SkillSourceEntry describes one curated skill source.
type SkillSourceEntry struct {
	ID               string               `json:"id"`
	Name             string               `json:"name"`
	Summary          string               `json:"summary"`
	Source           string               `json:"source"`
	SkillName        string               `json:"skill_name,omitempty"`
	Tags             []string             `json:"tags,omitempty"`
	Categories       []string             `json:"categories,omitempty"`
	Version          string               `json:"version,omitempty"`
	Trust            string               `json:"trust,omitempty"`
	Origin           string               `json:"origin,omitempty"`
	Installs         int                  `json:"installs,omitempty"`
	Warnings         []string             `json:"warnings,omitempty"`
	InstallSupported bool                 `json:"install_supported"`
	InstallSource    string               `json:"install_source,omitempty"`
	InstallName      string               `json:"install_name,omitempty"`
	InstallReason    string               `json:"install_reason,omitempty"`
	Installed        bool                 `json:"installed"`
	InstalledPath    string               `json:"installed_path,omitempty"`
	InstallMemory    *skill.InstallMemory `json:"install_memory,omitempty"`
}

// SkillRuntime is the agent-facing skill session interface used by skill tools.
type SkillRuntime interface {
	ActivateSkill(name string) (SkillActivation, error)
	ExpandSkill(name string, sections []string) (SkillExpansion, error)
	ListSkills() ([]skill.CatalogEntry, error)
	ListSkillSources() ([]SkillSourceEntry, error)
	SearchSkillSources(query string) ([]SkillSourceEntry, error)
	GetSkill(name string) (skill.CatalogEntry, error)
	ActiveSkills() ([]SkillActivation, error)
	UnloadSkill(name string) (SkillActivation, error)
	InstallSkill(source, name string) (SkillInstallResult, error)
	RemoveSkill(name string) (SkillRemoveResult, error)
}

type skillNameArgs struct {
	Name string `json:"name"`
}

type listSkillsArgs struct {
	Query          string `json:"query,omitempty"`
	Suite          string `json:"suite,omitempty"`
	Offset         int    `json:"offset,omitempty"`
	Limit          int    `json:"limit,omitempty"`
	IncludeDetails bool   `json:"include_details,omitempty"`
}

type expandSkillArgs struct {
	Name     string   `json:"name"`
	Sections []string `json:"sections"`
}

type listSkillSourcesArgs struct {
	Query string `json:"query,omitempty"`
}

type installSkillArgs struct {
	Source string `json:"source"`
	Name   string `json:"name,omitempty"`
}

// NewLoadSkillTool creates a new load skill tool.
func NewLoadSkillTool(runtime SkillRuntime) Tool {
	return NewTypedTool(NewToolSpec("load_skill", "Activate a skill core by name for future turns", map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"name": map[string]string{"type": "string"},
		},
		"required": []string{"name"},
	}, nil), func(ctx context.Context, args skillNameArgs) (ToolResult, error) {
		_ = ctx
		if strings.TrimSpace(args.Name) == "" {
			return ToolResult{}, fmt.Errorf("missing name argument")
		}
		result, err := runtime.ActivateSkill(args.Name)
		if err != nil {
			return ToolResult{}, err
		}
		return ToolResult{Structured: result}, nil
	})
}

// NewListSkillsTool creates a new skill catalog tool.
func NewListSkillsTool(runtime SkillRuntime) Tool {
	return NewTypedTool(NewToolSpec("list_skills", "List, search, or page through available skills. Catalog entries may include child_skill_ids; use suite plus offset/limit to inspect child details on demand, then load_skill with an exact id.", map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"query":           map[string]string{"type": "string", "description": "Optional text search query matched against id, name, description, categories, sections, and usage hints."},
			"suite":           map[string]string{"type": "string", "description": "Optional suite id from child_skill_ids, for example a root skill id. Returns nested skills in that suite."},
			"offset":          map[string]string{"type": "integer", "description": "Zero-based result offset for pagination."},
			"limit":           map[string]string{"type": "integer", "description": "Maximum results to return. Defaults to 20, max 100."},
			"include_details": map[string]string{"type": "boolean", "description": "Return full catalog entries for matches instead of compact metadata."},
		},
	}, map[string]string{"q": "query"}), func(ctx context.Context, args listSkillsArgs) (ToolResult, error) {
		_ = ctx
		items, err := runtime.ListSkills()
		if err != nil {
			return ToolResult{}, err
		}
		return ToolResult{Structured: buildSkillListResult(items, args)}, nil
	})
}

type compactSkillEntry struct {
	ID                 string              `json:"id"`
	Name               string              `json:"name,omitempty"`
	Description        string              `json:"description,omitempty"`
	RecommendedBundles []string            `json:"recommended_bundles,omitempty"`
	Compatibility      skill.Compatibility `json:"compatibility,omitempty"`
	ChildSkillCount    int                 `json:"child_skill_count,omitempty"`
	ChildSkillIDs      []string            `json:"child_skill_ids,omitempty"`
	ChildSkillHint     string              `json:"child_skill_hint,omitempty"`
}

type detailedSkillEntry struct {
	skill.CatalogEntry
	ChildSkillCount int      `json:"child_skill_count,omitempty"`
	ChildSkillIDs   []string `json:"child_skill_ids,omitempty"`
	ChildSkillHint  string   `json:"child_skill_hint,omitempty"`
}

type compactSkillSuite struct {
	ID       string `json:"id"`
	Count    int    `json:"count"`
	ListHint string `json:"list_hint,omitempty"`
}

type skillListPagination struct {
	Offset     int  `json:"offset"`
	Limit      int  `json:"limit"`
	Total      int  `json:"total"`
	NextOffset int  `json:"next_offset,omitempty"`
	HasMore    bool `json:"has_more"`
}

func buildSkillListResult(items []skill.CatalogEntry, args listSkillsArgs) map[string]interface{} {
	offset, limit := normalizeSkillListPaging(args.Offset, args.Limit)
	query := strings.TrimSpace(args.Query)
	suite := strings.TrimSpace(args.Suite)
	regular, suites := splitToolSkillSuites(items)
	if suite != "" {
		children := append([]skill.CatalogEntry{}, suites[suite]...)
		sortSkillCatalogByID(children)
		page, pagination := paginateSkillCatalog(children, offset, limit)
		result := map[string]interface{}{
			"mode":       "suite",
			"suite":      suite,
			"count":      len(page),
			"pagination": pagination,
			"hint":       "Use load_skill with an exact id. Increase offset to continue paging this suite.",
		}
		if args.IncludeDetails {
			result["skills"] = page
			return result
		}
		result["skills"] = compactSkills(page, true)
		return result
	}
	if query != "" {
		matches := rankSkillMatches(items, query)
		page, pagination := paginateSkillCatalog(matches, offset, limit)
		result := map[string]interface{}{
			"mode":       "search",
			"query":      query,
			"count":      len(page),
			"pagination": pagination,
			"hint":       "Use load_skill with an exact id. Increase offset for more matches, or use suite to page a nested skill suite.",
		}
		if args.IncludeDetails {
			result["skills"] = page
			return result
		}
		result["skills"] = compactSkills(page, true)
		return result
	}

	sortSkillCatalogByID(regular)
	page, pagination := paginateSkillCatalog(regular, offset, limit)
	suiteIDs := make([]string, 0, len(suites))
	for suiteID := range suites {
		suiteIDs = append(suiteIDs, suiteID)
	}
	sort.Strings(suiteIDs)
	suiteResults := make([]compactSkillSuite, 0, len(suiteIDs))
	for _, suiteID := range suiteIDs {
		suiteResults = append(suiteResults, compactSkillSuite{
			ID:       suiteID,
			Count:    len(suites[suiteID]),
			ListHint: fmt.Sprintf(`Call list_skills with suite=%q, offset=0, and limit=%d to inspect nested skills.`, suiteID, limit),
		})
	}
	return map[string]interface{}{
		"mode":       "catalog",
		"skills":     catalogSkillEntries(page, suites, args.IncludeDetails),
		"suites":     suiteResults,
		"pagination": pagination,
		"hint":       "Root skills with child_skill_ids have nested skills. Use suite=<id> with offset/limit to browse child details, query for text search, then load_skill with an exact id.",
	}
}

func normalizeSkillListPaging(offset, limit int) (int, int) {
	if offset < 0 {
		offset = 0
	}
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	return offset, limit
}

func paginateSkillCatalog(items []skill.CatalogEntry, offset, limit int) ([]skill.CatalogEntry, skillListPagination) {
	total := len(items)
	if offset > total {
		offset = total
	}
	end := offset + limit
	if end > total {
		end = total
	}
	pagination := skillListPagination{
		Offset:  offset,
		Limit:   limit,
		Total:   total,
		HasMore: end < total,
	}
	if pagination.HasMore {
		pagination.NextOffset = end
	}
	return items[offset:end], pagination
}

func splitToolSkillSuites(items []skill.CatalogEntry) ([]skill.CatalogEntry, map[string][]skill.CatalogEntry) {
	regular := make([]skill.CatalogEntry, 0, len(items))
	suites := map[string][]skill.CatalogEntry{}
	for _, item := range items {
		id := skillCatalogID(item)
		if slash := strings.Index(id, "/"); slash > 0 {
			suites[id[:slash]] = append(suites[id[:slash]], item)
			continue
		}
		regular = append(regular, item)
	}
	return regular, suites
}

func compactSkills(items []skill.CatalogEntry, includeDescription bool) []compactSkillEntry {
	compact := make([]compactSkillEntry, 0, len(items))
	for _, item := range items {
		compact = append(compact, compactSkill(item, includeDescription))
	}
	return compact
}

func catalogSkillEntries(items []skill.CatalogEntry, suites map[string][]skill.CatalogEntry, includeDetails bool) interface{} {
	if includeDetails {
		detailed := make([]detailedSkillEntry, 0, len(items))
		for _, item := range items {
			entry := detailedSkillEntry{CatalogEntry: item}
			id := skillCatalogID(item)
			if children := suites[id]; len(children) > 0 {
				entry.ChildSkillCount = len(children)
				entry.ChildSkillIDs = childSkillIDs(children)
				entry.ChildSkillHint = childSkillHint(id, len(children), len(entry.ChildSkillIDs))
			}
			detailed = append(detailed, entry)
		}
		return detailed
	}
	compact := make([]compactSkillEntry, 0, len(items))
	for _, item := range items {
		entry := compactSkill(item, true)
		id := skillCatalogID(item)
		if children := suites[id]; len(children) > 0 {
			entry.ChildSkillCount = len(children)
			entry.ChildSkillIDs = childSkillIDs(children)
			entry.ChildSkillHint = childSkillHint(id, len(children), len(entry.ChildSkillIDs))
		}
		compact = append(compact, entry)
	}
	return compact
}

func compactSkill(item skill.CatalogEntry, includeDescription bool) compactSkillEntry {
	id := skillCatalogID(item)
	entry := compactSkillEntry{
		ID:                 id,
		Name:               strings.TrimSpace(item.Name),
		RecommendedBundles: append([]string{}, item.RecommendedBundles...),
		Compatibility:      item.Compatibility,
	}
	if includeDescription {
		entry.Description = strings.TrimSpace(item.Description)
	}
	return entry
}

const maxChildSkillIDs = 80

func childSkillIDs(items []skill.CatalogEntry) []string {
	ids := make([]string, 0, len(items))
	for _, item := range items {
		if id := skillCatalogID(item); id != "" {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	if len(ids) > maxChildSkillIDs {
		return ids[:maxChildSkillIDs]
	}
	return ids
}

func childSkillHint(suiteID string, total, returned int) string {
	hint := fmt.Sprintf("Use list_skills with suite=%q and offset/limit to inspect child details, then load_skill with an exact child id.", suiteID)
	if total > returned {
		hint += fmt.Sprintf(" Showing %d of %d child ids.", returned, total)
	}
	return hint
}

func skillCatalogID(item skill.CatalogEntry) string {
	id := strings.TrimSpace(item.ID)
	if id == "" {
		id = strings.TrimSpace(item.Name)
	}
	return id
}

func rankSkillMatches(items []skill.CatalogEntry, query string) []skill.CatalogEntry {
	terms := skillSearchTerms(query)
	type scored struct {
		item  skill.CatalogEntry
		score int
	}
	scoredItems := make([]scored, 0, len(items))
	for _, item := range items {
		score := scoreSkillMatch(item, terms)
		if score <= 0 {
			continue
		}
		scoredItems = append(scoredItems, scored{item: item, score: score})
	}
	sort.SliceStable(scoredItems, func(i, j int) bool {
		if scoredItems[i].score == scoredItems[j].score {
			return strings.TrimSpace(scoredItems[i].item.ID) < strings.TrimSpace(scoredItems[j].item.ID)
		}
		return scoredItems[i].score > scoredItems[j].score
	})
	result := make([]skill.CatalogEntry, 0, len(scoredItems))
	for _, item := range scoredItems {
		result = append(result, item.item)
	}
	return result
}

func skillSearchTerms(query string) []string {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return nil
	}
	terms := []string{query}
	fields := strings.FieldsFunc(query, func(r rune) bool {
		return r == '/' || r == '-' || r == '_' || r == ',' || r == ';' || r == ':' || r == '"' || r == '\'' || r == ' '
	})
	for _, field := range fields {
		if field = strings.TrimSpace(field); field != "" {
			terms = append(terms, field)
		}
	}
	return uniqueSkillSearchTerms(terms)
}

func scoreSkillMatch(item skill.CatalogEntry, terms []string) int {
	id := strings.ToLower(strings.TrimSpace(item.ID))
	name := strings.ToLower(strings.TrimSpace(item.Name))
	desc := strings.ToLower(strings.TrimSpace(item.Description))
	haystack := strings.Join([]string{
		id,
		name,
		desc,
		strings.ToLower(strings.Join(item.WhenToUse, " ")),
		strings.ToLower(strings.Join(item.Categories, " ")),
		strings.ToLower(strings.Join(item.Sections, " ")),
	}, " ")
	score := 0
	for _, term := range terms {
		switch {
		case term == "":
			continue
		case id == term || name == term:
			score += 100
		case strings.Contains(id, term):
			score += 25
		case strings.Contains(name, term):
			score += 18
		case strings.Contains(desc, term):
			score += 8
		case strings.Contains(haystack, term):
			score += 4
		}
	}
	return score
}

func sortSkillCatalogByID(items []skill.CatalogEntry) {
	sort.SliceStable(items, func(i, j int) bool {
		return strings.TrimSpace(items[i].ID) < strings.TrimSpace(items[j].ID)
	})
}

func uniqueSkillSearchTerms(values []string) []string {
	seen := make(map[string]bool, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		result = append(result, value)
	}
	return result
}

// NewExpandSkillTool creates a new skill expansion tool.
func NewExpandSkillTool(runtime SkillRuntime) Tool {
	return NewTypedTool(NewToolSpec("expand_skill", "Load additional sections for an active skill", map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"name": map[string]string{"type": "string"},
			"sections": map[string]interface{}{
				"type": "array",
				"items": map[string]string{
					"type": "string",
				},
			},
		},
		"required": []string{"name", "sections"},
	}, nil), func(ctx context.Context, args expandSkillArgs) (ToolResult, error) {
		_ = ctx
		if strings.TrimSpace(args.Name) == "" {
			return ToolResult{}, fmt.Errorf("missing name argument")
		}
		if len(args.Sections) == 0 {
			return ToolResult{}, fmt.Errorf("missing sections argument")
		}
		result, err := runtime.ExpandSkill(args.Name, args.Sections)
		if err != nil {
			return ToolResult{}, err
		}
		return ToolResult{Structured: result}, nil
	})
}

// NewUnloadSkillTool creates a new unload skill tool.
func NewUnloadSkillTool(runtime SkillRuntime) Tool {
	return NewTypedTool(NewToolSpec("unload_skill", "Remove an active skill from the current session", map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"name": map[string]string{"type": "string"},
		},
		"required": []string{"name"},
	}, nil), func(ctx context.Context, args skillNameArgs) (ToolResult, error) {
		_ = ctx
		if strings.TrimSpace(args.Name) == "" {
			return ToolResult{}, fmt.Errorf("missing name argument")
		}
		result, err := runtime.UnloadSkill(args.Name)
		if err != nil {
			return ToolResult{}, err
		}
		return ToolResult{Structured: result}, nil
	})
}

// NewListSkillSourcesTool creates a new skill sources tool.
func NewListSkillSourcesTool(runtime SkillRuntime) Tool {
	return NewTypedTool(NewToolSpec("list_skill_sources", "List curated skill install sources for this workspace, or search skills.sh when query is provided", map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"query": map[string]string{"type": "string"},
		},
	}, map[string]string{"q": "query"}), func(ctx context.Context, args listSkillSourcesArgs) (ToolResult, error) {
		_ = ctx
		if strings.TrimSpace(args.Query) != "" {
			items, err := runtime.SearchSkillSources(args.Query)
			if err != nil {
				return ToolResult{}, err
			}
			return ToolResult{Structured: map[string]interface{}{"sources": items}}, nil
		}
		items, err := runtime.ListSkillSources()
		if err != nil {
			return ToolResult{}, err
		}
		return ToolResult{Structured: map[string]interface{}{"sources": items}}, nil
	})
}

// NewInstallSkillTool creates a new install skill tool.
func NewInstallSkillTool(runtime SkillRuntime) Tool {
	return NewTypedTool(NewToolSpec("install_skill", "Install a skill from a local path, GitHub repo URL, or owner/repo source", map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"source": map[string]string{"type": "string"},
			"name":   map[string]string{"type": "string"},
		},
		"required": []string{"source"},
	}, nil), func(ctx context.Context, args installSkillArgs) (ToolResult, error) {
		_ = ctx
		if strings.TrimSpace(args.Source) == "" {
			return ToolResult{}, fmt.Errorf("missing source argument")
		}
		result, err := runtime.InstallSkill(args.Source, args.Name)
		if err != nil {
			return ToolResult{}, err
		}
		return ToolResult{Structured: result}, nil
	})
}
