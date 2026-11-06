package notify

import (
	"os"
	"path/filepath"
	"strings"
)

// Files named in a shell command (round 13, G2b): Codex, Copilot and agy
// run most of their reads and many writes through a shell tool whose
// payload is the command line. A plain, conservative reading of it names
// the files: cat, head, tail, nl, bat, less and sed -n read the files they
// are given; `> file`, `&> file` and tee write one; `>> file`, tee -a and
// sed -i edit one; `< file` reads one. Anything with a command
// substitution or a variable in a word is not guessed at, a heredoc's body
// is not read, and a path counts only when it is a regular file on disk
// now (the hook runs after the tool did, on the session's machine), so a
// flag, a pattern or a typo names nothing.
const maxShellFiles = 32

var shellReaders = map[string]bool{"cat": true, "head": true, "tail": true, "nl": true, "bat": true, "batcat": true, "less": true, "more": true}

// deviceName says whether p, clean and absolute, names a device rather
// than a file: anything under /dev but /dev/shm. A name like /dev/stdin or
// /dev/fd/3 resolves to what the hook's own process holds open, which can
// be a regular file that is not the agent's, so the regular-file check
// below cannot tell it; /dev/null and /dev/tty fail that check anyway.
// /dev/shm is a tmpfs where people keep ordinary files, and is kept.
// /dev/mqueue is not: its entries are POSIX message queues, which stat as
// regular files but are no file anyone read or wrote.
func deviceName(p string) bool {
	if p != "/dev" && !strings.HasPrefix(p, "/dev/") {
		return false
	}
	return p != "/dev/shm" && !strings.HasPrefix(p, "/dev/shm/")
}

// shellFiles are the files command reads, writes or edits, absolute when
// cwd (the command's directory) is known, at most maxShellFiles, a repeat
// of the same op and path dropped.
func shellFiles(command, cwd string) []FileRef {
	if command == "" || strings.Contains(command, "$(") || strings.Contains(command, "`") {
		return nil
	}
	if strings.Contains(command, "<<") {
		// A heredoc's body is data: only the line that opens it is a command.
		command, _, _ = strings.Cut(command, "\n")
	}
	var out []FileRef
	add := func(op, word string) {
		if len(out) == maxShellFiles || word == "" {
			return
		}
		p := word
		if strings.HasPrefix(p, "~/") {
			if home, err := os.UserHomeDir(); err == nil {
				p = filepath.Join(home, p[2:])
			}
		}
		if !filepath.IsAbs(p) {
			if cwd == "" {
				return
			}
			p = filepath.Join(cwd, p)
		}
		if p = filepath.Clean(p); deviceName(p) {
			return
		}
		if fi, err := os.Stat(p); err != nil || !fi.Mode().IsRegular() {
			return
		}
		for _, f := range out {
			if f.Op == op && f.Path == p {
				return
			}
		}
		out = append(out, FileRef{Op: op, Path: p})
	}
	for _, cmd := range shellCommands(command) {
		for _, r := range cmd.redirects {
			switch r.op {
			case ">", "&>", ">|":
				add("write", r.target)
			case ">>", "&>>":
				add("edit", r.target)
			case "<":
				add("read", r.target)
			}
		}
		words := cmd.words
		for len(words) > 0 && strings.Contains(words[0], "=") && !strings.HasPrefix(words[0], "=") {
			words = words[1:] // VAR=value before the command
		}
		if len(words) > 0 && (words[0] == "sudo" || words[0] == "command" || words[0] == "env") {
			words = words[1:]
		}
		if len(words) == 0 {
			continue
		}
		name, args := filepath.Base(words[0]), words[1:]
		switch {
		case shellReaders[name]:
			for _, a := range operands(args, name == "head" || name == "tail") {
				add("read", a)
			}
		case name == "tee":
			op := "write"
			if hasFlag(args, "-a", "--append") {
				op = "edit"
			}
			for _, a := range operands(args, false) {
				add(op, a)
			}
		case name == "sed":
			inPlace := false
			script := true // the first operand is the script unless -e or -f gave it
			for _, a := range args {
				if a == "-i" || strings.HasPrefix(a, "-i") || a == "--in-place" || strings.HasPrefix(a, "--in-place=") {
					inPlace = true
				}
				if a == "-e" || a == "-f" || strings.HasPrefix(a, "--expression") || strings.HasPrefix(a, "--file") {
					script = false
				}
			}
			ops := operands(args, false)
			if script && len(ops) > 0 {
				ops = ops[1:]
			}
			op := "read"
			if inPlace {
				op = "edit"
			} else if !hasFlag(args, "-n", "--quiet", "--silent") {
				continue // a sed that prints its whole input is a filter, not a read worth naming
			}
			for _, a := range ops {
				add(op, a)
			}
		}
	}
	return out
}

// operands are the words of args that are not options; numeric skips the
// value of head's and tail's -n and -c given as a separate word.
func operands(args []string, numeric bool) []string {
	var out []string
	rest := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case rest:
			out = append(out, a)
		case a == "--":
			rest = true
		case strings.HasPrefix(a, "-") && a != "-":
			if numeric && (a == "-n" || a == "-c") {
				i++
			}
			if a == "-e" || a == "-f" {
				i++ // sed's script or script file
			}
		default:
			out = append(out, a)
		}
	}
	return out
}

func hasFlag(args []string, flags ...string) bool {
	for _, a := range args {
		for _, f := range flags {
			if a == f {
				return true
			}
		}
		// Short flags run together: -an, -ni.
		if len(a) > 2 && a[0] == '-' && a[1] != '-' {
			for _, f := range flags {
				if len(f) == 2 && f[0] == '-' && strings.ContainsRune(a[1:], rune(f[1])) {
					return true
				}
			}
		}
	}
	return false
}

type shellRedirect struct{ op, target string }

type shellCommand struct {
	words     []string
	redirects []shellRedirect
}

// shellCommands splits a command line into its simple commands (at |, ||,
// &&, ;, & and new lines), each with its words and its redirects, quotes
// taken away. A word holding a $ outside single quotes is dropped, so a
// variable is never taken for a file.
func shellCommands(line string) []shellCommand {
	var cmds []shellCommand
	var cur shellCommand
	var word strings.Builder
	inWord, unsafe := false, false
	pending := "" // a redirect waiting for its target
	endWord := func() {
		if !inWord {
			return
		}
		w := word.String()
		word.Reset()
		inWord = false
		if unsafe {
			unsafe = false
			if pending != "" {
				pending = ""
			}
			return
		}
		if pending != "" {
			cur.redirects = append(cur.redirects, shellRedirect{pending, w})
			pending = ""
			return
		}
		cur.words = append(cur.words, w)
	}
	endCmd := func() {
		endWord()
		pending = ""
		if len(cur.words) > 0 || len(cur.redirects) > 0 {
			cmds = append(cmds, cur)
		}
		cur = shellCommand{}
	}
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch {
		case c == '\'':
			inWord = true
			j := strings.IndexByte(line[i+1:], '\'')
			if j < 0 {
				word.WriteString(line[i+1:])
				i = len(line)
				continue
			}
			word.WriteString(line[i+1 : i+1+j])
			i += j + 1
		case c == '"':
			inWord = true
			for i++; i < len(line) && line[i] != '"'; i++ {
				if line[i] == '\\' && i+1 < len(line) {
					i++
				} else if line[i] == '$' {
					unsafe = true
				}
				word.WriteByte(line[i])
			}
		case c == '\\' && i+1 < len(line):
			inWord = true
			i++
			if line[i] != '\n' {
				word.WriteByte(line[i])
			}
		case c == '$':
			inWord, unsafe = true, true
			word.WriteByte(c)
		case c == ' ' || c == '\t':
			endWord()
		case c == '\n' || c == ';' || c == '|' || c == '&' && !(i+1 < len(line) && line[i+1] == '>'):
			endCmd()
			if i+1 < len(line) && (line[i+1] == '|' || line[i+1] == '&') && (c == '|' || c == '&') {
				i++
			}
		case c == '>' || c == '<' || c == '&':
			// A redirect: [n]> >> >| < &> &>>, a dup like 2>&1 or >&2 names no file.
			w := word.String()
			if inWord && w != "" && strings.Trim(w, "0123456789") != "" {
				endWord()
			} else {
				word.Reset()
				inWord = false
			}
			op := string(c)
			if c == '&' {
				op = "&>"
				i++
			}
			if i+1 < len(line) && (line[i+1] == '>' || line[i+1] == '|') && c != '<' {
				op += string(line[i+1])
				i++
			}
			if i+1 < len(line) && line[i+1] == '&' {
				// A dup (>&2, 2>&1): its target is a descriptor.
				i++
				for i+1 < len(line) && (line[i+1] >= '0' && line[i+1] <= '9' || line[i+1] == '-') {
					i++
				}
				continue
			}
			pending = op
		default:
			inWord = true
			word.WriteByte(c)
		}
	}
	endCmd()
	return cmds
}
