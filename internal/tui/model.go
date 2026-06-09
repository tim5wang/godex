package tui

import (
	"context"
	"io"
	"time"

	"github.com/atotto/clipboard"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	"github.com/charmbracelet/lipgloss"

	"github.com/tim5wang/godex/internal/agent"
	"github.com/tim5wang/godex/internal/core/config"
	"github.com/tim5wang/godex/internal/domain/events"
	"github.com/tim5wang/godex/internal/domain/message"
	rtbackend "github.com/tim5wang/godex/internal/services/backend"
	"github.com/tim5wang/godex/internal/services/commands"
	"github.com/tim5wang/godex/internal/tools"
)

const botName = "GoDex"

// Backend is the runtime backend surface the TUI needs.
type Backend interface {
	OpenSession(context.Context, rtbackend.SessionLocator) (*rtbackend.OpenedSession, error)
	Snapshot(context.Context, string) (rtbackend.Snapshot, error)
	ContextSummary(context.Context, string) (tools.ContextInspection, error)
	ListLongTasks(context.Context, string) ([]agent.LongTaskView, error)
	GetLongTask(context.Context, string, string) (agent.LongTaskView, error)
	LookupLongTask(context.Context, string, string, string) (interface{}, error)
	RollbackLongTaskStory(context.Context, string, string, string, string) (agent.LongTaskRollbackResult, error)
	GCLongTaskArtifacts(context.Context, string, string, int, bool) (agent.LongTaskGCSweepResult, error)
	ListSubagents(context.Context, string) ([]agent.DurableSubagentJobView, error)
	Submit(context.Context, string, message.Envelope) (*rtbackend.SubmitResult, error)
	ExecuteCommand(context.Context, string, commands.Command) (commands.Result, error)
	ApprovePermission(context.Context, string, string, tools.PermissionGrantScope) (tools.PermissionResolution, error)
	DenyPermission(context.Context, string, string, string) (tools.PermissionResolution, error)
	AttachSink(string, events.Sink) (func(), error)
}

// Session is the Bubble Tea frontend bound to the shared backend.
type Session struct {
	cfg     *config.Config
	backend Backend
	stdout  io.Writer
	now     func() time.Time
}

type runtimeEventMsg struct {
	Event events.Event
}

type snapshotLoadedMsg struct {
	Snapshot rtbackend.Snapshot
	Err      error
}

type submitFinishedMsg struct {
	Err error
}

type commandFinishedMsg struct {
	Err error
}

type contextSummaryLoadedMsg struct {
	Summary tools.ContextInspection
	Err     error
}

type workbenchLoadedMsg struct {
	LongTasks []agent.LongTaskView
	Subagents []agent.DurableSubagentJobView
	Err       error
}

type permissionFinishedMsg struct {
	Resolution tools.PermissionResolution
	Err        error
}

type heartbeatTickMsg struct{}

type focusMode int

const (
	focusComposer focusMode = iota
	focusFeed
)

type feedItemKind string

const (
	feedUser       feedItemKind = "user"
	feedAssistant  feedItemKind = "assistant"
	feedTool       feedItemKind = "tool"
	feedTodo       feedItemKind = "todo"
	feedPermission feedItemKind = "permission"
	feedCommand    feedItemKind = "command"
	feedWarning    feedItemKind = "warning"
	feedError      feedItemKind = "error"
)

type feedItem struct {
	ID          string
	Kind        feedItemKind
	Title       string
	Body        string
	Summary     string
	Status      string
	TurnID      string
	Input       map[string]interface{}
	Output      string
	Error       string
	Foldable    bool
	Expanded    bool
	RuntimeOnly bool
	CreatedAt   time.Time
	SessionID   string
	Permission  *tools.PendingPermission
}

type itemSpan struct {
	ID       string
	Start    int
	End      int
	Foldable bool
}

type model struct {
	ctx       context.Context
	cfg       *config.Config
	backend   Backend
	now       func() time.Time
	sessionID string
	locator   rtbackend.SessionLocator

	width               int
	height              int
	feedHeight          int
	showRules           bool
	status              string
	autoFollow          bool
	focus               focusMode
	submitting          bool
	working             bool
	workingSince        time.Time
	heartbeatFrame      int
	resolvingPermission bool

	snapshot     rtbackend.Snapshot
	historyItems []feedItem
	overlayItems []feedItem
	itemSpans    []itemSpan

	selectedItemID string
	clipboardWrite func(string) error

	bashCancel context.CancelFunc
	bashCh     chan bashStreamEvent

	inputHistory      []string
	inputHistoryIndex int
	inputDraft        string

	contextSummary     tools.ContextInspection
	activeWorkbenchTab workbenchTab
	longTasks          []agent.LongTaskView
	// longTaskDetailVisible is true when the user has drilled into
	// a specific longtask from the workbench task tab. T15
	// acceptance: the 5 longtask components render in this mode
	// and the keyboard shortcuts (r/w/c/f/R/l/g) drive actions.
	longTaskDetailVisible bool
	longTaskDetailID      string
	longTaskRollback      longTaskRollbackReasonState
	longTaskLookup        longTaskLookupState
	subagents          []agent.DurableSubagentJobView
	workbenchErr       error
	modelCallCount     int
	seenModelCallEvent map[string]struct{}
	activePhase        string
	activeToolName     string

	highlighter      *Highlighter
	markdownRenderer *MarkdownRenderer

	viewport viewport.Model
	composer textarea.Model
}

func defaultClipboardWrite(text string) error {
	return clipboard.WriteAll(text)
}

var (
	titleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.AdaptiveColor{Light: "#111827", Dark: "#F9FAFB"})

	headerMetaStyle = lipgloss.NewStyle().
			Foreground(lipgloss.AdaptiveColor{Light: "#4B5563", Dark: "#9CA3AF"})

	userLineStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.AdaptiveColor{Light: "#111827", Dark: "#F9FAFB"})

	assistantLineStyle = lipgloss.NewStyle().
				Foreground(lipgloss.AdaptiveColor{Light: "#1F2937", Dark: "#E5E7EB"})

	toolLineStyle = lipgloss.NewStyle().
			Foreground(lipgloss.AdaptiveColor{Light: "#374151", Dark: "#D1D5DB"})

	toolRunningStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.AdaptiveColor{Light: "#0F766E", Dark: "#5EEAD4"})

	toolSelectedStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.AdaptiveColor{Light: "#0F766E", Dark: "#5EEAD4"})

	selectedTextStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.AdaptiveColor{Light: "#0F766E", Dark: "#5EEAD4"})

	permissionLineStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.AdaptiveColor{Light: "#B45309", Dark: "#FBBF24"})

	permissionSelectedStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.AdaptiveColor{Light: "#92400E", Dark: "#FCD34D"})

	commandLineStyle = lipgloss.NewStyle().
				Foreground(lipgloss.AdaptiveColor{Light: "#1D4ED8", Dark: "#93C5FD"})

	warningLineStyle = lipgloss.NewStyle().
				Foreground(lipgloss.AdaptiveColor{Light: "#B45309", Dark: "#FBBF24"})

	errorLineStyle = lipgloss.NewStyle().
			Foreground(lipgloss.AdaptiveColor{Light: "#B91C1C", Dark: "#FCA5A5"})

	mutedLineStyle = lipgloss.NewStyle().
			Foreground(lipgloss.AdaptiveColor{Light: "#6B7280", Dark: "#9CA3AF"})

	ruleStyle = lipgloss.NewStyle().
			Foreground(lipgloss.AdaptiveColor{Light: "#D1D5DB", Dark: "#4B5563"})

	readyStyle = lipgloss.NewStyle().
			Foreground(lipgloss.AdaptiveColor{Light: "#059669", Dark: "#34D399"})

	thinkingStyle = lipgloss.NewStyle().
			Foreground(lipgloss.AdaptiveColor{Light: "#7C3AED", Dark: "#A78BFA"})

	workingStyle = lipgloss.NewStyle().
			Foreground(lipgloss.AdaptiveColor{Light: "#2563EB", Dark: "#60A5FA"})

	// === Markdown Styles ===
	headingStyle = lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.AdaptiveColor{Light: "#6B21A8", Dark: "#C084FC"})

	heading1Style = lipgloss.NewStyle().
		Bold(true).
		Underline(true).
		Foreground(lipgloss.AdaptiveColor{Light: "#6B21A8", Dark: "#C084FC"})

	codeStyle = lipgloss.NewStyle().
		Foreground(lipgloss.AdaptiveColor{Light: "#B91C1C", Dark: "#FCA5A5"})

	codeBlockStyle = lipgloss.NewStyle().
		Foreground(lipgloss.AdaptiveColor{Light: "#1F2937", Dark: "#E5E7EB"})

	codeBlockBorderStyle = lipgloss.NewStyle().
		Foreground(lipgloss.AdaptiveColor{Light: "#4B5563", Dark: "#6B7280"})

	linkStyle = lipgloss.NewStyle().
		Underline(true).
		Foreground(lipgloss.AdaptiveColor{Light: "#1D4ED8", Dark: "#93C5FD"})

	quoteStyle = lipgloss.NewStyle().
		Italic(true).
		Foreground(lipgloss.AdaptiveColor{Light: "#4B5563", Dark: "#9CA3AF"})

	quoteBorderStyle = lipgloss.NewStyle().
		Foreground(lipgloss.AdaptiveColor{Light: "#9CA3AF", Dark: "#4B5563"})

	listBulletStyle = lipgloss.NewStyle().
		Foreground(lipgloss.AdaptiveColor{Light: "#059669", Dark: "#34D399"})

	hrStyle = lipgloss.NewStyle().
		Foreground(lipgloss.AdaptiveColor{Light: "#D1D5DB", Dark: "#4B5563"})

	boldStyle = lipgloss.NewStyle().Bold(true)
	italicStyle = lipgloss.NewStyle().Italic(true)
	strikethroughStyle = lipgloss.NewStyle().Strikethrough(true)

	tableBorderStyle = lipgloss.NewStyle().
		Foreground(lipgloss.AdaptiveColor{Light: "#6B7280", Dark: "#9CA3AF"})

	taskDoneStyle = lipgloss.NewStyle().
		Foreground(lipgloss.AdaptiveColor{Light: "#059669", Dark: "#34D399"})
	taskPendingStyle = lipgloss.NewStyle().
		Foreground(lipgloss.AdaptiveColor{Light: "#6B7280", Dark: "#9CA3AF"})

	// === Diff Styles ===
	diffRemovedStyle = lipgloss.NewStyle().
		Foreground(lipgloss.AdaptiveColor{Light: "#B91C1C", Dark: "#FCA5A5"})
	diffAddedStyle = lipgloss.NewStyle().
		Foreground(lipgloss.AdaptiveColor{Light: "#059669", Dark: "#34D399"})
	diffContextStyle = lipgloss.NewStyle().
		Foreground(lipgloss.AdaptiveColor{Light: "#6B7280", Dark: "#9CA3AF"})
	diffFileHeaderStyle = lipgloss.NewStyle().
		Foreground(lipgloss.AdaptiveColor{Light: "#374151", Dark: "#D1D5DB"})
)
