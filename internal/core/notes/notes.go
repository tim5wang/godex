package notes

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Note struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	Summary   string    `json:"summary,omitempty"`
	Tags      []string  `json:"tags,omitempty"`
	Content   string    `json:"content"`
	Path      string    `json:"path"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type SaveInput struct {
	ID      string   `json:"id,omitempty"`
	Title   string   `json:"title"`
	Summary string   `json:"summary,omitempty"`
	Tags    []string `json:"tags,omitempty"`
	Content string   `json:"content"`
}

type SearchOptions struct {
	Query string
	Tag   string
}

type Manager struct {
	dir string
	now func() time.Time
}

type noteFrontmatter struct {
	Title     string   `yaml:"title,omitempty"`
	Summary   string   `yaml:"summary,omitempty"`
	Tags      []string `yaml:"tags,omitempty"`
	CreatedAt string   `yaml:"created_at,omitempty"`
	UpdatedAt string   `yaml:"updated_at,omitempty"`
}

func NewManager(dir string) *Manager {
	return &Manager{dir: dir, now: time.Now}
}

func (m *Manager) List(opts SearchOptions) ([]Note, error) {
	if err := os.MkdirAll(m.dir, 0755); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(m.dir)
	if err != nil {
		return nil, err
	}
	query := strings.ToLower(strings.TrimSpace(opts.Query))
	tag := strings.ToLower(strings.TrimSpace(opts.Tag))
	out := make([]Note, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.EqualFold(filepath.Ext(entry.Name()), ".md") {
			continue
		}
		note, err := m.readFile(filepath.Join(m.dir, entry.Name()))
		if err != nil {
			return nil, err
		}
		if query != "" && !strings.Contains(noteSearchHaystack(note), query) {
			continue
		}
		if tag != "" && !noteHasTag(note, tag) {
			continue
		}
		out = append(out, note)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].UpdatedAt.Equal(out[j].UpdatedAt) {
			return out[i].Title < out[j].Title
		}
		return out[i].UpdatedAt.After(out[j].UpdatedAt)
	})
	return out, nil
}

func (m *Manager) Get(id string) (Note, error) {
	path, err := m.pathForID(id)
	if err != nil {
		return Note{}, err
	}
	return m.readFile(path)
}

func (m *Manager) Save(input SaveInput) (Note, error) {
	title := strings.TrimSpace(input.Title)
	content := strings.TrimSpace(input.Content)
	if title == "" {
		title = titleFromContent(content)
	}
	if title == "" {
		return Note{}, fmt.Errorf("note title is required")
	}
	if content == "" {
		content = "# " + title
	}
	id := safeID(input.ID)
	if id == "" {
		id = uniqueNoteID(m.dir, title)
	}
	path := filepath.Join(m.dir, id+".md")
	now := m.now().UTC()
	createdAt := now
	if existing, err := m.readFile(path); err == nil && !existing.CreatedAt.IsZero() {
		createdAt = existing.CreatedAt
	}
	note := Note{
		ID:        id,
		Title:     title,
		Summary:   strings.TrimSpace(input.Summary),
		Tags:      cleanTags(input.Tags),
		Content:   content,
		Path:      path,
		CreatedAt: createdAt,
		UpdatedAt: now,
	}
	if err := os.MkdirAll(m.dir, 0755); err != nil {
		return Note{}, err
	}
	if err := os.WriteFile(path, []byte(renderNote(note)), 0644); err != nil {
		return Note{}, err
	}
	return note, nil
}

func (m *Manager) Delete(id string) (Note, error) {
	note, err := m.Get(id)
	if err != nil {
		return Note{}, err
	}
	if err := os.Remove(note.Path); err != nil {
		return Note{}, err
	}
	return note, nil
}

func (m *Manager) readFile(path string) (Note, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Note{}, err
	}
	frontmatter, body, err := splitFrontmatter(string(data))
	if err != nil {
		return Note{}, err
	}
	id := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	info, _ := os.Stat(path)
	updated := parseTime(frontmatter.UpdatedAt)
	if updated.IsZero() && info != nil {
		updated = info.ModTime().UTC()
	}
	created := parseTime(frontmatter.CreatedAt)
	if created.IsZero() {
		created = updated
	}
	title := strings.TrimSpace(frontmatter.Title)
	if title == "" {
		title = titleFromContent(body)
	}
	if title == "" {
		title = id
	}
	return Note{
		ID:        id,
		Title:     title,
		Summary:   strings.TrimSpace(frontmatter.Summary),
		Tags:      cleanTags(frontmatter.Tags),
		Content:   strings.TrimSpace(body),
		Path:      path,
		CreatedAt: created,
		UpdatedAt: updated,
	}, nil
}

func (m *Manager) pathForID(id string) (string, error) {
	id = safeID(id)
	if id == "" {
		return "", fmt.Errorf("note id is required")
	}
	path := filepath.Join(m.dir, id+".md")
	cleanDir, err := filepath.Abs(m.dir)
	if err != nil {
		return "", err
	}
	cleanPath, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(cleanDir, cleanPath)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("note id escapes notes directory")
	}
	return path, nil
}

func splitFrontmatter(raw string) (noteFrontmatter, string, error) {
	lines := strings.Split(raw, "\n")
	if len(lines) < 3 || strings.TrimSpace(lines[0]) != "---" {
		return noteFrontmatter{}, raw, nil
	}
	end := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			end = i
			break
		}
	}
	if end == -1 {
		return noteFrontmatter{}, raw, nil
	}
	var meta noteFrontmatter
	if err := yaml.Unmarshal([]byte(strings.Join(lines[1:end], "\n")), &meta); err != nil {
		return noteFrontmatter{}, "", err
	}
	return meta, strings.Join(lines[end+1:], "\n"), nil
}

func renderNote(note Note) string {
	meta := noteFrontmatter{
		Title:     note.Title,
		Summary:   note.Summary,
		Tags:      note.Tags,
		CreatedAt: note.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt: note.UpdatedAt.UTC().Format(time.RFC3339),
	}
	data, _ := yaml.Marshal(meta)
	return "---\n" + strings.TrimSpace(string(data)) + "\n---\n\n" + strings.TrimSpace(note.Content) + "\n"
}

func titleFromContent(content string) string {
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		return strings.TrimSpace(strings.TrimPrefix(line, "#"))
	}
	return ""
}

func noteSearchHaystack(note Note) string {
	return strings.ToLower(note.Title + "\n" + note.Summary + "\n" + strings.Join(note.Tags, " ") + "\n" + note.Content)
}

func noteHasTag(note Note, tag string) bool {
	for _, item := range note.Tags {
		if strings.EqualFold(strings.TrimSpace(item), tag) {
			return true
		}
	}
	return false
}

func cleanTags(items []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(items))
	for _, item := range items {
		for _, part := range strings.Split(item, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			key := strings.ToLower(part)
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			out = append(out, part)
		}
	}
	sort.Strings(out)
	return out
}

func uniqueNoteID(dir, title string) string {
	base := safeID(title)
	if base == "" {
		sum := sha1.Sum([]byte(title + time.Now().Format(time.RFC3339Nano)))
		base = "note-" + hex.EncodeToString(sum[:])[:8]
	}
	id := base
	for i := 2; ; i++ {
		if _, err := os.Stat(filepath.Join(dir, id+".md")); os.IsNotExist(err) {
			return id
		}
		id = fmt.Sprintf("%s-%d", base, i)
	}
}

func safeID(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var b strings.Builder
	dash := false
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			dash = false
			continue
		}
		if r == '-' || r == '_' || r == ' ' || r == '.' {
			if !dash && b.Len() > 0 {
				b.WriteByte('-')
				dash = true
			}
		}
	}
	out := strings.Trim(b.String(), "-")
	if len(out) > 80 {
		out = strings.Trim(out[:80], "-")
	}
	return out
}

func parseTime(value string) time.Time {
	if strings.TrimSpace(value) == "" {
		return time.Time{}
	}
	parsed, err := time.Parse(time.RFC3339, strings.TrimSpace(value))
	if err != nil {
		return time.Time{}
	}
	return parsed
}
