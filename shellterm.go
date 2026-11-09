// Package shellterm is a line-oriented terminal: a line editor
// (github.com/go-widgets/lineedit: history, Tab completion, Ctrl-R, Ctrl-C,
// Ctrl-L) in front of a Shell that the caller supplies, drawing into a
// terminal (Bind connects a toolkit.Terminal).
//
// A Session is the model. Input arrives as the bytes a terminal sends;
// output goes to the function given to Attach. Keys are handled on the
// session's own goroutine, never the UI's: a Completer may ask a slow
// back end, and must not block the event loop that delivers its answer.
// Everything the session hands the view goes through Options.Post, which
// runs a function on the UI goroutine (an mvvm queue's Post, or a plain
// host's equivalent), so the view is only ever touched from there.
//
// It was extracted from go-wasmcontainers/dashboard v0.8.0 (vm/terminal.go)
// so that the dashboard and wasmdesk share one implementation.
package shellterm

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/go-widgets/lineedit"
	"github.com/go-widgets/mvvm"
)

// Shell runs one command line, writing its output to out, and returns the
// exit status and whether the screen should be cleared. It runs on a
// goroutine of its own; ctx is cancelled by Ctrl-C.
type Shell interface {
	Run(ctx context.Context, line string, out io.Writer) (code int, clear bool)
}

// Options configure a Session.
type Options struct {
	// Post runs f on the UI goroutine, in order. Required: New panics
	// without it, since a session that wrote from its own goroutine would
	// race the view.
	Post func(f func())
	// Greeting is written once, at the first Attach, before the prompt.
	Greeting string
	// OnDone, if set, runs on the UI goroutine after each command, once its
	// output and the prompt are written (to refresh what it changed).
	OnDone func()
	// HistorySize bounds the history (0: 500).
	HistorySize int
	// Prompt, if set, returns the prompt to show before the line. It is
	// called on the session's own goroutine, with the session locked, when the
	// session starts (Attach, SetShell) and after each command, once Run has
	// returned: so it may read state that the shell changed (a working
	// directory, the last status) without a lock of its own, but must not call
	// back into the Session. Control characters are dropped from its result (a
	// prompt is one line of text, its width is counted as such); the prompt is
	// drawn in the renderer's prompt style. "" (or nil Prompt) keeps the
	// previous prompt, "$ " at first.
	Prompt func() string
}

// Limits on what waits for the view or for the key goroutine.
const (
	// InputQueue bounds the keystrokes waiting for the key goroutine; keys
	// past it are dropped rather than blocking the UI.
	InputQueue = 256
	// PendingWrites bounds the output kept before the first Attach.
	PendingWrites = 256
	// CompleteTimeout bounds one key's work, a Tab completion included.
	CompleteTimeout = 5 * time.Second
)

// Session is the terminal's model.
type Session struct {
	opts Options

	// Out receives everything to draw and Clear clears the screen; both
	// are set by Attach and called on the UI goroutine.
	Out   func(string)
	Clear func()
	// Running says a command runs (only Ctrl-C reaches it). It is set on
	// the UI goroutine.
	Running *mvvm.Observable[bool]

	mu       sync.Mutex
	shell    Shell
	complete lineedit.Completer
	ed       *lineedit.Editor
	r        *lineedit.Renderer
	run      context.CancelFunc
	in       chan string
	started  bool
	pending  []string
}

// New returns a session with no shell (a submitted line says so) until
// SetShell.
func New(o Options) *Session {
	if o.Post == nil {
		panic("shellterm: Options.Post is required")
	}
	if o.HistorySize <= 0 {
		o.HistorySize = 500
	}
	return &Session{opts: o, Running: mvvm.NewObservable(false),
		in: make(chan string, InputQueue), r: &lineedit.Renderer{Cols: 80}}
}

// SetShell sets what runs the lines and what completes them (nil: no
// completion). The history is kept.
func (t *Session) SetShell(s Shell, complete lineedit.Completer) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.shell, t.complete = s, complete
	hist := []string(nil)
	if t.ed != nil {
		hist = t.ed.History()
	}
	t.ed = lineedit.New(lineedit.Options{Complete: complete, Cols: t.r.Cols, HistorySize: t.opts.HistorySize})
	t.ed.SetHistory(hist)
	t.refreshPrompt()
}

// refreshPrompt asks Options.Prompt for the prompt; t.mu is held and t.ed set.
func (t *Session) refreshPrompt() {
	if t.opts.Prompt == nil {
		return
	}
	t.ed.SetPrompt(strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || r >= 0x80 && r <= 0x9f {
			return -1
		}
		return r
	}, t.opts.Prompt()))
}

// History returns the lines entered, oldest first (for persistence).
func (t *Session) History() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.ed == nil {
		return nil
	}
	return t.ed.History()
}

// out hands s to the view, from any goroutine.
func (t *Session) out(s string) { t.opts.Post(func() { t.write(s) }) }

// write runs on the UI goroutine.
func (t *Session) write(s string) {
	if t.Out == nil {
		if len(t.pending) < PendingWrites {
			t.pending = append(t.pending, s)
		}
		return
	}
	t.Out(s)
}

// Attach connects the view and starts the session: greeting, prompt, and
// the goroutine that handles keys. A second Attach (a new view) replays
// nothing and greets no more. Called on the UI goroutine. clear may be nil:
// the escape sequences that clear the screen are written instead.
func (t *Session) Attach(out func(string), clear func()) {
	t.Out, t.Clear = out, clear
	for _, s := range t.pending {
		out(s)
	}
	t.pending = nil
	if t.started {
		return
	}
	t.started = true
	if t.ed == nil {
		t.SetShell(nil, nil)
	}
	t.mu.Lock()
	t.write(t.opts.Greeting)
	t.write(t.r.Render(t.ed.State()))
	t.mu.Unlock()
	go t.loop()
}

// Resize tells the editor the terminal's width. It never blocks the
// caller (the UI goroutine): the key goroutine may hold the editor while a
// completion waits.
func (t *Session) Resize(cols int) {
	go func() {
		t.mu.Lock()
		defer t.mu.Unlock()
		t.r.Cols = cols
		if t.ed != nil {
			t.ed.SetCols(cols)
		}
	}()
}

// Input queues what the user typed. It never blocks (keys past a full
// queue are dropped).
func (t *Session) Input(data string) {
	select {
	case t.in <- data:
	default:
	}
}

func (t *Session) loop() {
	for data := range t.in {
		for _, k := range lineedit.ParseKeys(data) {
			t.key(k)
		}
	}
}

// Note shows one line of text above the prompt, the line being edited kept
// below it; nothing while a command runs or before Attach. The text is
// written as given (escape sequences included): clean anything untrusted
// first. Called on the UI goroutine, which it never blocks (see Resize).
func (t *Session) Note(text string) {
	if !t.started || t.Running.Get() {
		return
	}
	go func() {
		t.mu.Lock()
		defer t.mu.Unlock()
		if t.run != nil {
			return // a command owns the screen
		}
		t.out(t.r.Print([]string{text}, t.ed.State()))
	}()
}

func (t *Session) key(k lineedit.Key) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.run != nil {
		if k.Code == lineedit.KeyCtrlC {
			t.run()
			t.out("^C")
		}
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), CompleteTimeout)
	before := t.ed.State()
	ev := t.ed.Key(ctx, k)
	cancel()
	switch {
	case ev.Submit:
		s := lineedit.State{Prompt: t.ed.State().Prompt, Line: ev.Line, Cursor: utf8.RuneCountInString(ev.Line)}
		t.out(t.r.Render(s) + t.r.Leave(s))
		t.start(ev.Line)
	case ev.Interrupt:
		t.out("^C" + t.r.Leave(before) + t.r.Render(t.ed.State()))
	case ev.Clear:
		t.r.Forget()
		t.clearOut(t.r.Render(t.ed.State()))
	case ev.Print != nil:
		t.out(t.r.Print(ev.Print, t.ed.State()))
	default:
		t.out(t.r.Render(t.ed.State()))
	}
}

// termWriter turns a command's output into terminal output: "\n" becomes
// "\r\n" (the screen is a terminal, not a log).
type termWriter struct{ t *Session }

func (w termWriter) Write(p []byte) (int, error) {
	w.t.out(strings.ReplaceAll(string(p), "\n", "\r\n"))
	return len(p), nil
}

// start runs a line; t.mu is held.
func (t *Session) start(line string) {
	if strings.TrimSpace(line) == "" {
		t.out(t.r.Render(t.ed.State()))
		return
	}
	if t.shell == nil {
		t.out("no shell attached\r\n" + t.r.Render(t.ed.State()))
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.run = cancel
	t.opts.Post(func() { t.Running.Set(true) })
	shell := t.shell
	go func() {
		code, clear := shell.Run(ctx, line, termWriter{t})
		cancel()
		t.mu.Lock()
		t.run = nil
		var b strings.Builder
		if clear {
			t.r.Forget()
		} else if code != 0 {
			fmt.Fprintf(&b, "\x1b[2m[exit status %d]\x1b[0m\r\n", code)
		}
		t.refreshPrompt()
		b.WriteString(t.r.Render(t.ed.State()))
		t.mu.Unlock()
		out := b.String()
		t.opts.Post(func() {
			if clear {
				t.clear()
			}
			t.write(out)
			t.Running.Set(false)
			if t.opts.OnDone != nil {
				t.opts.OnDone()
			}
		})
	}()
}

// clearOut clears the screen, then writes s (from any goroutine).
func (t *Session) clearOut(s string) {
	t.opts.Post(func() {
		t.clear()
		t.write(s)
	})
}

// clear runs on the UI goroutine: the view's Clear (screen and history),
// or the escape sequences that clear the screen.
func (t *Session) clear() {
	if t.Clear != nil {
		t.Clear()
		return
	}
	t.write("\x1b[H\x1b[2J")
}
