package shellterm_test

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/go-widgets/lineedit"
	"github.com/go-widgets/shellterm"
	"github.com/go-widgets/toolkit"
)

// echo is a Shell: it prints its line back, and "clear" clears the screen.
type echo struct{}

func (echo) Run(_ context.Context, line string, out io.Writer) (int, bool) {
	words, err := lineedit.Split(line)
	if err != nil {
		fmt.Fprintln(out, "syntax error:", err)
		return 2, false
	}
	if len(words) > 0 && words[0] == "clear" {
		return 0, true
	}
	fmt.Fprintln(out, strings.Join(words, " "))
	return 0, false
}

// A session in front of a toolkit.Terminal. post runs a function on the UI
// goroutine; here it runs it at once, as a single-threaded host would.
func Example() {
	post := func(f func()) { f() }
	term := toolkit.NewTerminal(80, 24, 1000)
	s := shellterm.New(shellterm.Options{Post: post, Greeting: "echo shell\r\n"})
	s.SetShell(echo{}, func(_ context.Context, words []string, _ string) ([]lineedit.Candidate, bool) {
		if len(words) == 0 {
			return []lineedit.Candidate{{Value: "clear"}}, false
		}
		return nil, false
	})
	shellterm.Bind(term, s, func() { /* ask the host to redraw */ })
	// The host now draws term and routes its events to it: typed keys
	// reach s, and s's output reaches term.
}
