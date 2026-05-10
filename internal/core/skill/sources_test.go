package skill

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSourceCatalogMergesRemoteAndLocalSources(t *testing.T) {
	workspace := t.TempDir()
	skillsDir := filepath.Join(workspace, "skills")
	if err := os.MkdirAll(filepath.Join(skillsDir, "playwright-cli"), 0755); err != nil {
		t.Fatalf("mkdir installed skill: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillsDir, "playwright-cli", "SKILL.md"), []byte("installed"), 0644); err != nil {
		t.Fatalf("write installed skill: %v", err)
	}
	memoryData, err := json.Marshal(InstallMemory{
		Source:        "https://github.com/tim5wang/godex",
		SourceEntryID: "playwright-cli",
		SourceOrigin:  "curated",
		Trust:         "official",
		Version:       "v1.0.0",
		Categories:    []string{"browser", "automation"},
		InstalledAt:   time.Now().UTC().Format(time.RFC3339),
	})
	if err != nil {
		t.Fatalf("marshal install memory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillsDir, "playwright-cli", installMetadataFileName), memoryData, 0644); err != nil {
		t.Fatalf("write install metadata: %v", err)
	}

	indexServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"version": 1,
			"sources": []map[string]any{
				{
					"id":         "remote-browser",
					"name":       "browser-pro",
					"summary":    "Remote browser automation skill.",
					"source":     "https://github.com/acme/browser-skills",
					"skill_name": "browser-pro",
					"tags":       []string{"browser", "automation"},
					"categories": []string{"browser", "automation"},
					"trust":      "verified",
					"version":    "v2.4.1",
				},
			},
		})
	}))
	defer indexServer.Close()

	customDir := filepath.Join(workspace, ".godex")
	if err := os.MkdirAll(customDir, 0755); err != nil {
		t.Fatalf("mkdir .godex: %v", err)
	}
	customConfig := map[string]any{
		"indexes": []string{indexServer.URL},
		"sources": []map[string]any{
			{
				"id":         "local-docs",
				"name":       "docs-helper",
				"summary":    "Local docs skill.",
				"source":     "./skills/docs-helper",
				"skill_name": "docs-helper",
				"tags":       []string{"docs"},
			},
		},
	}
	data, err := json.Marshal(customConfig)
	if err != nil {
		t.Fatalf("marshal custom config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(customDir, "skill-sources.json"), data, 0644); err != nil {
		t.Fatalf("write custom source config: %v", err)
	}

	items, err := SourceCatalog(workspace, skillsDir)
	if err != nil {
		t.Fatalf("source catalog: %v", err)
	}

	if len(items) < 4 {
		t.Fatalf("expected merged source catalog, got %#v", items)
	}

	var sawRemote bool
	var sawLocal bool
	var sawInstalled bool
	for _, item := range items {
		switch item.ID {
		case "remote-browser":
			sawRemote = true
			if item.Origin != "remote" {
				t.Fatalf("expected remote origin, got %#v", item)
			}
			if item.Trust != "verified" || item.Version != "v2.4.1" || len(item.Categories) != 2 {
				t.Fatalf("expected remote metadata, got %#v", item)
			}
		case "local-docs":
			sawLocal = true
			if item.Origin != "local" {
				t.Fatalf("expected local origin, got %#v", item)
			}
		case "playwright-cli":
			if !item.Installed {
				t.Fatalf("expected installed curated skill, got %#v", item)
			}
			if !strings.Contains(item.InstalledPath, filepath.Join("skills", "playwright-cli")) {
				t.Fatalf("unexpected installed path: %#v", item)
			}
			if item.InstallMemory == nil || item.InstallMemory.Version != "v1.0.0" {
				t.Fatalf("expected install memory for installed skill, got %#v", item)
			}
			sawInstalled = true
		}
	}
	if !sawRemote || !sawLocal || !sawInstalled {
		t.Fatalf("missing expected entries remote=%v local=%v installed=%v catalog=%#v", sawRemote, sawLocal, sawInstalled, items)
	}
}

func TestSourceCatalogSupportsLegacyArrayConfig(t *testing.T) {
	workspace := t.TempDir()
	customDir := filepath.Join(workspace, ".godex")
	if err := os.MkdirAll(customDir, 0755); err != nil {
		t.Fatalf("mkdir .godex: %v", err)
	}
	data, err := json.Marshal([]map[string]any{
		{
			"name":       "legacy-source",
			"summary":    "Legacy config entry.",
			"source":     "./skills/legacy-source",
			"skill_name": "legacy-source",
		},
	})
	if err != nil {
		t.Fatalf("marshal legacy config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(customDir, "skill-sources.json"), data, 0644); err != nil {
		t.Fatalf("write legacy config: %v", err)
	}

	items, err := SourceCatalog(workspace, filepath.Join(workspace, "skills"))
	if err != nil {
		t.Fatalf("source catalog: %v", err)
	}

	var found bool
	for _, item := range items {
		if item.SkillName == "legacy-source" {
			found = true
			if item.Origin != "local" {
				t.Fatalf("expected local origin for legacy entry, got %#v", item)
			}
		}
	}
	if !found {
		t.Fatalf("expected legacy source entry in catalog: %#v", items)
	}
}

func TestSourceCatalogKeepsHealthyRemoteIndexesWhenOneFails(t *testing.T) {
	workspace := t.TempDir()
	skillsDir := filepath.Join(workspace, "skills")

	goodIndex := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"version": 1,
			"sources": []map[string]any{
				{
					"id":         "remote-review",
					"name":       "Review Helper Deluxe",
					"summary":    "Remote review skill.",
					"source":     "https://github.com/acme/review-skills",
					"skill_name": "review-helper",
				},
			},
		})
	}))
	defer goodIndex.Close()

	badIndex := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer badIndex.Close()

	customDir := filepath.Join(workspace, ".godex")
	if err := os.MkdirAll(customDir, 0755); err != nil {
		t.Fatalf("mkdir .godex: %v", err)
	}
	data, err := json.Marshal(map[string]any{
		"indexes": []string{goodIndex.URL, badIndex.URL},
	})
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(customDir, "skill-sources.json"), data, 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	items, err := SourceCatalog(workspace, skillsDir)
	if err != nil {
		t.Fatalf("source catalog: %v", err)
	}

	var foundRemote bool
	var warnings []string
	for _, item := range items {
		if item.ID == "remote-review" {
			foundRemote = true
		}
		warnings = append(warnings, item.Warnings...)
	}
	if !foundRemote {
		t.Fatalf("expected successful remote index entry to survive partial failure, got %#v", items)
	}
	if len(warnings) == 0 {
		t.Fatalf("expected remote index warning, got %#v", items)
	}
	joined := strings.Join(warnings, " ")
	if !strings.Contains(joined, badIndex.URL) || !strings.Contains(joined, "unexpected status 500 Internal Server Error") {
		t.Fatalf("expected warning to mention failed index, got %q", joined)
	}
}

func TestSearchSourceCatalogMergesSkillsHubResults(t *testing.T) {
	workspace := t.TempDir()
	home := t.TempDir()
	skillsDir := filepath.Join(home, "skills")

	searchServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("q"); got != "react agent" {
			t.Fatalf("expected query to be escaped and restored as %q, got %q", "react agent", got)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"skills": []map[string]any{
				{
					"id":       "vercel-labs/agent-skills/vercel-react-best-practices",
					"skillId":  "vercel-react-best-practices",
					"name":     "vercel-react-best-practices",
					"installs": 2413,
					"source":   "vercel-labs/agent-skills",
				},
				{
					"id":       "smithery/discord-bot",
					"skillId":  "discord-bot",
					"name":     "discord-bot",
					"installs": 91,
					"source":   "smithery.ai",
				},
			},
		})
	}))
	defer searchServer.Close()
	t.Setenv("SKILLS_API_URL", searchServer.URL)

	items, err := SearchSourceCatalog(workspace, skillsDir, "react agent")
	if err != nil {
		t.Fatalf("search source catalog: %v", err)
	}

	var installable SourceEntry
	var discoverOnly SourceEntry
	for _, item := range items {
		switch item.ID {
		case "skillsh:vercel-labs/agent-skills/vercel-react-best-practices":
			installable = item
		case "skillsh:smithery/discord-bot":
			discoverOnly = item
		}
	}

	if installable.ID == "" {
		t.Fatalf("expected installable skills.sh entry in %#v", items)
	}
	if installable.Origin != "skillsh" || installable.SkillName != "vercel-react-best-practices" || !installable.InstallSupported {
		t.Fatalf("unexpected installable skills.sh entry: %#v", installable)
	}
	if installable.InstallSource != "vercel-labs/agent-skills" || installable.InstallName != "vercel-react-best-practices" {
		t.Fatalf("expected install preview for installable entry, got %#v", installable)
	}
	if !strings.Contains(installable.Summary, "skills.sh") {
		t.Fatalf("expected skills.sh summary, got %#v", installable)
	}

	if discoverOnly.ID == "" {
		t.Fatalf("expected discover-only skills.sh entry in %#v", items)
	}
	if discoverOnly.InstallSupported {
		t.Fatalf("expected smithery-backed entry to be discover-only, got %#v", discoverOnly)
	}
	if joined := strings.Join(discoverOnly.Warnings, " "); !strings.Contains(joined, "cannot be installed by GoDex yet") {
		t.Fatalf("expected install warning for discover-only entry, got %q", joined)
	}
	if !strings.Contains(discoverOnly.InstallReason, "git repository") {
		t.Fatalf("expected install reason for discover-only entry, got %#v", discoverOnly)
	}

	if _, err := os.Stat(filepath.Join(home, "skills-hub-cache.json")); err != nil {
		t.Fatalf("expected skills hub cache under godex home: %v", err)
	}
	if _, err := os.Stat(filepath.Join(workspace, ".godex", "skills-hub-cache.json")); !os.IsNotExist(err) {
		t.Fatalf("expected no skills hub cache under workspace .godex, stat err=%v", err)
	}
}

func TestSourceEntryInstallSupportedNormalizesRepoSkillSource(t *testing.T) {
	entry := SourceEntry{
		Name:   "stock-trading",
		Source: "meo9rhsan3492-cell/cn-stock-sim/stock-trading",
	}
	if !sourceEntryInstallSupported(entry) {
		t.Fatalf("expected normalized repo/skill source to be installable, got %#v", entry)
	}
}

func TestSearchSourceCatalogFallsBackToCachedSkillsHubResults(t *testing.T) {
	workspace := t.TempDir()
	skillsDir := filepath.Join(workspace, "skills")

	var fail bool
	searchServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fail {
			http.Error(w, "boom", http.StatusBadGateway)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"skills": []map[string]any{
				{
					"id":       "vercel-labs/agent-skills/vercel-react-best-practices",
					"skillId":  "vercel-react-best-practices",
					"name":     "vercel-react-best-practices",
					"installs": 2413,
					"source":   "vercel-labs/agent-skills",
				},
			},
		})
	}))
	defer searchServer.Close()
	t.Setenv("SKILLS_API_URL", searchServer.URL)

	first, err := SearchSourceCatalog(workspace, skillsDir, "react agent")
	if err != nil {
		t.Fatalf("prime cached search: %v", err)
	}
	if len(first) == 0 {
		t.Fatalf("expected primed skills.sh search results, got %#v", first)
	}

	fail = true
	second, err := SearchSourceCatalog(workspace, skillsDir, "react agent")
	if err != nil {
		t.Fatalf("fallback cached search: %v", err)
	}

	var found bool
	for _, item := range second {
		if item.ID != "skillsh:vercel-labs/agent-skills/vercel-react-best-practices" {
			continue
		}
		found = true
		if joined := strings.Join(item.Warnings, " "); !strings.Contains(joined, "using cached skills.sh results") {
			t.Fatalf("expected cached-result warning, got %#v", item)
		}
	}
	if !found {
		t.Fatalf("expected cached skills.sh entry in %#v", second)
	}
}

func TestTrendingSourceCatalogParsesSkillsHubLeaderboard(t *testing.T) {
	workspace := t.TempDir()
	skillsDir := filepath.Join(workspace, "skills")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/trending" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`<!doctype html><html><body>
<a href="/vercel-labs/skills/find-skills"><div><span>1</span><h3>find-skills</h3><p>vercel-labs/skills</p><span>1.2M</span></div></a>
<a href="/anthropics/skills/frontend-design"><div><span>2</span><h3>frontend-design</h3><p>anthropics/skills</p><span>315.3K</span></div></a>
</body></html>`))
	}))
	defer server.Close()
	t.Setenv("SKILLS_API_URL", server.URL)

	items, err := TrendingSourceCatalog(workspace, skillsDir)
	if err != nil {
		t.Fatalf("trending source catalog: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("expected two trending items, got %#v", items)
	}
	if items[0].SkillName != "find-skills" || items[0].Installs != 1_200_000 {
		t.Fatalf("unexpected top trending item: %#v", items[0])
	}
	if items[1].SkillName != "frontend-design" || items[1].Installs != 315_300 {
		t.Fatalf("unexpected second trending item: %#v", items[1])
	}
	if items[0].Origin != "skillsh" || items[0].InstallSource != "vercel-labs/skills" {
		t.Fatalf("expected normalized skills.sh entry, got %#v", items[0])
	}
}
