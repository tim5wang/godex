package retrieval

import (
	"path"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Catalog is a host-filtered view. Its identities, aliases and relationships
// must already be limited to the caller's desktop, applicability and live proof.
type Catalog struct {
	Entities  []CatalogEntity
	Relations []CatalogRelation
	Groups    map[string]string // document id -> applicability group
}
type CatalogEntity struct {
	ID, Kind, Name, Group string
	Aliases               []string
}
type CatalogRelation struct {
	ID, FromID, ToID, Kind, FactID string
	FactVersion                    int
}
type EntityHit struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Kind        string `json:"kind"`
	Via         string `json:"via,omitempty"`
	RelationID  string `json:"relation_id,omitempty"`
	FactID      string `json:"evidence_fact_id,omitempty"`
	FactVersion int    `json:"evidence_fact_version,omitempty"`
}
type CatalogMention struct{ Kind, Name string }

var catalogFile = regexp.MustCompile(`(?:[A-Za-z0-9_./-]+\.)+(?:go|py|ts|tsx|js|jsx|md|json|yaml|yml|rs|java|sql|sh)\b`)

// DiscoverEntities records literal technical mentions, not assertions that a
// path exists or that a named function/tool is executable. Domain names and
// equivalences are explicit catalog metadata, not guessed by a model.
func DiscoverEntities(text string) []CatalogMention {
	seen := map[string]bool{}
	out := []CatalogMention{}
	add := func(kind, name string) {
		if len(out) >= 32 {
			return
		}
		name = strings.Trim(name, "`\"'(),;[]{}")
		if name == "" || len([]rune(name)) > 180 {
			return
		}
		key := kind + "\x00" + name
		if !seen[key] {
			seen[key] = true
			out = append(out, CatalogMention{kind, name})
		}
	}
	for _, name := range catalogFile.FindAllString(text, -1) {
		add("file", path.Clean(name))
	}
	for _, name := range entityToken.FindAllString(text, -1) {
		if strings.Contains(name, "_") || mixedCase(name) && strings.ToUpper(name) != name {
			if !strings.ContainsAny(name, "/:-") && !strings.Contains(name, ".") {
				add("symbol", name)
			}
		}
	}
	return out
}

// EntityNameKey preserves separators; a_b, ab and a.b remain different names.
func EntityNameKey(name string) string {
	return strings.ToLower(strings.Join(strings.Fields(strings.TrimSpace(name)), " "))
}
func EntityNameMatches(text, name string) bool {
	text = EntityNameKey(text)
	name = EntityNameKey(name)
	if name == "" {
		return false
	}
	han := strings.ContainsFunc(name, func(r rune) bool { return unicode.Is(unicode.Han, r) })
	for offset := 0; offset < len(text); {
		at := strings.Index(text[offset:], name)
		if at < 0 {
			return false
		}
		at += offset
		end := at + len(name)
		if han || ((at == 0 || !entityBoundary(text[:at], true)) && (end == len(text) || !entityBoundary(text[end:], false))) {
			return true
		}
		offset = end
	}
	return false
}
func entityBoundary(text string, last bool) bool {
	var r rune
	if last {
		r, _ = utf8.DecodeLastRuneInString(text)
	} else {
		r, _ = utf8.DecodeRuneInString(text)
	}
	return r < 128 && (unicode.IsLetter(r) || unicode.IsDigit(r) || strings.ContainsRune("_./:-", r))
}

type catalogPlan struct {
	catalog   Catalog
	resolved  map[string]bool
	anchored  map[string]bool
	related   map[string][]CatalogRelation
	ambiguous []string
	names     map[string]CatalogEntity
}

func planCatalog(query string, catalog Catalog) catalogPlan {
	p := catalogPlan{catalog: catalog, resolved: map[string]bool{}, anchored: map[string]bool{}, related: map[string][]CatalogRelation{}, names: map[string]CatalogEntity{}}
	lookups := map[string][]string{}
	for _, e := range catalog.Entities {
		p.names[e.ID] = e
		for _, name := range append([]string{e.Name}, e.Aliases...) {
			key := EntityNameKey(name)
			if key != "" && EntityNameMatches(query, name) {
				lookups[key] = append(lookups[key], e.ID)
			}
		}
	}
	for name, ids := range lookups {
		unique := map[string]bool{}
		for _, id := range ids {
			unique[id] = true
		}
		if len(unique) > 1 {
			p.ambiguous = append(p.ambiguous, name)
			continue
		}
		for id := range unique {
			p.resolved[id] = true
			e := p.names[id]
			if strings.ContainsAny(name, "_./:") && (e.Kind != "file" || strings.Contains(e.Name, "/")) {
				p.anchored[id] = true
			}
		}
	}
	sort.Strings(p.ambiguous)
	for _, r := range catalog.Relations {
		from, fromOK := p.names[r.FromID]
		to, toOK := p.names[r.ToID]
		if !fromOK || !toOK || from.Group != to.Group {
			continue
		}
		if p.resolved[r.FromID] {
			p.related[r.ToID] = append(p.related[r.ToID], r)
		}
		if p.resolved[r.ToID] {
			p.related[r.FromID] = append(p.related[r.FromID], r)
		}
	}
	return p
}
func (p catalogPlan) match(d Document) (direct, related []EntityHit) {
	group := p.catalog.Groups[d.ID]
	if group == "" {
		return nil, nil
	}
	text := d.Title + "\n" + d.Text + "\n" + strings.Join(d.Entities, " ")
	ids := map[string]bool{}
	for id := range p.resolved {
		ids[id] = true
	}
	for id := range p.related {
		ids[id] = true
	}
	for id := range ids {
		e := p.names[id]
		if e.Group != group {
			continue
		}
		matched := false
		for _, name := range append([]string{e.Name}, e.Aliases...) {
			if EntityNameMatches(text, name) {
				matched = true
				break
			}
		}
		if !matched {
			continue
		}
		if p.resolved[e.ID] {
			direct = append(direct, EntityHit{ID: e.ID, Name: e.Name, Kind: e.Kind})
			continue
		}
		if rs := p.related[e.ID]; len(rs) > 0 {
			r := rs[0]
			via := r.FromID
			if via == e.ID {
				via = r.ToID
			}
			related = append(related, EntityHit{ID: e.ID, Name: e.Name, Kind: e.Kind, Via: via, RelationID: r.ID, FactID: r.FactID, FactVersion: r.FactVersion})
		}
	}
	sort.Slice(direct, func(i, j int) bool { return direct[i].ID < direct[j].ID })
	sort.Slice(related, func(i, j int) bool { return related[i].ID < related[j].ID })
	return direct, related
}
