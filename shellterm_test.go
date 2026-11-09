package shellterm

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-widgets/lineedit"
	"github.com/go-widgets/toolkit/vt"
)

// rig is a session drawing into a vt screen. Post runs each function at
// once under ui, which stands in for the UI goroutine: everything that
// touches the view or the screen is serialised by it.
type rig struct {
	t      *testing.T
	ui     sync.Mutex
	s      *vt.Screen
	ses    *Session
	clears int
	dones  int
}

func newRig(t *testing.T, o Options) *rig {
	r := &rig{t: t, s: vt.New(80, 12, 200)}
	o.Post = func(f func()) { r.ui.Lock(); defer r.ui.Unlock(); f() }
	if o.OnDone == nil {
		o.OnDone = func() { r.dones++ }
	}
	r.ses = New(o)
	return r
}

func (r *rig) onUI(f func()) { r.ui.Lock(); defer r.ui.Unlock(); f() }

func (r *rig) attach() {
	r.onUI(func() {
		r.ses.Attach(func(out string) { r.s.WriteString(out) }, func() {
			r.clears++
			r.s.WriteString("\x1b[H\x1b[2J")
		})
	})
}

func (r *rig) text() (s string) {
	r.onUI(func() { s = r.s.Text(vt.Pos{}, vt.Pos{Line: r.s.Lines(), Col: 80}) })
	return s
}

// line is the cursor's line, trimmed.
func (r *rig) line() (s string) {
	r.onUI(func() {
		_, y, _ := r.s.Cursor()
		l := r.s.ScrollbackLen() + y
		s = strings.TrimRight(r.s.Text(vt.Pos{Line: l}, vt.Pos{Line: l, Col: 79}), " ")
	})
	return s
}

// counts reads the clears and OnDone calls, which change on the UI side.
func (r *rig) counts() (clears, dones int) {
	r.onUI(func() { clears, dones = r.clears, r.dones })
	return clears, dones
}

func (r *rig) running() (b bool) {
	r.onUI(func() { b = r.ses.Running.Get() })
	return b
}

// until waits for cond, failing after five seconds.
func (r *rig) until(what string, cond func() bool) {
	r.t.Helper()
	for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(time.Millisecond) {
		if cond() {
			return
		}
	}
	r.t.Fatalf("timed out waiting for %s; screen:\n%s", what, r.text())
}

// typeWait types in and waits until the screen shows want.
func (r *rig) typeWait(in, want string) {
	r.t.Helper()
	r.ses.Input(in)
	r.until(want, func() bool { return strings.Contains(r.text(), want) })
}

// settle waits until the key goroutine has handled everything queued.
func (r *rig) settle() {
	r.t.Helper()
	r.until("queue drained", func() bool { return len(r.ses.in) == 0 })
	r.ses.mu.Lock() // the key in hand, if any, is done
	r.ses.mu.Unlock()
}

// recShell records the lines it runs.
type recShell struct {
	mu    sync.Mutex
	lines []string
	code  int
	clear bool
	block chan struct{} // non-nil: Run waits for ctx (Ctrl-C) or a close
}

func (s *recShell) Run(ctx context.Context, line string, out io.Writer) (int, bool) {
	s.mu.Lock()
	s.lines = append(s.lines, line)
	code, clear, block := s.code, s.clear, s.block
	s.mu.Unlock()
	if block != nil {
		select {
		case <-ctx.Done():
			return 130, false
		case <-block:
		}
	}
	fmt.Fprintf(out, "ran %s\n", line)
	return code, clear
}

func (s *recShell) set(code int, clear bool, block chan struct{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.code, s.clear, s.block = code, clear, block
}

// static completes from a fixed table keyed by the words before the cursor.
func static(m map[string][]lineedit.Candidate) lineedit.Completer {
	return func(_ context.Context, words []string, _ string) ([]lineedit.Candidate, bool) {
		return m[strings.Join(words, " ")], false
	}
}

var words = static(map[string][]lineedit.Candidate{
	"":     {{Value: "wctr"}, {Value: "help"}, {Value: "clear"}},
	"wctr": {{Value: "run"}, {Value: "rm"}},
})

func TestSession(t *testing.T) {
	r := newRig(t, Options{Greeting: "\x1b[1mhello\x1b[0m. Type help.\r\n"})
	ses := r.ses
	if ses.History() != nil {
		t.Error("history before any editor")
	}
	ses.Note("before attach: not shown")
	r.onUI(func() { ses.write("early ") })
	r.attach()
	r.attach() // greets once
	if txt := r.text(); !strings.HasPrefix(txt, "early hello. Type help.") || strings.Count(txt, "hello") != 1 {
		t.Fatalf("greeting = %q", txt)
	}
	r.typeWait("wctr ps\r", "no shell attached")

	sh := &recShell{}
	ses.SetShell(sh, words)
	ses.Resize(80)
	r.typeWait("wctr images\r", "ran wctr images")
	r.until("prompt back", func() bool { return !r.running() && r.line() == "$" })
	if _, dones := r.counts(); dones != 1 {
		t.Errorf("OnDone ran %d times, want 1", dones)
	}
	// History survives SetShell; Up recalls.
	ses.SetShell(sh, nil)
	if h := ses.History(); len(h) != 2 || h[1] != "wctr images" {
		t.Errorf("history = %q", h)
	}
	r.typeWait("\x1b[A", "$ wctr images")
	r.typeWait("\x03", "^C")
	r.until("prompt after ^C", func() bool { return r.line() == "$" })
	sh.set(3, false, nil)
	r.typeWait("wctr ps\r", "[exit status 3]")
	sh.set(0, true, nil)
	ses.Input("clear\r")
	r.until("cleared", func() bool { c, _ := r.counts(); return c == 1 && !r.running() })
	sh.set(0, false, nil)
	ses.Input("\x0c")
	r.until("Ctrl-L", func() bool { c, _ := r.counts(); return c == 2 })
	r.typeWait("   \r", "$")
	// Completion lists candidates above the prompt.
	ses.SetShell(sh, words)
	r.typeWait("\t\t", "clear")
	r.typeWait("\x15", "$")

	// A running command gets Ctrl-C only.
	sh.set(0, false, make(chan struct{}))
	ses.Input("wctr logs -f clock\r")
	r.until("running", r.running)
	r.onUI(func() { ses.Note("# not while a command runs") })
	ses.Input("xyz\x03")
	r.until("interrupted", func() bool { return !r.running() })
	r.settle()
	if txt := r.text(); strings.Contains(txt, "xyz") || strings.Contains(txt, "not while") {
		t.Errorf("keys reached a running command:\n%s", txt)
	}
	sh.set(0, false, nil)

	// A command that starts between the call and the note's turn wins.
	ses.mu.Lock()
	ses.run = func() {}
	ses.mu.Unlock()
	r.onUI(func() { ses.Note("racing") })
	time.Sleep(20 * time.Millisecond) // the note's goroutine finds run set
	ses.mu.Lock()
	ses.run = nil
	ses.mu.Unlock()
	if strings.Contains(r.text(), "racing") {
		t.Error("a note over a running command")
	}
	// A note shows above the prompt; the line being edited stays below.
	r.typeWait("wc", "$ wc")
	r.onUI(func() { ses.Note("# dashboard: wctr rm web") })
	r.until("note", func() bool { return strings.Contains(r.text(), "# dashboard: wctr rm web") })
	if r.line() != "$ wc" {
		t.Errorf("line after a note = %q", r.line())
	}
}

func TestSessionBounds(t *testing.T) {
	r := newRig(t, Options{})
	ses := r.ses
	// A full input queue drops keys instead of blocking.
	for range InputQueue + 5 {
		ses.Input("x")
	}
	if len(ses.in) != InputQueue {
		t.Errorf("queue = %d", len(ses.in))
	}
	r.onUI(func() {
		for range PendingWrites + 44 {
			ses.write("x")
		}
	})
	if len(ses.pending) != PendingWrites {
		t.Errorf("pending = %d", len(ses.pending))
	}
	ses.Resize(100) // before an editor exists: only the renderer learns it
	r.until("resized", func() bool { ses.mu.Lock(); defer ses.mu.Unlock(); return ses.r.Cols == 100 })
	// Without the view's Clear, the escape sequences clear.
	var b strings.Builder
	ses.Out = func(s string) { b.WriteString(s) }
	ses.clear()
	if b.String() != "\x1b[H\x1b[2J" {
		t.Errorf("clear = %q", b.String())
	}
	if New(Options{Post: func(func()) {}}).opts.HistorySize != 500 {
		t.Error("default history size")
	}
}

func TestNewRequiresPost(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("New without Post did not panic")
		}
	}()
	New(Options{})
}

// Without OnDone a command still finishes cleanly.
func TestSessionWithoutOnDone(t *testing.T) {
	r := &rig{t: t, s: vt.New(80, 12, 200)}
	r.ses = New(Options{Post: func(f func()) { r.ui.Lock(); defer r.ui.Unlock(); f() }})
	r.ses.SetShell(&recShell{}, nil)
	r.attach()
	r.typeWait("go\r", "ran go")
	r.until("prompt", func() bool { return !r.running() && r.line() == "$" })
}
