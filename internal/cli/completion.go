package cli

import (
	"bytes"
	"cmp"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/rocksolidlabs/conductor/internal/agents"
	"github.com/rocksolidlabs/conductor/internal/dispatch"
	"github.com/rocksolidlabs/conductor/internal/version"
)

const completionUsage = `Usage:
  conductor completion zsh|bash
      print a completion script for the shell: subcommands, flags and their
      values, and for conductor up the crew ids, from conductor crews --ids
      as you type (the server from CONDUCTOR_SERVER, else
      http://localhost:8080, and the token from CONDUCTOR_WORKBENCH_TOKEN; no ids
      without the token or when the server does not answer)
  conductor completion install [--shell zsh|bash] [--rc FILE]
      add one marked line that loads the script to ~/.zshrc or ~/.bashrc,
      the shell from $SHELL unless --shell says; run again, it changes
      nothing; a file, a link or a directory that is not yours is refused
`

// completionMark ends the line completion install writes, telling whoever
// reads the rc file where it came from. A second install looks for the whole
// line, not the mark: a comment of the user's may hold it.
const completionMark = "# conductor completion"

// idShape is crew.ValidID's pattern, as an extended regular expression the
// scripts apply again to what `crews --ids` prints: a conductor of another
// version, or another program of that name, may print anything.
// TestCompletionIDShapeIsValidID holds the two together.
const idShape = "^[a-z0-9][a-z0-9-]{0,63}$"

// How a flag's value is completed.
type flagKind int

const (
	flagBool  flagKind = iota // takes no value
	flagValue                 // takes a value nothing can guess: completes nothing
	flagFile                  // takes a path
	flagEnum                  // takes one of values
)

type flagSpec struct {
	name   string
	kind   flagKind
	values []string
}

// commandSpec is a subcommand as the scripts know it: its flags (those of
// the FlagSet its run function makes; TestCompletionSpecMatchesEveryFlag
// holds the two together), the words its first argument may be, the words its
// second may be after each first, and whether its argument is a crew id.
type commandSpec struct {
	name   string
	flags  []flagSpec
	words  []string
	after  map[string][]string
	crewID bool
}

// completionSpec lists the subcommands of root.go with the flags their run
// functions define. The notify payload flags come from hookPayloads and the
// adapters from agents.All, so those follow on their own.
func completionSpec() []commandSpec {
	levels := []string{"debug", "info", "warn", "error"}
	api := func(more ...flagSpec) []flagSpec {
		return append([]flagSpec{{name: "--server", kind: flagValue}, {name: "--token", kind: flagValue}, {name: "--insecure"}}, more...)
	}
	serveFlags := []flagSpec{{name: "--config", kind: flagFile}, {name: "--listen", kind: flagValue}, {name: "--dev"}, {name: "--log-level", kind: flagEnum, values: levels}, {name: "--examples"}, {name: "--yolo"}, {name: "--print-listen"}, {name: "--exit-on-stdin-close"}, {name: "--switchyard"}}
	notifyFlags := []flagSpec{
		{name: "--state", kind: flagEnum, values: []string{"needs_input", "working", "done", "clear"}},
		{name: "--message", kind: flagValue},
		{name: "--event", kind: flagEnum, values: []string{"progress", "artifact", "handoff", "tool_use", "tool_denied", "error", "file"}},
		{name: "--url", kind: flagValue},
		{name: "--to", kind: flagValue},
		{name: "--tool", kind: flagValue},
		{name: "--op", kind: flagEnum, values: []string{"read", "edit", "write", "delete"}},
		{name: "--path", kind: flagFile},
		{name: "--choices", kind: flagValue},
		{name: "--ask"},
		{name: "--codex"},
		{name: "--files"},
	}
	for _, h := range hookPayloads {
		notifyFlags = append(notifyFlags, flagSpec{name: "--" + h.name})
	}
	notifyFlags = append(notifyFlags, flagSpec{name: "--quiet"})
	adapters := []string{"all"}
	for _, a := range agents.All() {
		adapters = append(adapters, a.ID)
	}
	return []commandSpec{
		// Every flag serve takes, and no other: TestCompletionSpecMatchesEveryFlag checks it.
		{name: "serve", flags: serveFlags},
		{name: "switchyard", flags: serveFlags},
		{name: "host", flags: []flagSpec{{name: "--ice-udp-port", kind: flagValue}, {name: "--ice-public-ip", kind: flagValue}, {name: "--server", kind: flagValue}, {name: "--token", kind: flagValue}, {name: "--name", kind: flagValue}, {name: "--host-name", kind: flagValue}, {name: "--agent", kind: flagValue}, {name: "--cwd", kind: flagFile}, {name: "--relay-only"}, {name: "--server-is-mine"}, {name: "--insecure"}, {name: "--no-local"}, {name: "--stun", kind: flagValue}, {name: "--scrollback", kind: flagValue}, {name: "--file-view", kind: flagEnum, values: []string{"view", "control", "off"}},
			{name: "--file-edit", kind: flagEnum, values: []string{"control", "off"}}, {name: "--server-config", kind: flagFile}, {name: "--signal-pattern", kind: flagValue}, {name: "--link", kind: flagEnum, values: []string{"view", "control"}}, {name: "--log-level", kind: flagEnum, values: levels}}},
		{name: "notify", flags: notifyFlags},
		{name: "mcp"},
		{name: "crew", flags: []flagSpec{{name: "--self", kind: flagValue}, {name: "--open"}, {name: "--ttl", kind: flagValue}, {name: "--label", kind: flagValue}, {name: "--quiet"}}, words: []string{"create", "add", "status", "link"}},
		{name: "task", flags: []flagSpec{{name: "--page", kind: flagValue}, {name: "--json"}, {name: "--summary", kind: flagValue}, {name: "--commit", kind: flagValue}, {name: "--issue", kind: flagValue}, {name: "--verdict", kind: flagEnum, values: []string{"approved", "needs_changes"}}, {name: "--finding", kind: flagValue}, {name: "--file", kind: flagFile},
			{name: "--to", kind: flagValue}, {name: "--after", kind: flagValue}, {name: "--reason", kind: flagValue}, {name: "--message", kind: flagValue}, {name: "--done", kind: flagValue}, {name: "--remaining", kind: flagValue}, {name: "--notes", kind: flagValue}}, words: []string{"context", "result", "send", "inbox", "ack", "blocked", "checkpoint", "ticket"},
			after: map[string][]string{"ticket": {"comment"}}},
		{name: "up", flags: api(flagSpec{name: "--name", kind: flagValue}, flagSpec{name: "--resume", kind: flagValue}, flagSpec{name: "--open"}), crewID: true},
		{name: "crews", flags: api(flagSpec{name: "--ids"})},
		// Every flag of every dispatch command (each takes its own), and the
		// store's vocabularies as their values.
		{name: "dispatch", flags: api(flagSpec{name: "--project", kind: flagValue}, flagSpec{name: "--status", kind: flagEnum, values: dispatch.Statuses}, flagSpec{name: "--q", kind: flagValue}, flagSpec{name: "--json"},
			flagSpec{name: "--title", kind: flagValue}, flagSpec{name: "--description-file", kind: flagFile}, flagSpec{name: "--priority", kind: flagEnum, values: dispatch.Priorities}, flagSpec{name: "--owner", kind: flagValue}, flagSpec{name: "--label", kind: flagValue},
			flagSpec{name: "--assignee", kind: flagValue}, flagSpec{name: "--milestone", kind: flagValue}, flagSpec{name: "--horizon", kind: flagEnum, values: dispatch.Horizons[1:]}, flagSpec{name: "--note", kind: flagValue},
			flagSpec{name: "--kind", kind: flagEnum, values: dispatch.LinkKinds}, flagSpec{name: "--url", kind: flagValue}, flagSpec{name: "--ref", kind: flagValue}),
			words: []string{"projects", "tickets", "show", "create", "edit", "comment", "accept", "link"}},
		{name: "hooks", flags: []flagSpec{{name: "--home", kind: flagFile}, {name: "--data-dir", kind: flagFile}}, words: []string{"install", "status"}, after: map[string][]string{"install": adapters}},
		{name: "skill"},
		{name: "completion", flags: []flagSpec{{name: "--shell", kind: flagEnum, values: []string{"zsh", "bash"}}, {name: "--rc", kind: flagFile}}, words: []string{"zsh", "bash", "install"}},
		{name: "version"},
	}
}

func commandNames(spec []commandSpec) string {
	out := make([]string, len(spec))
	for i, c := range spec {
		out[i] = c.name
	}
	return strings.Join(out, " ")
}

func flagNames(flags []flagSpec) string {
	out := make([]string, len(flags))
	for i, f := range flags {
		out[i] = f.name
	}
	return strings.Join(out, " ")
}

// zshScript is the zsh completion script for spec: a function on words and
// CURRENT that hands compadd what fits, bound with compdef once compinit has
// run. The crew ids come from `crews --ids` run as the command typed
// (words[1]), so a conductor not on PATH still completes; each line that has
// a crew id's shape (idShape) reaches compadd as an array element, never as
// shell text.
func zshScript(spec []commandSpec) string {
	var b strings.Builder
	fmt.Fprintf(&b, "#compdef conductor\n# conductor completion for zsh, generated by conductor %s. Load it after compinit with: source <(conductor completion zsh)\n", version.String())
	b.WriteString("_conductor() {\n  local cur=${words[CURRENT]} cmd=${words[2]} sub=${words[3]} prev=${words[CURRENT-1]}\n")
	fmt.Fprintf(&b, "  if (( CURRENT == 2 )); then compadd -- %s; return; fi\n", commandNames(spec))
	b.WriteString("  if [[ $cur == -* ]]; then\n    case $cmd in\n")
	for _, c := range spec {
		if len(c.flags) > 0 {
			fmt.Fprintf(&b, "      %s) compadd -- %s ;;\n", c.name, flagNames(c.flags))
		}
	}
	b.WriteString("    esac\n    return\n  fi\n  case \"$cmd $prev\" in\n")
	for _, c := range spec {
		for _, f := range c.flags {
			switch f.kind {
			case flagEnum:
				fmt.Fprintf(&b, "    %q) compadd -- %s; return ;;\n", c.name+" "+f.name, strings.Join(f.values, " "))
			case flagFile:
				fmt.Fprintf(&b, "    %q) _files; return ;;\n", c.name+" "+f.name)
			case flagValue:
				fmt.Fprintf(&b, "    %q) return ;;\n", c.name+" "+f.name)
			}
		}
	}
	b.WriteString("  esac\n  case $cmd in\n")
	for _, c := range spec {
		switch {
		case c.crewID:
			fmt.Fprintf(&b, "    %s)\n      local id re='%s' MATCH MBEGIN MEND; local -a ids\n      for id in \"${(@f)$(\"${words[1]}\" crews --ids 2>/dev/null)}\"; do [[ $id =~ $re ]] && ids+=(\"$id\"); done\n      (( ${#ids} )) && compadd -- \"${ids[@]}\" ;;\n", c.name, idShape)
		case len(c.words) > 0:
			fmt.Fprintf(&b, "    %s)\n      if (( CURRENT == 3 )); then compadd -- %s\n", c.name, strings.Join(c.words, " "))
			for _, w := range c.words {
				if more := c.after[w]; len(more) > 0 {
					fmt.Fprintf(&b, "      elif [[ $sub == %s ]] && (( CURRENT == 4 )); then compadd -- %s\n", w, strings.Join(more, " "))
				}
			}
			b.WriteString("      fi ;;\n")
		}
	}
	b.WriteString("  esac\n}\nif (( $+functions[compdef] )); then compdef _conductor conductor; fi\n")
	return b.String()
}

// bashScript is the bash completion script for spec, the same shape on
// COMP_WORDS, COMP_CWORD and COMPREPLY; compgen -W filters the script's own
// words by the typed prefix. The crew ids come from `crews --ids` run as
// COMP_WORDS[0] and are never expanded: a read loop keeps each line that has
// a crew id's shape (idShape) and starts with the typed prefix, compared as a
// string, and adds it to COMPREPLY as it is. File names come through the same
// kind of loop, so a name with a space stays one candidate, with compopt -o
// filenames for readline to quote them and mark directories.
func bashScript(spec []commandSpec) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# conductor completion for bash, generated by conductor %s. Load it with: source <(conductor completion bash)\n", version.String())
	b.WriteString("_conductor() {\n  local cur=${COMP_WORDS[COMP_CWORD]} cmd=${COMP_WORDS[1]} sub=${COMP_WORDS[2]} prev=${COMP_WORDS[COMP_CWORD-1]}\n  COMPREPLY=()\n")
	fmt.Fprintf(&b, "  if (( COMP_CWORD == 1 )); then COMPREPLY=($(compgen -W %q -- \"$cur\")); return; fi\n", commandNames(spec))
	b.WriteString("  if [[ $cur == -* ]]; then\n    case $cmd in\n")
	for _, c := range spec {
		if len(c.flags) > 0 {
			fmt.Fprintf(&b, "      %s) COMPREPLY=($(compgen -W %q -- \"$cur\")) ;;\n", c.name, flagNames(c.flags))
		}
	}
	b.WriteString("    esac\n    return\n  fi\n  case \"$cmd $prev\" in\n")
	for _, c := range spec {
		for _, f := range c.flags {
			switch f.kind {
			case flagEnum:
				fmt.Fprintf(&b, "    %q) COMPREPLY=($(compgen -W %q -- \"$cur\")); return ;;\n", c.name+" "+f.name, strings.Join(f.values, " "))
			case flagFile:
				fmt.Fprintf(&b, "    %q) compopt -o filenames 2>/dev/null; local f; while IFS= read -r f; do COMPREPLY+=(\"$f\"); done < <(compgen -f -- \"$cur\"); return ;;\n", c.name+" "+f.name)
			case flagValue:
				fmt.Fprintf(&b, "    %q) return ;;\n", c.name+" "+f.name)
			}
		}
	}
	b.WriteString("  esac\n  case $cmd in\n")
	for _, c := range spec {
		switch {
		case c.crewID:
			fmt.Fprintf(&b, "    %s)\n      local id re='%s'\n      while IFS= read -r id; do [[ $id =~ $re && $id == \"$cur\"* ]] && COMPREPLY+=(\"$id\"); done < <(\"${COMP_WORDS[0]}\" crews --ids 2>/dev/null) ;;\n", c.name, idShape)
		case len(c.words) > 0:
			fmt.Fprintf(&b, "    %s)\n      if (( COMP_CWORD == 2 )); then COMPREPLY=($(compgen -W %q -- \"$cur\"))\n", c.name, strings.Join(c.words, " "))
			for _, w := range c.words {
				if more := c.after[w]; len(more) > 0 {
					fmt.Fprintf(&b, "      elif [[ $sub == %s ]] && (( COMP_CWORD == 3 )); then COMPREPLY=($(compgen -W %q -- \"$cur\"))\n", w, strings.Join(more, " "))
				}
			}
			b.WriteString("      fi ;;\n")
		}
	}
	b.WriteString("  esac\n}\ncomplete -F _conductor conductor\n")
	return b.String()
}

// runCompletion prints a script or installs the line that loads it.
func runCompletion(args []string, stdout, stderr io.Writer) (int, error) {
	if len(args) == 0 {
		fmt.Fprint(stderr, completionUsage)
		return 2, errors.New("completion: zsh, bash or install is required")
	}
	switch args[0] {
	case "zsh", "bash":
		if len(args) != 1 {
			fmt.Fprint(stderr, completionUsage)
			return 2, fmt.Errorf("completion %s takes no arguments", args[0])
		}
		script := zshScript(completionSpec())
		if args[0] == "bash" {
			script = bashScript(completionSpec())
		}
		_, err := io.WriteString(stdout, script)
		return 0, err
	case "install":
		return runCompletionInstall(args[1:], stdout, stderr)
	case "-h", "--help", "help":
		fmt.Fprint(stderr, completionUsage)
		return 0, nil
	}
	fmt.Fprint(stderr, completionUsage)
	return 2, fmt.Errorf("completion: unknown shell %q (zsh or bash)", args[0])
}

// runCompletionInstall appends the line to the rc file of the shell.
func runCompletionInstall(args []string, stdout, stderr io.Writer) (int, error) {
	fs := flag.NewFlagSet("completion install", flag.ContinueOnError)
	fs.SetOutput(stderr)
	shell := fs.String("shell", "", "zsh or bash (default: the name of $SHELL)")
	rc := fs.String("rc", "", "the file to add the line to (default: ~/.zshrc or ~/.bashrc)")
	fs.Usage = func() {
		fmt.Fprint(stderr, completionUsage)
		fs.PrintDefaults()
	}
	rest, err := parseInterspersed(fs, args)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0, nil
		}
		return 2, err
	}
	if len(rest) != 0 {
		fs.Usage()
		return 2, errors.New("completion install takes no arguments")
	}
	sh := cmp.Or(*shell, filepath.Base(os.Getenv("SHELL")))
	if sh != "zsh" && sh != "bash" {
		return 2, fmt.Errorf("completion install: say which shell with --shell zsh or --shell bash (SHELL is %q)", os.Getenv("SHELL"))
	}
	path := *rc
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil || !filepath.IsAbs(home) {
			return 1, fmt.Errorf("completion install: your home directory is unknown (HOME is %q); name the rc file with --rc", os.Getenv("HOME"))
		}
		path = filepath.Join(home, "."+sh+"rc")
	}
	changed, err := installCompletionLine(path, sh)
	if err != nil {
		return 1, fmt.Errorf("completion install: %w", err)
	}
	if !changed {
		fmt.Fprintf(stdout, "%s has the line already; nothing to change\n", path)
		return 0, nil
	}
	fmt.Fprintf(stdout, "wrote %s:\n  %s\nopen a new shell, or run that line, to complete conductor\n", path, completionLine(sh))
	if sh == "zsh" {
		fmt.Fprintln(stdout, "zsh: the line works once compinit has run, so the rc file must run compinit before it (autoload -Uz compinit && compinit)")
	}
	return 0, nil
}

// completionLine is the line installed: it loads the script this conductor
// prints when conductor is on PATH, and is marked so a reader of the rc file
// knows where it came from.
func completionLine(shell string) string {
	return "command -v conductor >/dev/null 2>&1 && source <(conductor completion " + shell + ") " + completionMark
}

// installCompletionLine appends completionLine(shell) to path unless a line
// of it is that line already (spaces around it aside), and reports whether it
// wrote. Before it reads or writes anything, the directory path is in must be
// the caller's own (agents.CheckOwner), and so must path when it exists: a
// link by its own owner (agents.CheckLinkOwner) as well as the file it leads
// to. Under sudo, a file of another user's, a link of theirs (which they may
// point at a file of root's) or a file in their directory (which they may
// swap for such a link) is refused. A missing file is made 0600 with O_EXCL,
// so that nothing put there meanwhile is followed; an existing one is
// appended to and keeps its mode.
func installCompletionLine(path, shell string) (bool, error) {
	if err := agents.CheckOwner(filepath.Dir(path)); err != nil {
		return false, err
	}
	fi, err := os.Lstat(path)
	exists := err == nil
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return false, err
	default:
		if fi.Mode()&fs.ModeSymlink != 0 {
			if err := agents.CheckLinkOwner(path); err != nil {
				return false, err
			}
		}
		if err := agents.CheckOwner(path); err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return false, fmt.Errorf("%s is a link to a file that does not exist: nothing written", path)
			}
			return false, err
		}
	}
	line := completionLine(shell)
	var b []byte
	if exists {
		if b, err = os.ReadFile(path); err != nil {
			return false, err
		}
		for l := range strings.SplitSeq(string(b), "\n") {
			if strings.TrimSpace(l) == line {
				return false, nil
			}
		}
	}
	flags := os.O_APPEND | os.O_WRONLY
	if !exists {
		flags |= os.O_CREATE | os.O_EXCL
	}
	f, err := os.OpenFile(path, flags, 0o600)
	if err != nil {
		return false, err
	}
	add := line + "\n"
	if len(b) > 0 && !bytes.HasSuffix(b, []byte("\n")) {
		add = "\n" + add
	}
	if _, err := f.WriteString(add); err != nil {
		f.Close()
		return false, err
	}
	return true, f.Close()
}
