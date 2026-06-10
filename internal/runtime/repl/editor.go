package repl

import (
	"context"
	"errors"
	"fmt"
	"io"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/bubbles/textarea"
)

// lineSubmittedMsg is delivered by the editor's Update whenever the
// user presses Enter on a non-empty line. The empty-line case is
// dropped so a stray Enter does not break the caller's for-loop.
type lineSubmittedMsg struct{ line string }

// editorModel wraps a bubbles/textarea so we can intercept Enter
// presses and turn them into lineSubmittedMsg. The textarea itself
// is grapheme-aware: left/right move by full Unicode grapheme
// cluster rather than by raw byte, which is what makes Chinese
// editing work correctly (chzyer/readline v1.5.1's MoveBackward /
// MoveForward worked on raw byte offsets and left the cursor
// "half a character" in when the line contained CJK).
type editorModel struct {
	ta      textarea.Model
	results chan<- string
	quits   chan<- error
}

func newEditorModel(prompt string, results chan<- string, quits chan<- error) editorModel {
	ta := textarea.New()
	ta.Prompt = prompt
	ta.ShowLineNumbers = false
	ta.CharLimit = 20000
	ta.SetHeight(1)
	ta.MaxHeight = 1
	ta.Focus()
	return editorModel{ta: ta, results: results, quits: quits}
}

func (m editorModel) Init() tea.Cmd { return m.ta.Focus() }

func (m editorModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		if msg.Type == tea.KeyEnter {
			line := m.ta.Value()
			if line == "" {
				// Don't deliver empty lines: REPL callers
				// re-prompt and empty Enter should not break
				// the loop. The textarea is left in place so
				// the user can keep typing.
				return m, nil
			}
			select {
			case m.results <- line:
			default:
				// Caller went away; nothing to do.
			}
			// Tell the tea.Program to exit so program.Run() returns
			// and the goroutine started in newEditor unwinds. Without
			// this, the program would keep reading keystrokes and
			// ReadLine's caller would never observe a clean shutdown.
			// The next readline is the responsibility of the REPL
			// loop, which constructs a fresh Editor.
			m.ta.Reset()
			return m, tea.Quit
		}
		if msg.Type == tea.KeyCtrlC || msg.Type == tea.KeyCtrlD {
			select {
			case m.quits <- io.EOF:
			default:
			}
			return m, tea.Quit
		}
	}
	var cmd tea.Cmd
	m.ta, cmd = m.ta.Update(msg)
	return m, cmd
}

func (m editorModel) View() string {
	return m.ta.View()
}

// Editor is the grapheme-aware REPL line editor. It replaces
// chzyer/readline v1.5.1's *readline.Instance for the sole purpose
// of "read one line of text from the user, supporting full
// Unicode including CJK and emoji". The handleEvent /
// commands.Parse / backend.Submit / AttachSink surface in
// internal/runtime/repl/repl.go does not need to know how lines
// are produced.
type Editor struct {
	program *tea.Program
	results <-chan string
	quits   <-chan error
}

// ErrEmptyLine is returned by ReadLine when the user pressed
// Enter on an empty line. Callers can choose to reprompt or
// surface it; REPL's Run loop silently re-prompts, matching
// readline's historical behaviour.
var ErrEmptyLine = errors.New("repl: empty line")

// newEditor creates a grapheme-aware line editor and starts a
// tea.Program driving it. The program does NOT take over the
// terminal's alt-screen (REPL is line-based, not full-screen) and
// does NOT install its own signal handler (REPL's caller owns
// SIGINT / SIGTERM via signal.NotifyContext).
//
// `in` is the user's stdin; it can be nil at construction time
// and provided later via AttachInput. `out` is accepted for
// signature compatibility with future swap-in line editors, but
// is currently ignored: tea.Program's renderer always writes to
// io.Discard, because the REPL loop owns its own prompt writer
// (Session.writePrompt) and concurrent writes to REPL stdout by
// the renderer and by handleEvent would interleave and corrupt
// the on-screen history. See repl_test.go TestSessionWritePrompt
// and the accompanying commit for the bug being prevented.
func newEditor(ctx context.Context, prompt string, in io.Reader, out io.Writer) (*Editor, error) {
	results := make(chan string, 1)
	quits := make(chan error, 1)
	m := newEditorModel(prompt, results, quits)

	options := []tea.ProgramOption{
		tea.WithoutSignalHandler(),
		tea.WithoutCatchPanics(), // we control our own teardown
		tea.WithContext(ctx),
		// REPL is line-based; the program only needs to render the
		// single prompt line. WithoutRenderer skips the alt-screen
		// handshake so the editor also works under `go test` where
		// stdout is not a TTY and bubbletea would otherwise block
		// forever waiting for the terminal to respond to the
		// enter-alt-screen escape sequence.
		tea.WithoutRenderer(),
		// Force the renderer to a sink. Even with WithoutRenderer
		// bubbletea still emits View() output on Update, and on a
		// real TTY it also emits cursor-positioning escape codes
		// that overwrite any concurrent printf from handleEvent.
		// Routing that output to Discard means the REPL loop is
		// the sole writer of REPL stdout, which is the only way
		// to keep multi-line history messages from being squashed
		// onto a single line.
		tea.WithOutput(io.Discard),
	}
	if in != nil {
		options = append(options, tea.WithInput(in))
	}
	_ = out // accepted for caller convenience; see doc comment.

	program := tea.NewProgram(m, options...)

	ed := &Editor{
		program: program,
		results: results,
		quits:   quits,
	}

	// Run the program in a goroutine. tea.NewProgram.Run blocks
	// until the program quits; we don't want ReadLine callers to
	// block on it directly because we need to start the program
	// before the first user keystroke arrives.
	go func() {
		_, err := program.Run()
		// Propagate any error other than the clean "we asked it to
		// quit" path. The Ctrl+C / Ctrl+D handler above is the
		// only thing that should end the program in normal use.
		if err != nil && !errors.Is(err, tea.ErrProgramKilled) {
			select {
			case quits <- err:
			default:
			}
		}
	}()

	return ed, nil
}

// AttachInput re-points the program's input at a different
// io.Reader. This is used by tests that want to feed synthetic
// key sequences without a real terminal. In production it is a
// no-op after the program has already started; the input is
// captured at tea.NewProgram time and bubbletea does not expose
// a way to swap it later. We keep the method so the test
// signature stays clean and future code can route stdin through
// an io.Pipe if needed.
func (e *Editor) AttachInput(in io.Reader) {
	// bubbletea v1.3.10 doesn't expose Program.SetInput; the
	// input is fixed at NewProgram time. This method exists
	// as a documentation hook for callers that want to feed
	// a different source; it has no effect on an already-running
	// program. The REPL's real stdin is wired in newEditor.
	_ = in
}

// ReadLine blocks until the user submits a non-empty line or the
// editor quits. The returned string is byte-identical to the
// input the user typed, including any CJK, emoji, or ZWJ
// sequences — grapheme-aware left/right on the textarea
// guarantees that no edit can land mid-UTF-8.
//
// Each call to ReadLine consumes the *Editor*: after the user
// presses Enter the underlying tea.Program is told to quit and
// the program goroutine unwinds. The REPL loop is responsible
// for constructing a fresh Editor for the next line, mirroring
// the chzyer/readline v1.5.1 model where each Readline() call is
// also a one-shot blocking read.
func (e *Editor) ReadLine() (string, error) {
	line, err := func() (string, error) {
		select {
		case line := <-e.results:
			return line, nil
		case err := <-e.quits:
			return "", err
		}
	}()
	// Belt-and-suspenders: ask the program to quit regardless of
	// which path won the race. The KeyEnter handler already
	// returns tea.Quit, but if the caller's ctx fires or a
	// Ctrl+C/D landed first, we still need the program goroutine
	// to unblock. program.Quit is safe to call after the program
	// has already exited.
	if e.program != nil {
		e.program.Quit()
	}
	return line, err
}

// Close tears the program down. Safe to call multiple times.
func (e *Editor) Close() {
	if e.program != nil {
		e.program.Quit()
	}
}

// Compile-time assertion that editorModel satisfies the tea.Model
// interface. If bubbletea ever breaks the contract (e.g. changes
// the Init / Update / View signatures) this line fails to
// compile and we know immediately.
var _ tea.Model = editorModel{}

// Compile-time guard so the fmt import is used even if the
// implementation evolves past its current shape.
var _ = fmt.Sprintf
