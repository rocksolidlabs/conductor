package session

import (
	"strings"
	"time"

	"github.com/rocksolidlabs/conductor/internal/proto"
)

// AttentionState says whether the agent in a session is waiting for a human.
type AttentionState string

const (
	AttentionNone       AttentionState = ""
	AttentionWorking    AttentionState = "working"
	AttentionNeedsInput AttentionState = "needs_input"
	AttentionDone       AttentionState = "done"
)

// Valid reports whether s is a known state.
func (s AttentionState) Valid() bool {
	switch s {
	case AttentionNone, AttentionWorking, AttentionNeedsInput, AttentionDone:
		return true
	}
	return false
}

// Attention sources.
const (
	SourceAPI     = "api"
	SourceBell    = "bell"
	SourceOSC     = "osc"
	SourcePattern = "pattern" // the last line of the screen matched Options.Pattern
	SourceInput   = "input"
	SourceAdmin   = "admin"
	SourceTrust   = "trust" // the agent's workspace-trust question is on the screen (Options.TrustPattern)
	// SourceQuestion: a question the agent asks during its work is on the
	// screen (Options.Questions marked Anytime: Codex's command approval).
	SourceQuestion = "question"
	// SourceSubmit: a submission's text is typed and its Enter was not
	// written, its link no longer opening the session; the session's next
	// submission waits for a person to type into the terminal (HeldWords).
	SourceSubmit = "submit"
	// SourceTitle: the agent's window title says it works (Options.WorkingTitle:
	// Codex's "⠋ Working").
	SourceTitle = "title"
	// SourceStatus: the agent's program status, a state it declares in an
	// OSC 7501 sequence (Codex 0.163 and later: working, blocked and what
	// blocks it), weighed as the window title is (Local.readStatus).
	SourceStatus = "status"
	// SourceScreen: the empty state after the screen moved on from a
	// question without an answer (withdrawn): a bell or OSC wait whose
	// screen changed past the snapshot's threshold (Local.screenQuiet), a
	// startup question or a dialog gone after keys typed in the terminal
	// reached it.
	SourceScreen = "screen"
)

// Attention kinds describe the shape of a needs-input prompt so clients can
// offer quick replies. An empty kind is a free-text prompt of unknown shape.
const (
	KindPermission = "permission" // numbered permission dialog (Claude Code)
	KindPrompt     = "prompt"     // free-text reply expected
	KindDone       = "done"       // agent finished its turn
)

// ValidKind reports whether k is a known attention kind (or empty).
func ValidKind(k string) bool {
	switch k {
	case "", KindPermission, KindPrompt, KindDone:
		return true
	}
	return false
}

// Option is one quick-reply choice: Input is exactly what a client sends as
// INPUT when the human picks it. Mode marks a choice that changes the
// session's permission mode (a dialog's "switch to auto mode", read off
// the screen: StartupQuestion.Mode): a page gives it no number key and
// never offers it first.
type Option struct {
	Label string `json:"label"`
	Input string `json:"input"`
	Mode  bool   `json:"mode,omitempty"`
}

// Limits for quick-reply options.
const (
	MaxAttentionOptions = 6
	MaxOptionLabel      = 60                   // runes
	MaxOptionInput      = proto.MaxAnswerInput // bytes: a choice of 40 and the CR that enters it, or a digit
)

// CleanOptions drops entries without a label or input, trims both to their
// limits and keeps at most MaxAttentionOptions. nil in, nil out.
func CleanOptions(in []Option) []Option {
	var out []Option
	for _, o := range in {
		label := CleanMessage(o.Label)
		if r := []rune(label); len(r) > MaxOptionLabel {
			// Cut with an ellipsis, where a word ends when one ends near
			// the bound, so that a long label reads as cut (Claude Code's
			// "Yes, and switch to BYPASS PERMISSIONS (no further prompts)
			// for this session", read off its dialog).
			cut := MaxOptionLabel - 1
			for i := cut; i > MaxOptionLabel*2/3; i-- {
				if r[i] == ' ' {
					cut = i
					break
				}
			}
			label = strings.TrimSpace(string(r[:cut])) + "…"
		}
		input := o.Input
		if len(input) > MaxOptionInput {
			input = input[:MaxOptionInput]
		}
		if label == "" || input == "" {
			continue
		}
		out = append(out, Option{Label: label, Input: input, Mode: o.Mode})
		if len(out) == MaxAttentionOptions {
			break
		}
	}
	return out
}

// Attention is the current signal state of a session. ID names the change,
// as Since stamps it, new with each one that shows a state: the question a
// reply was shown for (Local.AnswerInput, Submission.Question), and the id of
// the question's line in the chat. Empty with the empty state.
// AnsweredTerminal is Attention.Answered for a question answered by keys
// typed in the session's terminal, shown still while the program's dialog
// stays on the screen.
const AnsweredTerminal = "terminal"

type Attention struct {
	State   AttentionState `json:"state"`
	Message string         `json:"message,omitempty"`
	Source  string         `json:"source,omitempty"`
	Since   *time.Time     `json:"since,omitempty"`
	Kind    string         `json:"kind,omitempty"`
	Options []Option       `json:"options,omitempty"`
	ID      string         `json:"id,omitempty"`
	// Answered marks the question shown that keys typed in its terminal
	// have answered (AnsweredTerminal): it stays until the screen shows it
	// gone, and no page offers it (#258). Set as the session reports its
	// attention (Local.attentionLocked), never stored.
	Answered string `json:"answered,omitempty"`
	// Plan is the path of the plan a question asks approval for (Claude
	// Code's ExitPlanMode, #289), as the agent named it, once the session
	// has seen it to be one of its plans (NotePlan): a page opens it in
	// the editor. Set on the question shown after the agent names it, and
	// gone with the next state.
	Plan string `json:"plan,omitempty"`
	// rows are the words of a question asked from the screen as its rows
	// (StartupQuestion.words), whole, which tell it from the next
	// (sameWords); Message is cut from them. Kept by the session, never
	// sent.
	rows string
	// list is the whole list of a dialog whose answers are read off the
	// screen, as it was asked (StartupQuestion.read): a reply is written
	// only while the screen shows that list (stillOnScreen). Kept by the
	// session, never sent.
	list string
	// ask marks the agent's own question (a needs_input report that says
	// so: Local.Ask), which the session keeps through its turn's done
	// (Local.own). Kept by the session, never sent to a browser.
	ask bool
	// Decision is the durable decision this question mirrors, "dr-<n>"
	// (Local.TryAskDecision), "" for any other question: answered only
	// through the decision (ErrDecisionQuestion), and kept through keys,
	// the screen and lines naming no decision. Not sent to viewers yet
	// (docs/plans/decisions.md, PR-5 sends it).
	Decision string `json:"-"`
	// approval marks a dialog read off the screen as a plan's approval (its
	// footer says so: planApprovalShown, #289): the one a late report of
	// that approval names the plan for, and the one that keeps the plan of
	// the report it takes the place of. Kept by the session, never sent.
	approval bool
}

// Asked reports whether a is the agent's own question (Local.Ask): what a
// host tells its server with the attention it shows, so that the server's
// copy keeps it through a done as the host's session does.
func (a Attention) Asked() bool { return a.ask }

// CleanName normalises a display name from a client: oneLine at
// proto.MaxNameLen runes (control characters dropped, surrounding space
// trimmed), and "guest" when nothing is left.
func CleanName(s string) string {
	if out := oneLine(s, proto.MaxNameLen); out != "" {
		return out
	}
	return "guest"
}

// MaxAttentionMessage bounds messages from any source.
const MaxAttentionMessage = 500

// Event is an attention signal found in terminal output.
type Event struct {
	Source  string // SourceBell, SourceOSC, SourceTitle or SourceStatus
	Message string // a title's text: "" when it was cut (Scanner); a status's state
	// Kind and App are a status's: what blocks the program ("permission",
	// "question", "auth", or none) and the program's name ("codex").
	Kind string
	App  string
}

// Program states a status declares (Event.Message with SourceStatus): Codex
// 0.163's idle, working and blocked, 0.164's done and error, clear as the
// program leaves the terminal, and StatusUnknown for a sequence Conductor
// does not understand (a state or kind it does not know, one cut at the
// capture): the program declared a state, and which is not known.
const (
	StatusIdle    = "idle"
	StatusWorking = "working"
	StatusBlocked = "blocked"
	StatusDone    = "done"
	StatusError   = "error"
	StatusClear   = "clear"
	StatusUnknown = "unknown"
)

// statusKinds are what a blocked status may name as blocking the program
// (Codex 0.164: an approval, a question form, a sign-in).
var statusKinds = map[string]bool{"permission": true, "question": true, "auth": true}

// parseStatus reads an OSC 7501 payload after its number and the ";":
// "key=value" fields joined by ":" ("state=blocked:app=codex:kind=question"),
// in any order, unknown fields ignored. It reports the state, the kind and
// the app, with StatusUnknown and no kind for a payload it does not
// understand: no state, a state or a kind it does not know.
func parseStatus(payload string) (state, kind, app string) {
	for _, field := range strings.Split(payload, ":") {
		k, v, ok := strings.Cut(field, "=")
		if !ok {
			continue
		}
		switch k {
		case "state":
			state = v
		case "kind":
			kind = v
		case "app":
			app = v
		}
	}
	switch state {
	case StatusIdle, StatusWorking, StatusBlocked, StatusDone, StatusError, StatusClear:
	default:
		return StatusUnknown, "", app
	}
	if kind != "" && !statusKinds[kind] {
		return StatusUnknown, "", app
	}
	if state != StatusBlocked {
		kind = ""
	}
	return state, kind, app
}

// Scanner finds terminal bells, OSC notification sequences and window titles
// in a byte stream. It keeps state across chunks so split sequences are still
// seen.
//
// Recognised:
//   - a bare BEL (0x07) outside an escape sequence          -> bell
//   - ESC ] 9 ; text (BEL | ESC \)                          -> osc (iTerm2 style)
//   - ESC ] 777 ; notify ; title ; body (BEL | ESC \)        -> osc (urxvt style)
//   - ESC ] 0 ; title (BEL | ESC \), and ESC ] 2 ; title    -> title, its
//     text with control characters dropped; a title longer than the
//     sequence's capture (maxOSCCapture bytes with its number) is reported
//     with no text, as no title a pattern can match (Options.WorkingTitle)
//   - ESC ] 7501 ; state=S:app=A[:kind=K] (BEL | ESC \)      -> status, the
//     program's state (parseStatus); one not understood or cut at the
//     capture is StatusUnknown
//
// Other OSC sequences (hyperlinks, colours) are skipped; a BEL that
// terminates one of them, or a title, is not a bell.
type Scanner struct {
	state   scanState
	param   []byte // OSC number and payload, bounded
	dropped bool
	// carried says the last Scan's first event ends a sequence begun
	// before it; through, that the sequence it left unfinished began
	// before it (Carried, Through).
	carried, through bool
}

// Carried reports whether the first event the last Scan returned ends a
// sequence begun in an earlier one (an OSC notification split between two
// reads).
func (s *Scanner) Carried() bool { return s.carried }

// Through reports whether the last Scan ended inside a sequence begun in
// an earlier one: none of its bytes ended it.
func (s *Scanner) Through() bool { return s.through }

type scanState int

const (
	scanText   scanState = iota
	scanEsc              // saw ESC
	scanOSC              // inside ESC ] ...
	scanOSCEsc           // inside OSC, saw ESC (expecting \ for ST)
	scanCSI              // inside ESC [ ... until a final byte 0x40-0x7E
)

const maxOSCCapture = 256

// Scan consumes p and returns any events found in it.
func (s *Scanner) Scan(p []byte) []Event {
	var events []Event
	// carry: the sequence under way began before p and has not ended.
	carry := s.state != scanText
	s.carried = false
	finish := func() {
		n := len(events)
		events = s.finishOSC(events)
		if carry && len(events) > n {
			s.carried = true
		}
	}
	for _, b := range p {
		if s.state == scanText {
			carry = false
		}
		switch s.state {
		case scanText:
			switch b {
			case 0x07:
				events = append(events, Event{Source: SourceBell})
			case 0x1b:
				s.state = scanEsc
			}
		case scanEsc:
			switch b {
			case ']':
				s.state = scanOSC
				s.param = s.param[:0]
				s.dropped = false
			case '[':
				s.state = scanCSI
			default:
				// Two-byte escapes and anything else end here.
				s.state = scanText
			}
		case scanCSI:
			if b >= 0x40 && b <= 0x7e {
				s.state = scanText
			}
		case scanOSC:
			switch b {
			case 0x07:
				finish()
			case 0x1b:
				s.state = scanOSCEsc
			default:
				if len(s.param) < maxOSCCapture {
					s.param = append(s.param, b)
				} else {
					s.dropped = true
				}
			}
		case scanOSCEsc:
			if b == '\\' {
				finish()
			} else {
				// Not a string terminator; treat ESC as the start of a new sequence.
				s.state = scanEsc
				s.param = s.param[:0]
				carry = false
			}
		}
	}
	s.through = carry && s.state != scanText
	return events
}

func (s *Scanner) finishOSC(events []Event) []Event {
	s.state = scanText
	payload := string(s.param)
	num, rest, _ := strings.Cut(payload, ";")
	switch num {
	case "9":
		events = append(events, Event{Source: SourceOSC, Message: trimMessage(rest)})
	case "0", "2":
		// The window title (OSC 0 sets the icon name too). Cut at the
		// capture, its end is unknown: no text.
		title := ""
		if !s.dropped {
			title = cleanTitle(rest)
		}
		events = append(events, Event{Source: SourceTitle, Message: title})
	case "7501":
		// The program's status (Codex 0.163 and later). One cut at the
		// capture is a state declared and not known.
		state, kind, app := StatusUnknown, "", ""
		if !s.dropped {
			state, kind, app = parseStatus(rest)
		}
		events = append(events, Event{Source: SourceStatus, Message: state, Kind: kind, App: app})
	case "777":
		parts := strings.SplitN(rest, ";", 3)
		if len(parts) >= 1 && parts[0] == "notify" {
			var msg string
			if len(parts) == 3 {
				msg = strings.TrimSpace(parts[1]) + ": " + strings.TrimSpace(parts[2])
			} else if len(parts) == 2 {
				msg = parts[1]
			}
			events = append(events, Event{Source: SourceOSC, Message: trimMessage(msg)})
		}
	}
	s.param = s.param[:0]
	return events
}

// cleanTitle is a window title's text without control characters (C0, DEL
// and C1), as given otherwise: what the agent wrote.
func cleanTitle(t string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r >= 0x7f && r < 0xa0 {
			return -1
		}
		return r
	}, t)
}

func trimMessage(m string) string {
	m = strings.TrimSpace(strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, m))
	if len(m) > 200 {
		m = m[:200]
	}
	return m
}

// CleanMessage bounds and strips control characters from a user or agent
// supplied attention message.
func CleanMessage(m string) string {
	m = strings.Map(func(r rune) rune {
		if (r < 0x20 && r != '\n' && r != '\t') || r == 0x7f {
			return -1
		}
		return r
	}, m)
	if len(m) > MaxAttentionMessage {
		m = m[:MaxAttentionMessage]
	}
	return strings.TrimSpace(m)
}
