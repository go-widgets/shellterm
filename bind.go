package shellterm

import "github.com/go-widgets/toolkit"

// Bind connects a session to a toolkit.Terminal: what the user types goes
// to the session, a resize tells it the width, and its output and clears
// go to the terminal, after which invalidate (nil: nothing) asks the host
// to redraw. A clear empties the screen and the scrollback. Call it on the
// UI goroutine; it attaches the session.
func Bind(term *toolkit.Terminal, s *Session, invalidate func()) {
	if invalidate == nil {
		invalidate = func() {}
	}
	term.OnInput = s.Input
	term.OnResize = func(cols, _ int) { s.Resize(cols) }
	s.Attach(func(out string) {
		_, _ = term.Write([]byte(out)) // a Terminal's Write never fails
		invalidate()
	}, func() {
		term.Screen().WriteString("\x1b[H\x1b[2J")
		term.Screen().ClearScrollback()
		invalidate()
	})
}
