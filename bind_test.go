package shellterm

import (
	"strings"
	"testing"

	"github.com/go-widgets/toolkit"
	"github.com/go-widgets/toolkit/vt"
)

// Bind wires a toolkit.Terminal both ways: typing reaches the shell, its
// output reaches the screen, a clear empties screen and scrollback, and each
// change asks for a redraw.
func TestBind(t *testing.T) {
	r := newRig(t, Options{Greeting: "hi\r\n"})
	term := toolkit.NewTerminal(80, 12, 100)
	sh := &recShell{}
	r.ses.SetShell(sh, nil)
	redraws := 0
	r.onUI(func() { Bind(term, r.ses, func() { redraws++ }) })
	screen := func() (s string) {
		r.onUI(func() {
			sc := term.Screen()
			s = sc.Text(vt.Pos{}, vt.Pos{Line: sc.Lines(), Col: 80})
		})
		return s
	}
	wait := func(want string) {
		t.Helper()
		r.until(want, func() bool { return strings.Contains(screen(), want) })
	}
	wait("hi")
	term.OnInput("echo 1\r") // what the widget hands over for typed keys
	wait("ran echo 1")
	term.OnResize(100, 12)
	r.until("cols", func() bool { r.ses.mu.Lock(); defer r.ses.mu.Unlock(); return r.ses.r.Cols == 100 })

	r.onUI(func() { term.Screen().WriteString(strings.Repeat("line\r\n", 40)) }) // into the scrollback
	sh.set(0, true, nil)
	term.OnInput("clear\r")
	r.until("cleared", func() bool {
		var n int
		r.onUI(func() { n = term.Screen().ScrollbackLen() })
		return n == 0 && !strings.Contains(screen(), "line") && !r.running()
	})
	if redraws == 0 {
		t.Error("no redraw asked for")
	}
}

// A nil invalidate is allowed.
func TestBindWithoutInvalidate(t *testing.T) {
	r := newRig(t, Options{Greeting: "hi\r\n"})
	term := toolkit.NewTerminal(80, 12, 0)
	r.onUI(func() { Bind(term, r.ses, nil) })
	r.ses.Input("\x0c") // a clear through the bound Clear
	r.until("prompt", func() bool {
		var s string
		r.onUI(func() { s = term.Screen().Text(vt.Pos{}, vt.Pos{Line: 1, Col: 80}) })
		return strings.HasPrefix(s, "$")
	})
}
