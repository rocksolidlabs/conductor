package agents

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/rocksolidlabs/conductor/internal/catalog"
)

// An agent's own flags, read from its own help, so that an arguments field
// can offer them as a person types (and after a flag that takes a value, the
// values its help lists). Nothing here is a table kept by hand: the flags,
// their words, their values and the model names are what the installed
// version's help prints; what a help cannot say (which flags a launch by
// Conductor sets itself, which ones turn the agent's questions off) comes
// from what Conductor already holds: the adapter's model flag, the entry's
// yolo and session recipes, and the adapter's FlagsRecipe.

// FlagsRecipe says how an adapter's agent prints its flags, and what a
// help cannot say about them. nil on an adapter whose help Conductor has
// not read: no flag is offered for it.
type FlagsRecipe struct {
	// Sub are the sub-command words whose flags a launch takes, before Args
	// (Goose's `session`); empty for an agent launched without one. They are
	// the recipe's own, never words of the catalog entry, which an admin
	// edits.
	Sub []string
	// Args print the help (`--help`).
	Args []string
	// Taken are the flags a launch by Conductor sets itself, which a person's
	// second one would undo or lose (Claude Code honours only the last
	// --settings; Codex's --cd moves it out of the folder the launch
	// checked): set launch.
	Taken []string
	// Skips are the flags, or flag=value, that turn the agent's questions
	// off: set yolo, like the yolo recipe's own flags, so that the list
	// leaves them to the Yolo switch.
	Skips []string
}

// Flag is one option an agent's help lists.
type Flag struct {
	// Name is its first spelling with two dashes, else its only one; Aliases
	// its other spellings (at most maxFlagAliases).
	Name    string   `json:"name"`
	Aliases []string `json:"aliases,omitempty"`
	// Value says whether it takes a value: none, required, optional, or
	// unknown for a help that writes no placeholder on any row.
	Value string `json:"value"`
	// Placeholder is the help's own word for the value, without its
	// brackets and dots; Repeat says the help writes it as a list (dots).
	Placeholder string `json:"placeholder,omitempty"`
	Repeat      bool   `json:"repeat,omitempty"`
	// About is the first paragraph of the help's own words for it, on one
	// line, cut at maxFlagAbout bytes.
	About string `json:"about,omitempty"`
	// Choices are the values the help lists for it; Examples the values it
	// gives in quotes after "e.g." (Claude Code's model names).
	Choices  []Choice `json:"choices,omitempty"`
	Examples []string `json:"examples,omitempty"`
	// Kind is choice (Choices), model or folder, or empty.
	Kind string `json:"kind,omitempty"`
	// Set says a launch by Conductor sets it, so a form does not offer it:
	// model (the adapter's model flag, which the form's Model field sets),
	// yolo (the yolo recipe's, or one that turns questions off), session
	// (the session recipe's own id), launch (FlagsRecipe.Taken), exit
	// (--help and --version end the program at once). Empty otherwise.
	Set string `json:"set,omitempty"`
}

// Choice is one value a help lists for a flag, with its words when the help
// gives them; Set is yolo for a value that turns the agent's questions off
// (FlagsRecipe.Skips).
type Choice struct {
	Value string `json:"value"`
	About string `json:"about,omitempty"`
	Set   string `json:"set,omitempty"`
}

// Value, Kind and Set words.
const (
	ValueNone     = "none"
	ValueRequired = "required"
	ValueOptional = "optional"
	ValueUnknown  = "unknown"

	KindChoice = "choice"
	KindModel  = "model"
	KindFolder = "folder"

	SetModel   = "model"
	SetYolo    = "yolo"
	SetSession = "session"
	SetLaunch  = "launch"
	SetExit    = "exit"
)

// The bounds of what a help read keeps.
const (
	// MaxHelp is the most of a help that is read: a longer one is not read
	// at all (ErrHelpTooLong).
	MaxHelp = 128 << 10

	maxHelpLine       = 4096
	maxFlags          = 256
	maxFlagAliases    = 4
	maxFlagHolder     = 40
	maxFlagAbout      = 240
	maxFlagChoices    = 32
	maxChoiceAbout    = 120
	maxFlagExamples   = 8
	maxHolderInHelp   = 60
	maxPlainListWords = 12
)

var (
	// ErrHelpTooLong is a help past MaxHelp.
	ErrHelpTooLong = errors.New("the help is longer than Conductor reads")
	// ErrNoFlags is a help with no options section, or none with a row.
	ErrNoFlags = errors.New("the help lists no flags Conductor can read")
)

// helpTimeout bounds a help read: the program is killed past it. A variable
// as probeTimeout is; ProbeProcess.Timeout overrides it.
var helpTimeout = 5 * time.Second

// HelpArgs are the arguments, after the agent's command, that print the
// flags a launch of a takes: its adapter recipe's Sub words, then its Args.
// false for an agent whose adapter has no FlagsRecipe. Nothing of the
// catalog entry but its adapter's name is read: an admin edits the entry's
// start arguments and clones it, and they never reach this argv.
func HelpArgs(a catalog.Agent) ([]string, bool) {
	ad, ok := Get(a.Adapter)
	if !ok || ad.Flags == nil || len(ad.Flags.Args) == 0 {
		return nil, false
	}
	return helpArgsOf(ad.Flags), true
}

// helpArgsOf is a recipe's Sub words then its Args, in a slice of its own.
func helpArgsOf(r *FlagsRecipe) []string {
	return append(slices.Clone(r.Sub), r.Args...)
}

// Help runs command with args after it (HelpArgs) and reads what it
// printed, as a probe runs (ProbeProcess.Run: argv only, no stdin, a
// temporary folder, the spare environment plus env, a process group that is
// killed, ended by End) within helpTimeout. Its standard output and standard
// error are ONE regular file in the run's folder, never a pipe: a CLI that
// exits before a pipe has taken its help (Claude Code, read so on a Mac)
// writes a file whole, and Antigravity prints its help on standard error.
// The file is read after the program is reaped, to MaxHelp bytes: a longer
// help is res.TooLong, and none of it is returned. Any exit status counts.
func (r *ProbeProcess) Help(ctx context.Context, command, args []string, env map[string]string) (string, ProbeResult) {
	timeout := helpTimeout
	if r.Timeout > 0 {
		timeout = r.Timeout
	}
	var text string
	var tooLong bool
	var readErr error
	const name = "help.txt"
	res := r.exec(ctx, timeout, command, args, env, func(cmd *exec.Cmd, dir string) (func(), error) {
		f, err := os.OpenFile(filepath.Join(dir, name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			return nil, err
		}
		cmd.Stdout, cmd.Stderr = f, f
		return func() { _ = f.Close() }, nil
	}, func(dir string) {
		f, err := os.Open(filepath.Join(dir, name))
		if err != nil {
			readErr = err
			return
		}
		defer f.Close()
		b, err := io.ReadAll(io.LimitReader(f, MaxHelp+1))
		switch {
		case err != nil:
			readErr = err
		case len(b) > MaxHelp:
			tooLong = true
		default:
			text = string(b)
		}
	})
	switch {
	case !res.Ran:
	case readErr != nil:
		res = ProbeResult{Error: "the help could not be read: " + readErr.Error()}
	case tooLong:
		res.TooLong, res.Error = true, ErrHelpTooLong.Error()
	}
	return text, res
}

// The rules a help is read by. Each is strict: a line that is not exactly a
// row is not one, and a list that is not exactly one of these forms gives
// no values. What it cannot read it leaves out; nothing is guessed.
var (
	// R1: a section of options opens at column 0.
	flagSection = regexp.MustCompile(`^(?i:options|flags|global options|usage of \S+):\s*$`)
	// R2: an option row: 2 to 8 spaces, spellings joined by ", ", an
	// optional placeholder after one space or =, then the line's end or two
	// spaces and words.
	flagRow = regexp.MustCompile(`^ {2,8}(` + flagSpelling + `(?:, ` + flagSpelling + `)*)` +
		`(?:[ =](\[<[^<>\[\]]+>\](?:\.\.\.)?|<[^<>]+>(?:\.\.\.)?|\[[^\[\]<>]+\](?:\.\.\.)?))?` +
		`(?:$|\s{2,}(\S.*)$)`)
	// R5 (1): commander's (choices: "a", "b", default: "a").
	choicesQuoted = regexp.MustCompile(`\(choices:\s*((?:"[^"]*"(?:,\s*)?)+)`)
	quotedValue   = regexp.MustCompile(`"([^"]*)"`)
	// R5 (2): clap's [possible values: a, b].
	possibleValues = regexp.MustCompile(`\[possible values:\s*([^\]]+)\]`)
	// R5 (3): clap's long form, a "Possible values:" line, then "- value: words".
	possibleBlock = regexp.MustCompile(`^\s*Possible values:\s*$`)
	possibleItem  = regexp.MustCompile(`^\s*- ([A-Za-z0-9][A-Za-z0-9_.:/@-]*):\s*(.*)$`)
	// R5 (4): a group in round brackets of nothing but 2 to 12 plain
	// lower-case words, each ending in a letter or digit (so `e.g.` is none).
	plainGroup = regexp.MustCompile(`\(([^()]*)\)`)
	plainWord  = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9_.-]*[a-z0-9])?$`)
	plainSplit = regexp.MustCompile(`, or |, | or | ?\| ?`)
	// R6: (e.g. 'a', 'b', or 'c').
	examplesGroup = regexp.MustCompile(`\(e\.g\.,? ([^()]*)\)`)
	exampleSplit  = regexp.MustCompile(`, or |, | or `)
	// A value Conductor offers: the shape a model's name takes too.
	flagValue = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:/@-]{0,63}$`)
	spaces    = regexp.MustCompile(`\s+`)
)

// flagSpelling is one spelling of a flag: two dashes and a name of at most
// 62 more bytes, or one dash and one letter or digit.
const flagSpelling = `(?:--[A-Za-z0-9][A-Za-z0-9_-]{0,61}|-[A-Za-z0-9])`

var folderHolders = []string{"dir", "directory", "directories", "folder", "folders"}

// ParseFlags reads the flags of an agent's help (Help) by the rules above:
// each option row of each options section, with its spellings, whether it
// takes a value, the help's own words, the values the help lists and the
// examples it gives, and what Conductor knows of it for agent a (Kind, Set).
// At most maxFlags rows are kept. ErrNoFlags for a help with no section or
// no row, ErrHelpTooLong for one past MaxHelp.
func ParseFlags(a catalog.Agent, help string) ([]Flag, error) {
	if len(help) > MaxHelp {
		return nil, ErrHelpTooLong
	}
	lines := helpLines(help)
	type row struct {
		flag  Flag
		words []string // the row's text and the lines after it
	}
	var rows []row
	sections := 0
	in := false
	anyHolder := false
	for _, line := range lines {
		if flagSection.MatchString(line) {
			in = true
			sections++
			continue
		}
		if !in {
			continue
		}
		if line != "" && line[0] != ' ' && line[0] != '\t' {
			in = false
			continue
		}
		if m := flagRow.FindStringSubmatch(line); m != nil && len(m[2]) <= maxHolderInHelp {
			f := Flag{Value: ValueNone}
			names := strings.Split(m[1], ", ")
			f.Name = names[0]
			for _, n := range names {
				if strings.HasPrefix(n, "--") {
					f.Name = n
					break
				}
			}
			for _, n := range names {
				if n != f.Name && len(f.Aliases) < maxFlagAliases {
					f.Aliases = append(f.Aliases, n)
				}
			}
			if holder := m[2]; holder != "" {
				anyHolder = true
				f.Value = ValueRequired
				if holder[0] == '[' {
					f.Value = ValueOptional
				}
				f.Repeat = strings.Contains(holder, "...")
				holder = strings.ReplaceAll(holder, "...", "")
				f.Placeholder = cutBytes(strings.Trim(holder, "[]<>"), maxFlagHolder, "")
			}
			r := row{flag: f}
			if m[3] != "" {
				r.words = append(r.words, m[3])
			}
			rows = append(rows, r)
			continue
		}
		if len(rows) > 0 {
			rows[len(rows)-1].words = append(rows[len(rows)-1].words, line)
		}
	}
	if sections == 0 || len(rows) == 0 {
		return nil, ErrNoFlags
	}
	if len(rows) > maxFlags {
		rows = rows[:maxFlags]
	}
	k := knownOf(a)
	out := make([]Flag, 0, len(rows))
	for _, r := range rows {
		f := r.flag
		if !anyHolder {
			f.Value = ValueUnknown
		}
		about, block, rest := splitWords(r.words)
		f.About = cutBytes(about, maxFlagAbout, "…")
		f.Choices = choicesOf(f, about+" "+rest, block)
		if f.Value != ValueNone {
			f.Examples = examplesOf(about + " " + rest)
		}
		f.Kind = kindOf(f, k)
		f.Set, f.Choices = k.set(f)
		out = append(out, f)
	}
	return out, nil
}

// helpLines is R0: carriage returns dropped, escape sequences removed,
// invalid UTF-8 replaced, each line cut at maxHelpLine bytes.
func helpLines(help string) []string {
	help = strings.ToValidUTF8(help, "�")
	help = strings.ReplaceAll(help, "\r", "")
	help = stripANSI(help)
	lines := strings.Split(help, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(cutBytes(l, maxHelpLine, ""), " \t")
	}
	return lines
}

// splitWords is R4: about is the first paragraph of a row's words, on one
// line; block the values of a "Possible values:" block among them, each
// with its words; rest every other line, on one line, where the other forms
// of values are looked for.
func splitWords(words []string) (about string, block []Choice, rest string) {
	var first, others []string
	para := true
	inBlock := false
	for _, w := range words {
		t := strings.TrimSpace(w)
		if possibleBlock.MatchString(w) {
			inBlock, para = true, false
			continue
		}
		if inBlock {
			if m := possibleItem.FindStringSubmatch(w); m != nil {
				block = append(block, Choice{Value: m[1], About: m[2]})
				continue
			}
			if t != "" && len(block) > 0 {
				// A wrapped line of the value above.
				block[len(block)-1].About += " " + t
				continue
			}
			inBlock = false
		}
		if t == "" {
			if len(first) > 0 {
				para = false
			}
			continue
		}
		if para {
			first = append(first, t)
		} else {
			others = append(others, t)
		}
	}
	for i := range block {
		block[i].About = cutBytes(oneLine(block[i].About), maxChoiceAbout, "…")
	}
	return oneLine(strings.Join(first, " ")), block, oneLine(strings.Join(others, " "))
}

func oneLine(s string) string { return strings.TrimSpace(spaces.ReplaceAllString(s, " ")) }

// choicesOf is R5: the first form that gives any values.
func choicesOf(f Flag, words string, block []Choice) []Choice {
	var vals []Choice
	if m := choicesQuoted.FindStringSubmatch(words); m != nil {
		for _, q := range quotedValue.FindAllStringSubmatch(m[1], -1) {
			vals = append(vals, Choice{Value: q[1]})
		}
	}
	if len(vals) == 0 {
		if m := possibleValues.FindStringSubmatch(words); m != nil {
			for _, v := range strings.Split(m[1], ",") {
				vals = append(vals, Choice{Value: strings.TrimSpace(v)})
			}
		}
	}
	if len(vals) == 0 {
		vals = block
	}
	if len(vals) == 0 && f.Value != ValueNone {
		groups := plainGroup.FindAllStringSubmatch(words, -1)
		if n := len(groups); n > 0 {
			items := plainSplit.Split(groups[n-1][1], -1)
			plain := len(items) >= 2 && len(items) <= maxPlainListWords
			for _, it := range items {
				plain = plain && plainWord.MatchString(it)
			}
			if plain {
				for _, it := range items {
					vals = append(vals, Choice{Value: it})
				}
			}
		}
	}
	out := make([]Choice, 0, len(vals))
	for _, c := range vals {
		if flagValue.MatchString(c.Value) && !slices.ContainsFunc(out, func(o Choice) bool { return o.Value == c.Value }) && len(out) < maxFlagChoices {
			out = append(out, c)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// examplesOf is R6: the quoted values of a group that starts "e.g.", when
// it holds nothing else.
func examplesOf(words string) []string {
	m := examplesGroup.FindStringSubmatch(words)
	if m == nil {
		return nil
	}
	var out []string
	for _, it := range exampleSplit.Split(strings.TrimSpace(m[1]), -1) {
		if len(it) < 3 || (it[0] != '\'' && it[0] != '"') || it[len(it)-1] != it[0] {
			return nil
		}
		v := it[1 : len(it)-1]
		if !flagValue.MatchString(v) {
			return nil
		}
		if !slices.Contains(out, v) {
			out = append(out, v)
		}
	}
	if len(out) > maxFlagExamples {
		out = out[:maxFlagExamples]
	}
	return out
}

// known is what Conductor holds of an agent's flags apart from its help.
type known struct {
	choosesModel func([]string) bool
	yolo         []string            // flags that are set yolo
	yoloValues   map[string][]string // flag -> its values that are set yolo
	session      []string
	taken        []string
}

func knownOf(a catalog.Agent) known {
	k := known{yoloValues: map[string][]string{}}
	ad, ok := Get(a.Adapter)
	if ok {
		k.choosesModel = ad.ChoosesModel
		if ad.Flags != nil {
			k.taken = ad.Flags.Taken
			for _, s := range ad.Flags.Skips {
				if name, value, has := strings.Cut(s, "="); has {
					k.yoloValues[name] = append(k.yoloValues[name], value)
				} else {
					k.yolo = append(k.yolo, s)
				}
			}
		}
	}
	if a.Yolo != nil {
		k.yolo = append(k.yolo, flagWords(a.Yolo.Args)...)
	}
	if a.Session != nil {
		k.session = flagWords(a.Session.StartArgs)
	}
	return k
}

// flagWords are the flags of args, each the part before any =.
func flagWords(args []string) []string {
	var out []string
	for _, x := range args {
		if strings.HasPrefix(x, "-") {
			out = append(out, strings.SplitN(x, "=", 2)[0])
		}
	}
	return out
}

func spellings(f Flag) []string { return append([]string{f.Name}, f.Aliases...) }

func (k known) model(f Flag) bool {
	if k.choosesModel == nil {
		return false
	}
	for _, n := range spellings(f) {
		if k.choosesModel([]string{n}) {
			return true
		}
	}
	return false
}

// kindOf is R7.
func kindOf(f Flag, k known) string {
	holder := strings.ToLower(f.Placeholder)
	switch {
	case len(f.Choices) > 0:
		return KindChoice
	case k.model(f), holder == "model", f.Value == ValueUnknown && f.Name == "--model":
		return KindModel
	case slices.Contains(folderHolders, holder),
		f.Value != ValueNone && (strings.HasSuffix(f.Name, "-dir") || f.Name == "--cd"):
		return KindFolder
	}
	return ""
}

// set is R8: what a launch by Conductor sets of f, and f's choices with
// those that turn the agent's questions off marked yolo.
func (k known) set(f Flag) (string, []Choice) {
	names := spellings(f)
	has := func(list []string) bool {
		return slices.ContainsFunc(names, func(n string) bool { return slices.Contains(list, n) })
	}
	for _, n := range names {
		for _, v := range k.yoloValues[n] {
			for i := range f.Choices {
				if f.Choices[i].Value == v {
					f.Choices[i].Set = SetYolo
				}
			}
		}
	}
	switch {
	case has(k.yolo):
		return SetYolo, f.Choices
	case has(k.session):
		return SetSession, f.Choices
	case has(k.taken):
		return SetLaunch, f.Choices
	case f.Name == "--help" || f.Name == "--version":
		return SetExit, f.Choices
	case k.model(f):
		return SetModel, f.Choices
	}
	return "", f.Choices
}

// cutBytes cuts s to at most n bytes on a rune, with tail (counted within
// n) when it was cut.
func cutBytes(s string, n int, tail string) string {
	if len(s) <= n {
		return s
	}
	cut := n - len(tail)
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return strings.TrimRight(s[:cut], " ") + tail
}
