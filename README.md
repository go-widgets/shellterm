# shellterm

A line-oriented terminal for go-widgets. It puts a line editor
([go-widgets/lineedit](https://github.com/go-widgets/lineedit): history, Tab
completion, Ctrl-R, Ctrl-C, Ctrl-L) in front of a **Shell you supply**, and
draws into a `toolkit.Terminal`. Each application keeps only its shell.

```sh
go get github.com/go-widgets/shellterm
```

## Use

```go
type Shell interface {
    Run(ctx context.Context, line string, out io.Writer) (code int, clear bool)
}

s := shellterm.New(shellterm.Options{
    Post:     post,                // runs a func on the UI goroutine (required)
    Greeting: "my shell\r\n",      // written once, before the first prompt
    OnDone:   refresh,             // optional: after each command, on the UI goroutine
    Prompt:   func() string { return cwd + " $ " }, // optional; default "$ "
})
s.SetShell(myShell, myCompleter) // a lineedit.Completer, or nil
term := toolkit.NewTerminal(80, 24, 5000)
shellterm.Bind(term, s, invalidate) // typing → s, output → term, clear → screen and scrollback
```

[example_test.go](example_test.go) is the same code, compiled by `go vet`
on every change.

**How a session behaves:**
- **The prompt:** a submitted line goes to `Shell.Run` on a goroutine of its
  own. Its output becomes terminal output (`\n` → `\r\n`). A non-zero status
  shows `[exit status N]`, then the prompt returns.
- **A prompt that follows the shell:** `Options.Prompt` (optional; the default
  is `$ `) is asked when the session starts and after each command, once `Run`
  has returned, so it can show a working directory or the last status
  (`[1] /tmp $ `). It runs on the session's goroutine with the session locked:
  read the shell's state, do not call back into the `Session`. Control
  characters are dropped from what it returns; an empty answer keeps the previous
  prompt. It is drawn in lineedit's prompt style (bold cyan).
- **While a command runs:** only Ctrl-C reaches it (it cancels `ctx`).
  `Running` (an `mvvm.Observable[bool]`) says so.
- **Keys:** they are handled on the session's goroutine, never the UI's, so a
  `Completer` may ask a slow back end. Each key's work is bounded by
  `CompleteTimeout`.
- **`Note(text)`:** shows a line above the prompt and keeps the line being
  edited below it. Nothing is shown while a command runs.
- **Bounds:** at most `InputQueue` keystrokes wait (more are dropped, never
  blocking the UI); at most `PendingWrites` outputs are kept before the first
  `Attach`.
- **Hosts:** a host without `toolkit.Terminal` calls `Attach(out, clear)`,
  `Input` and `Resize` itself.

**Font and cell size:** set them on the terminal: `term.SetFont(face)`,
`term.SetCellSize(16, 16)` (toolkit v0.362.0).

## Origin

Extracted from go-wasmcontainers/dashboard v0.8.0 (`vm/terminal.go`) for
go-widgets/toolkit#548, so that two consumers share one implementation:
- the go-wasmcontainers dashboard and playground;
- wasmdesk/wasmbox (wasmdesk/wasmbox#173).

**What changed in the move:**
- The dashboard's app queue became `Options.Post`, and its refresh became
  `Options.OnDone`.
- The wctr greeting became `Options.Greeting`.
- The "# dashboard:" note became the generic `Note`; the caller formats and
  cleans its text.
- wctr's shell and completer stay in go-wasmcontainers.

## Tests

- 100 % statement coverage, gated in CI.
- The session tests run under `-race` against a real `vt.Screen` and a
  serialised UI goroutine, and cover: the greeting, history kept across
  `SetShell`, Ctrl-C at the prompt and during a command, exit status, clear and
  Ctrl-L, completion listings, notes, and the queue bounds.
- CI runs on Linux (amd64 and arm64), macOS, Windows and 32-bit Linux, vets
  js/wasm, and runs govulncheck.
- Untrusted input (keys, terminal bytes) is parsed by lineedit, which fuzzes
  it in its own CI. Untrusted output goes to `toolkit.Terminal`, which is safe
  for it by construction.

## Licence

BSD 3-Clause, see [LICENSE](LICENSE).
