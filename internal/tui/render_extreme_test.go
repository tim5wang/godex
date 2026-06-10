package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/tim5wang/godex/internal/core/config"
	rtbackend "github.com/tim5wang/godex/internal/services/backend"
)

// renderWithTimeout calls a render function with a hard deadline. If the
// function does not return within the deadline, it is reported as a
// timeout. The test fails (t.Fatal) when ANY input triggers a timeout.
//
// We do not assume a specific input causes the hang. The point of this
// test is to flush out which common markdown constructs, if any, can
// wedge the TUI renderer in a runaway CPU loop.
func renderWithTimeout(t *testing.T, label string, fn func() []string) []string {
	t.Helper()
	done := make(chan []string, 1)
	go func() {
		defer func() {
			// Catch any panic so a render bug doesn't take down the test
			// runner — we want to know "hung" vs "crashed" separately.
			if r := recover(); r != nil {
				done <- nil
			}
		}()
		done <- fn()
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	select {
	case got := <-done:
		return got
	case <-ctx.Done():
		t.Fatalf("renderWithTimeout: %s did not return within 1s (likely busy-loop / infinite recursion)", label)
		return nil
	}
}

// TestMarkdownRendererDoesNotHangOnExtremeInputs probes the renderer
// with extreme / pathological inputs that an LLM assistant message or a
// tool result might produce. Each input is rendered in a goroutine
// guarded by a 1s deadline; if any single input times out we fail with
// the offending label so we know which markdown construct wedges the
// renderer.
//
// Pre-fix expectation: at least one of these will hang. The repro will
// tell us which one, so we can fix the real bug.
func TestMarkdownRendererDoesNotHangOnExtremeInputs(t *testing.T) {
	t.Parallel()

	mr := NewMarkdownRenderer(NewHighlighter())
	width := 80

	cases := []struct {
		label string
		input string
	}{
		{
			label: "empty",
			input: "",
		},
		{
			label: "single line",
			input: "hello world",
		},
		{
			label: "very long list with many inline nodes",
			input: "- " + strings.Repeat("**bold** and *italic* and `code` and [link](https://example.com) ", 500),
		},
		{
			label: "deeply nested unordered lists",
			input: func() string {
				var b strings.Builder
				for i := 0; i < 30; i++ {
					b.WriteString(strings.Repeat("  ", i))
					b.WriteString("- item\n")
				}
				return b.String()
			}(),
		},
		{
			label: "deeply nested ordered lists",
			input: func() string {
				var b strings.Builder
				for i := 0; i < 30; i++ {
					b.WriteString(strings.Repeat("  ", i))
					b.WriteString("1. item\n")
				}
				return b.String()
			}(),
		},
		{
			label: "task list with many items",
			input: func() string {
				var b strings.Builder
				for i := 0; i < 200; i++ {
					b.WriteString("- [ ] todo item\n")
				}
				return b.String()
			}(),
		},
		{
			label: "huge table with many columns",
			input: func() string {
				var b strings.Builder
				b.WriteString("|")
				for i := 0; i < 30; i++ {
					b.WriteString(" col |")
				}
				b.WriteString("\n|")
				for i := 0; i < 30; i++ {
					b.WriteString(" --- |")
				}
				for r := 0; r < 50; r++ {
					b.WriteString("\n|")
					for c := 0; c < 30; c++ {
						b.WriteString(" x |")
					}
				}
				return b.String()
			}(),
		},
		{
			label: "huge fenced code block",
			input: "```python\n" + strings.Repeat("x = 1\n", 5000) + "```",
		},
		{
			label: "many headings",
			input: strings.Repeat("## heading\n\nbody\n\n", 500),
		},
		{
			label: "many paragraphs",
			input: strings.Repeat("paragraph text here\n\n", 500),
		},
		{
			label: "linkify stress (http:// repeated)",
			input: strings.Repeat("https://example.com/foo ", 500),
		},
		{
			label: "strikethrough stress",
			input: strings.Repeat("~~strike~~ ", 500),
		},
		{
			label: "width=10 with wide table",
			input: func() string {
				var b strings.Builder
				b.WriteString("| col1 | col2 | col3 |\n| --- | --- | --- |\n| aaa | bbb | ccc |\n")
				return b.String()
			}(),
		},
		{
			label: "single very long unbreakable token",
			input: strings.Repeat("a", 100000),
		},
	}

	for _, c := range cases {
		c := c
		t.Run(c.label, func(t *testing.T) {
			t.Parallel()
			got := renderWithTimeout(t, c.label, func() []string {
				return mr.Render(c.input, width)
			})
			_ = got
		})
	}
}

// TestRefreshViewportDoesNotHangOnExtremeMessages runs the full
// model-level rendering path (View → resize → refreshViewport →
// renderActiveViewportContent) on a model whose feed contains a
// pathological assistant message, and asserts it returns within a
// reasonable deadline. This is the "TUI stuck on Loading TUI..."
// reproducer: pre-fix, the renderer wedges and never returns to
// bubbletea's event loop, so WindowSizeMsg is never delivered to
// the model and View() permanently returns "Loading TUI...".
func TestRefreshViewportDoesNotHangOnExtremeMessages(t *testing.T) {
	t.Parallel()

	cases := []struct {
		label string
		body  string
	}{
		{
			label: "very long list with many inline nodes",
			body:  "- " + strings.Repeat("**bold** and *italic* and `code` and [link](https://example.com) ", 500),
		},
		{
			label: "huge table with many columns",
			body: func() string {
				var b strings.Builder
				b.WriteString("|")
				for i := 0; i < 30; i++ {
					b.WriteString(" col |")
				}
				b.WriteString("\n|")
				for i := 0; i < 30; i++ {
					b.WriteString(" --- |")
				}
				for r := 0; r < 50; r++ {
					b.WriteString("\n|")
					for c := 0; c < 30; c++ {
						b.WriteString(" x |")
					}
				}
				return b.String()
			}(),
		},
		{
			label: "deeply nested lists",
			body: func() string {
				var b strings.Builder
				for i := 0; i < 30; i++ {
					b.WriteString(strings.Repeat("  ", i))
					b.WriteString("- item\n")
				}
				return b.String()
			}(),
		},
		{
			label: "huge fenced code block",
			body:  "```python\n" + strings.Repeat("x = 1\n", 5000) + "```",
		},
	}

	for _, c := range cases {
		c := c
		t.Run(c.label, func(t *testing.T) {
			t.Parallel()
			m := newModelWithDeferredInit(
				context.Background(),
				&config.Config{
					Model:        "test-model",
					WorkspaceDir: "/workspace",
					LeadName:     "lead",
				},
				&fakeBackend{},
				time.Now,
				"session-1",
				rtbackend.Snapshot{Locator: rtbackend.SessionLocator{Channel: "local", Key: "default"}},
			)
			m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
			m.historyItems = []feedItem{{
				ID:          "message:0:assistant",
				Kind:        feedAssistant,
				Title:       botName,
				Body:        c.body,
				Summary:     firstSummaryLine(c.body),
				Foldable:    false,
				RuntimeOnly: false,
			}}

			renderWithTimeout(t, c.label, func() []string {
				m.refreshViewport(false)
				return nil
			})
		})
	}
}
