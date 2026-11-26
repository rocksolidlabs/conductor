package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"slices"
	"strconv"
	"strings"

	"github.com/rocksolidlabs/conductor/internal/notify"
)

// runNotify reports an attention state, or with --event something the agent
// did, from inside a Conductor session. It is designed for agent hooks:
// outside a session it exits 0 without output.
func runNotify(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	fs := flag.NewFlagSet("notify", flag.ContinueOnError)
	fs.SetOutput(stderr)
	state := fs.String("state", "needs_input", "needs_input, working, done or clear")
	message := fs.String("message", "", "short message shown next to the badge")
	event := fs.String("event", "", "report an event instead of a state: progress, artifact, handoff, tool_use, tool_denied, error or file")
	link := fs.String("url", "", "with --event artifact: where the result lives")
	to := fs.String("to", "", "with --event handoff: who the work goes to")
	tool := fs.String("tool", "", "with --event tool_use, tool_denied, error or file: the tool involved")
	op := fs.String("op", "", "with --event file: what was done to it, read, edit, write or delete")
	filePath := fs.String("path", "", "with --event file: the file, as you name it")
	choices := fs.String("choices", "", "with --state needs_input: answers to offer as buttons, separated by |, at most 6 of 40 bytes; a click sends the choice as the person's answer")
	ask := fs.Bool("ask", false, "your own question (--message, and --choices): the session shows it as needing input until someone answers, also once your turn has ended")
	codex := fs.Bool("codex", false, "read a Codex notify payload from the last argument and map it")
	files := fs.Bool("files", false, "with --<agent>-hook: report only the files the tool call touched, not the call (Claude Code's file-tool hook)")
	// Each agent's hooks write their payload to stdin; the flag says whose it is.
	type hookFlag struct {
		flag    string
		set     *bool
		mapHook func([]byte) (notify.Request, bool)
	}
	hooks := make([]hookFlag, 0, len(hookPayloads))
	for _, h := range hookPayloads {
		hooks = append(hooks, hookFlag{"--" + h.name, fs.Bool(h.name, false, h.usage), h.mapHook})
	}
	quiet := fs.Bool("quiet", true, "exit 0 silently when not running inside a conductor session")
	floor := fs.String("protocol-floor", "", "a chat's PreToolUse hook: read the shell call's payload from stdin and deny a command that names a path of the deny list in this file")
	fs.Usage = func() {
		fmt.Fprintln(stderr, "Usage: conductor notify [--state S] [--message M] [--choices \"a|b|c\"] | --ask --message M [--choices \"a|b|c\"] | --event E [--message M] [--url U] [--to T] [--tool N] [--op O --path P] | --<agent>-hook [--files] | --codex <json>")
		fs.PrintDefaults()
	}
	// Agents' hooks run this command, and a hook runner reads exit 2 as
	// "block the agent": a mistake in a hook's command line exits 1.
	misuse := 2
	if hookMode(args) {
		misuse = 1
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0, nil
		}
		return misuse, err
	}
	if *floor != "" {
		// A chat's floor, its second look (docs/plans/chat-or-terminal.md,
		// design 7): nothing is sent anywhere.
		return protocolFloor(*floor, stdin, stdout)
	}
	// The payload flags given, in the order of the usage: at most one may be.
	var payload []string
	for _, h := range hooks {
		if *h.set {
			payload = append(payload, h.flag)
		}
	}
	if *codex {
		payload = append(payload, "--codex")
	}
	if len(payload) > 1 {
		return misuse, fmt.Errorf("%s cannot be combined: a payload is read one way", strings.Join(payload, " and "))
	}
	if *files && (len(payload) == 0 || payload[0] == "--codex") {
		return misuse, errors.New("--files goes with a hook payload: conductor notify --claude-hook --files")
	}
	// The agent's own question is said on purpose, and only as one: never
	// with an event, a hook's payload (a program's wait is no question of
	// the agent's) or another state.
	if *ask {
		switch {
		case *event != "":
			return misuse, errors.New("--ask asks a question: conductor notify --ask --message M [--choices \"a|b|c\"], not with --event")
		case len(payload) > 0:
			return misuse, fmt.Errorf("--ask asks a question: conductor notify --ask --message M [--choices \"a|b|c\"], not with %s", strings.Join(payload, " or "))
		case *state != "needs_input":
			return misuse, fmt.Errorf("--ask asks a question: it goes with --state needs_input or none, not with --state %s", *state)
		}
	}
	if *event != "" {
		// --state has a default, so only Visit can tell that it was given.
		stateGiven := false
		fs.Visit(func(f *flag.Flag) { stateGiven = stateGiven || f.Name == "state" })
		with := payload
		if stateGiven {
			with = append([]string{"--state"}, payload...)
		}
		if len(with) > 0 {
			return misuse, fmt.Errorf("--event cannot be combined with %s", strings.Join(with, " or "))
		}
	} else if fields := eventFieldsGiven(fs); len(fields) > 0 {
		// A state, or what a payload maps to, carries none of them: they would
		// be dropped without a word.
		verb := "goes"
		if len(fields) > 1 {
			verb = "go"
		}
		return misuse, fmt.Errorf("%s only %s with --event: conductor notify --event E [--message M] [--url U] [--to T] [--tool N] [--op O --path P]", joinFlags(fields), verb)
	}
	// Choices go with a needs_input state of the agent's own, and nothing else.
	var options []notify.Option
	if *choices != "" {
		switch {
		case *event != "":
			return misuse, errors.New("--choices goes with --state needs_input, not with --event")
		case len(payload) > 0:
			return misuse, fmt.Errorf("--choices goes with --state needs_input, not with %s", strings.Join(payload, " or "))
		case *state != "needs_input":
			return misuse, fmt.Errorf("--choices goes with --state needs_input, not with --state %s", *state)
		}
		opts, err := parseChoices(*choices)
		if err != nil {
			return misuse, err
		}
		options = opts
	}
	// A chat's child (the protocol way, CONDUCTOR_PROTOCOL set) reports
	// through its own stream; the hooks the person's settings run in it
	// too (Conductor's own among them) would be a second account of the
	// same steps. Their payload is read, so the runner never writes into a
	// closed pipe, and nothing is sent. The agent's own reports (--ask,
	// --event, a state) are not hook payloads and are still sent.
	if len(payload) == 1 && os.Getenv(protocolEnv) != "" {
		if payload[0] != "--codex" {
			_, _ = readPayload(stdin)
		}
		return 0, nil
	}
	target, err := notify.TargetFromEnv(os.Getenv)
	if err != nil {
		// A hook runner writes the payload whether or not there is a session
		// to report to: read it, so the runner never writes into a closed
		// pipe. (--codex has its payload in an argument, and its stdin is
		// Codex's own.)
		if len(payload) == 1 && payload[0] != "--codex" {
			_, _ = readPayload(stdin)
		}
		if *quiet {
			return 0, nil
		}
		return 1, err
	}
	req := notify.Request{State: *state, Message: *message, Ask: *ask}
	if len(options) > 0 {
		req.Kind = "prompt"
		req.Options = options
	}
	if *event != "" {
		req = notify.Request{Event: *event, Message: *message, URL: *link, To: *to, Tool: *tool, Op: *op, Path: *filePath}
	}
	for _, h := range hooks {
		if !*h.set {
			continue
		}
		raw, err := readPayload(stdin)
		if err != nil {
			return 1, err
		}
		mapped, ok := h.mapHook(raw)
		if !ok {
			return 0, nil
		}
		req = mapped
		// The file tools' hook reports their files alone (design 4e): a call
		// that names no file is nothing to send.
		if *files {
			if len(req.Files) == 0 {
				return 0, nil
			}
			req = notify.Request{Tool: req.Tool, Files: req.Files}
		}
	}
	if *codex {
		rest := fs.Args()
		if len(rest) == 0 {
			return 0, nil
		}
		mapped, ok := notify.MapCodex([]byte(rest[len(rest)-1]))
		if !ok {
			return 0, nil
		}
		req = mapped
	}
	// To the session's host first when it listens (a hosted session), else
	// to the server (Target.Send).
	if err := target.Send(ctx, req); err != nil {
		if *quiet {
			// Hooks must never break the agent because Conductor is unreachable.
			fmt.Fprintln(stderr, "conductor notify:", err)
			return 0, nil
		}
		return 1, err
	}
	return 0, nil
}

// eventFieldsGiven returns the flags that describe an event (--url, --to,
// --tool) given on the command line, in the order of the usage, even with an
// empty value.
func eventFieldsGiven(fs *flag.FlagSet) []string {
	given := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { given[f.Name] = true })
	var out []string
	for _, name := range []string{"url", "to", "tool", "op", "path"} {
		if given[name] {
			out = append(out, "--"+name)
		}
	}
	return out
}

// joinFlags lists flags as prose: "--url", "--url and --to", "--url, --to and
// --tool".
func joinFlags(flags []string) string {
	if len(flags) < 2 {
		return strings.Join(flags, "")
	}
	return strings.Join(flags[:len(flags)-1], ", ") + " and " + flags[len(flags)-1]
}

// protocolEnv is set (to 1) in the environment of a session's child that
// runs the agent's protocol way.
const protocolEnv = "CONDUCTOR_PROTOCOL"

// hookPayload is an agent whose hooks write their payload to stdin: the flag
// that says whose it is, its usage, and the mapper.
type hookPayload struct {
	name, usage string
	mapHook     func([]byte) (notify.Request, bool)
}

// hookPayloads lists them in the order of the usage.
var hookPayloads = []hookPayload{
	{"claude-hook", "read a Claude Code hook payload from stdin and map it", notify.MapClaudeHook},
	{"codex-hook", "read a Codex hooks.json payload from stdin and map it", notify.MapCodexHook},
	{"copilot-hook", "read a GitHub Copilot CLI hook payload from stdin and map it", notify.MapCopilotHook},
	{"cursor-hook", "read a Cursor CLI hook payload from stdin and map it", notify.MapCursorHook},
	{"agy-hook", "read an Antigravity hook payload from stdin and map it", notify.MapAgyHook},
	{"goose-hook", "read a Goose hook payload from stdin and map it", notify.MapGooseHook},
}

// payloadFlags are the flags that make the command map an agent's payload:
// one per hookPayloads entry, and --codex.
var payloadFlags = func() []string {
	out := make([]string, 0, len(hookPayloads)+1)
	for _, h := range hookPayloads {
		out = append(out, h.name)
	}
	return append(out, "codex")
}()

// hookMode reports whether args ask for an agent's payload to be mapped, which
// is how agents' hooks run the command, whether or not the rest of args parses.
func hookMode(args []string) bool {
	for _, a := range args {
		if a == "--" {
			break
		}
		name, value, hasValue := strings.Cut(strings.TrimLeft(a, "-"), "=")
		if strings.HasPrefix(a, "-") && name == "protocol-floor" {
			// A chat's PreToolUse hook (protocolFloor).
			return true
		}
		if !strings.HasPrefix(a, "-") || !slices.Contains(payloadFlags, name) {
			continue
		}
		if on, err := strconv.ParseBool(value); !hasValue || err != nil || on {
			return true
		}
	}
	return false
}

// readPayload reads a hook's payload from stdin, at most 1 MiB. A terminal is
// not read: nothing writes a payload to one, and what is typed there is the
// agent's.
func readPayload(stdin io.Reader) ([]byte, error) {
	if f, ok := stdin.(*os.File); ok {
		if fi, err := f.Stat(); err == nil && fi.Mode()&os.ModeCharDevice != 0 {
			return nil, nil
		}
	}
	return notify.ReadAllBounded(stdin)
}

// Bounds of --choices: how many, and the bytes of one (the typed line).
const (
	maxChoices    = 6
	maxChoiceSize = 40
)

// parseChoices turns "a|b|c" into quick-reply options: the label is the
// choice, the input is the choice and a carriage return, so a click types
// it as a line. At most maxChoices, none empty, none over maxChoiceSize
// bytes, none holding a control character.
func parseChoices(s string) ([]notify.Option, error) {
	parts := strings.Split(s, "|")
	if len(parts) > maxChoices {
		return nil, fmt.Errorf("--choices: %d choices, at most %d", len(parts), maxChoices)
	}
	out := make([]notify.Option, 0, len(parts))
	for _, c := range parts {
		c = strings.TrimSpace(c)
		switch {
		case c == "":
			return nil, errors.New("--choices: an empty choice (a|b|c, each a word or a short phrase)")
		case len(c) > maxChoiceSize:
			return nil, fmt.Errorf("--choices: %q is longer than %d bytes", c, maxChoiceSize)
		case strings.ContainsFunc(c, func(r rune) bool { return r < 0x20 || r == 0x7f }):
			return nil, fmt.Errorf("--choices: %q holds a control character", c)
		}
		out = append(out, notify.Option{Label: c, Input: c + "\r"})
	}
	return out, nil
}
