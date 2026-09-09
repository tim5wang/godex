// Package selfdocs gives godex a built-in, code-derived knowledge base about
// its own features ("让 godex 更了解它自己").
//
// Feature knowledge is declared as `// godex-feature: <id>` comment blocks in
// Go source files — the comment sits next to the code it describes, so it
// drifts with the implementation instead of diverging from it. A go:generate
// extractor (gen/main.go) scans the tree and emits features_gen.go, which is
// compiled into the binary. At runtime, tools and the CLI can list, look up,
// and search these features without reading docs/ or the source tree.
//
// # Comment syntax
//
//	// godex-feature: longtask
//	// 长任务韧性：Ralph-style LongTask story loop、动态并行 DAG、auto-repair。
//	// 入口：godex longtask、Web Task Center、longtask 工具
//	// 文档：docs/roadmap、README
//
// The marker line is `// godex-feature: <id>` (kebab-case, unique). Following
// comment lines become the description. Optional key lines are supported:
//
//	入口： comma-separated entry points
//	文档： comma-separated doc paths
//
// Regenerate after editing markers: `go generate ./internal/selfdocs/...`.

//go:generate go run ./gen
package selfdocs

// Feature is one self-knowledge entry extracted from source comments.
type Feature struct {
	ID          string   `json:"id"`
	Title       string   `json:"title,omitempty"`
	Description string   `json:"description"`
	EntryPoints []string `json:"entry_points,omitempty"`
	Docs        []string `json:"docs,omitempty"`
	Location    string   `json:"location"` // source file:line of the marker
}

// All returns every extracted feature, sorted by ID.
func All() []Feature {
	return generatedFeatures
}

// Get returns the feature with the given id.
func Get(id string) (Feature, bool) {
	for _, f := range generatedFeatures {
		if f.ID == id {
			return f, true
		}
	}
	return Feature{}, false
}

// Search returns features whose id, description, entry points, or doc paths
// contain the query (case-insensitive substring match).
func Search(q string) []Feature {
	if q == "" {
		return nil
	}
	lower := toLower(q)
	var out []Feature
	for _, f := range generatedFeatures {
		if containsFold(f.ID, lower) || containsFold(f.Description, lower) ||
			containsFold(f.Title, lower) || containsFold(sliceJoin(f.EntryPoints), lower) ||
			containsFold(sliceJoin(f.Docs), lower) {
			out = append(out, f)
		}
	}
	return out
}

func toLower(s string) string {
	b := []byte(s)
	for i := range b {
		if b[i] >= 'A' && b[i] <= 'Z' {
			b[i] += 'a' - 'A'
		}
	}
	return string(b)
}

func containsFold(haystack, lowerNeedle string) bool {
	lowerHay := toLower(haystack)
	return len(lowerNeedle) > 0 && indexOf(lowerHay, lowerNeedle) >= 0
}

func indexOf(s, sub string) int {
	if sub == "" {
		return 0
	}
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func sliceJoin(items []string) string {
	out := ""
	for i, s := range items {
		if i > 0 {
			out += " "
		}
		out += s
	}
	return out
}
