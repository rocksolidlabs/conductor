package brakeman

import (
	"fmt"
	"path"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// MaxRef is a ref's bound, in bytes.
const MaxRef = 512

// The kinds of a ref (docs/plans/brakeman.md, Evidence references): what
// it names, each with its own parts.
const (
	RefRun      = "run"      // run:<run>
	RefTask     = "task"     // task:<run>/<task>
	RefAttempt  = "attempt"  // attempt:<run>/<task>/<n>
	RefCheck    = "check"    // check:<run>/<task>/<n>/<id>/<k>
	RefCommand  = "command"  // command:<run>/<command>
	RefEvent    = "event"    // event:<run>/<seq>
	RefMessage  = "message"  // message:<run>/<seq>
	RefSession  = "session"  // session:<id>
	RefQuestion = "question" // question:<session>/<question>
	RefTicket   = "ticket"   // ticket:<KEY-n>
	RefComment  = "comment"  // comment:<KEY-n>/<id>
	RefOp       = "op"       // op:<actor>/<opId>
	RefCommit   = "commit"   // commit:<run>/<sha>
	RefFile     = "file"     // file:<session>/<path>, the path absolute
	RefBrief    = "brief"    // brief:<br-n>/<item>
)

// The shapes of a ref's parts, each the one its own package gives it,
// named beside it; internal/brakeman imports none of those packages, so
// they are written here.
var (
	// A run id: a crew id, a dash and eight hex digits (crew.ValidRunID).
	runIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}-[0-9a-f]{8}$`)
	// A task id and a check id (crew's taskIDPattern).
	taskIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,39}$`)
	// A run command's id (crew.CommandIDPattern) and a Dispatch op id
	// (dispatch.OpIDPattern): one pattern.
	opIDPattern = regexp.MustCompile(`^[A-Za-z0-9._~-]{1,32}$`)
	// A session, viewer, question or Dispatch row id: 16 lower-case hex
	// digits (session.NewID, dispatch.ValidID).
	hexIDPattern = regexp.MustCompile(`^[0-9a-f]{16}$`)
	// A ticket's key: its project's key (dispatch.KeyPattern), a dash and
	// its number of at most 18 digits (dispatch.ParseKey).
	ticketKeyPattern = regexp.MustCompile(`^[A-Z][A-Z0-9]{1,5}-[1-9][0-9]{0,17}$`)
	// A full git object id: SHA-1 or SHA-256.
	commitPattern = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)
	// A request's id.
	requestIDPattern = regexp.MustCompile(`^br-[1-9][0-9]{0,17}$`)
)

// maxActor is a Dispatch actor's bound (dispatch.MaxActor).
const maxActor = 60

// Ref is an evidence reference, parsed once: its kind and the parts that
// kind has (the rest empty or zero). It prints as it was parsed (String),
// and is written in JSON as that string.
type Ref struct {
	Kind     string
	Run      string // run, task, attempt, check, command, event, message, commit
	Task     string // task, attempt, check
	N        int    // attempt, check: the attempt's number, from 1
	Check    string // check: the check's id
	K        int    // check: the run of that check on that attempt, from 1
	Command  string // command
	Seq      int64  // event, message: from 1
	Session  string // session, question, file
	Question string // question
	Ticket   string // ticket, comment: KEY-n
	Comment  string // comment
	Actor    string // op
	Op       string // op
	Commit   string // commit
	Path     string // file: absolute and clean
	Request  string // brief: br-n
	Item     int    // brief: the item's index, from 0
}

// RefError is a ref that does not parse: the ref and why.
type RefError struct {
	Ref string
	Why string
}

func (e *RefError) Error() string {
	r := e.Ref
	if len(r) > 80 {
		r = r[:80] + "…"
	}
	return fmt.Sprintf("ref %q: %s", r, e.Why)
}

// ParseRef parses s by the grammar of docs/plans/brakeman.md (Evidence
// references): a kind, a colon and that kind's parts, at most MaxRef
// bytes, every number written without a sign or a leading zero, so that
// a ref parsed prints back as it was written.
func ParseRef(s string) (Ref, error) {
	bad := func(why string, a ...any) (Ref, error) {
		return Ref{}, &RefError{Ref: s, Why: fmt.Sprintf(why, a...)}
	}
	if len(s) > MaxRef {
		return bad("more than %d bytes", MaxRef)
	}
	if !utf8.ValidString(s) {
		return bad("not UTF-8")
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) {
			return bad("a control character")
		}
	}
	kind, rest, ok := strings.Cut(s, ":")
	if !ok {
		return bad("no kind: a ref is kind:parts")
	}
	r := Ref{Kind: kind}
	parts := strings.Split(rest, "/")
	want := func(n int) bool { return len(parts) == n }
	switch kind {
	case RefRun:
		if !want(1) || !runIDPattern.MatchString(parts[0]) {
			return bad("want run:<run>")
		}
		r.Run = parts[0]
	case RefTask:
		if !want(2) || !runIDPattern.MatchString(parts[0]) || !taskIDPattern.MatchString(parts[1]) {
			return bad("want task:<run>/<task>")
		}
		r.Run, r.Task = parts[0], parts[1]
	case RefAttempt:
		if !want(3) || !runIDPattern.MatchString(parts[0]) || !taskIDPattern.MatchString(parts[1]) {
			return bad("want attempt:<run>/<task>/<n>")
		}
		n, ok := number(parts[2])
		if !ok {
			return bad("an attempt's number is a whole number from 1")
		}
		r.Run, r.Task, r.N = parts[0], parts[1], int(n)
	case RefCheck:
		if !want(5) || !runIDPattern.MatchString(parts[0]) || !taskIDPattern.MatchString(parts[1]) || !taskIDPattern.MatchString(parts[3]) {
			return bad("want check:<run>/<task>/<n>/<id>/<k>")
		}
		n, ok1 := number(parts[2])
		k, ok2 := number(parts[4])
		if !ok1 || !ok2 {
			return bad("an attempt's number and a check's run are whole numbers from 1")
		}
		r.Run, r.Task, r.N, r.Check, r.K = parts[0], parts[1], int(n), parts[3], int(k)
	case RefCommand:
		if !want(2) || !runIDPattern.MatchString(parts[0]) || !opIDPattern.MatchString(parts[1]) {
			return bad("want command:<run>/<command>")
		}
		r.Run, r.Command = parts[0], parts[1]
	case RefEvent, RefMessage:
		if !want(2) || !runIDPattern.MatchString(parts[0]) {
			return bad("want %s:<run>/<seq>", kind)
		}
		seq, ok := number(parts[1])
		if !ok {
			return bad("a sequence number is a whole number from 1")
		}
		r.Run, r.Seq = parts[0], seq
	case RefSession:
		if !want(1) || !hexIDPattern.MatchString(parts[0]) {
			return bad("want session:<id>, 16 hex digits")
		}
		r.Session = parts[0]
	case RefQuestion:
		if !want(2) || !hexIDPattern.MatchString(parts[0]) || !hexIDPattern.MatchString(parts[1]) {
			return bad("want question:<session>/<question>, each 16 hex digits")
		}
		r.Session, r.Question = parts[0], parts[1]
	case RefTicket:
		if !want(1) || !ticketKeyPattern.MatchString(parts[0]) {
			return bad("want ticket:<KEY-n>")
		}
		r.Ticket = parts[0]
	case RefComment:
		if !want(2) || !ticketKeyPattern.MatchString(parts[0]) || !hexIDPattern.MatchString(parts[1]) {
			return bad("want comment:<KEY-n>/<id>")
		}
		r.Ticket, r.Comment = parts[0], parts[1]
	case RefOp:
		// An actor may hold a slash; an op id never does.
		i := strings.LastIndexByte(rest, '/')
		if i < 0 {
			return bad("want op:<actor>/<opId>")
		}
		actor, op := rest[:i], rest[i+1:]
		if actor == "" || len(actor) > maxActor || strings.TrimSpace(actor) != actor || !opIDPattern.MatchString(op) {
			return bad("want op:<actor>/<opId>, the actor at most %d bytes", maxActor)
		}
		r.Actor, r.Op = actor, op
	case RefCommit:
		if !want(2) || !runIDPattern.MatchString(parts[0]) || !commitPattern.MatchString(parts[1]) {
			return bad("want commit:<run>/<sha>, the full object id")
		}
		r.Run, r.Commit = parts[0], parts[1]
	case RefFile:
		// The session, a slash, then the touched index's path, which is
		// absolute: file:<session>//home/me/repo/main.go.
		sess, p, ok := strings.Cut(rest, "/")
		if !ok || !hexIDPattern.MatchString(sess) || !strings.HasPrefix(p, "/") || path.Clean(p) != p {
			return bad("want file:<session>/<path>, the path absolute and clean")
		}
		r.Session, r.Path = sess, p
	case RefBrief:
		if !want(2) || !requestIDPattern.MatchString(parts[0]) {
			return bad("want brief:<br-n>/<item>")
		}
		item, ok := index(parts[1])
		if !ok || item >= MaxItems {
			return bad("an item is an index from 0 to %d", MaxItems-1)
		}
		r.Request, r.Item = parts[0], item
	default:
		return bad("no kind %q", kind)
	}
	return r, nil
}

// number reads a whole number from 1, written without a sign or a
// leading zero.
func number(s string) (int64, bool) {
	if s == "" || s[0] == '0' || len(s) > 18 {
		return 0, false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, false
		}
	}
	n, err := strconv.ParseInt(s, 10, 64)
	return n, err == nil && n >= 1
}

// index reads a whole number from 0, written without a sign or a leading
// zero.
func index(s string) (int, bool) {
	if s == "0" {
		return 0, true
	}
	n, ok := number(s)
	if !ok || n > 1<<20 {
		return 0, false
	}
	return int(n), true
}

// String prints the ref as ParseRef read it.
func (r Ref) String() string {
	itoa := strconv.Itoa
	switch r.Kind {
	case RefRun:
		return "run:" + r.Run
	case RefTask:
		return "task:" + r.Run + "/" + r.Task
	case RefAttempt:
		return "attempt:" + r.Run + "/" + r.Task + "/" + itoa(r.N)
	case RefCheck:
		return "check:" + r.Run + "/" + r.Task + "/" + itoa(r.N) + "/" + r.Check + "/" + itoa(r.K)
	case RefCommand:
		return "command:" + r.Run + "/" + r.Command
	case RefEvent, RefMessage:
		return r.Kind + ":" + r.Run + "/" + strconv.FormatInt(r.Seq, 10)
	case RefSession:
		return "session:" + r.Session
	case RefQuestion:
		return "question:" + r.Session + "/" + r.Question
	case RefTicket:
		return "ticket:" + r.Ticket
	case RefComment:
		return "comment:" + r.Ticket + "/" + r.Comment
	case RefOp:
		return "op:" + r.Actor + "/" + r.Op
	case RefCommit:
		return "commit:" + r.Run + "/" + r.Commit
	case RefFile:
		return "file:" + r.Session + "/" + r.Path
	case RefBrief:
		return "brief:" + r.Request + "/" + itoa(r.Item)
	}
	return ""
}

// IsZero reports a ref with no kind: none.
func (r Ref) IsZero() bool { return r.Kind == "" }

// MarshalText writes the ref as it prints; a zero ref is refused.
func (r Ref) MarshalText() ([]byte, error) {
	s := r.String()
	if s == "" {
		return nil, fmt.Errorf("brakeman: a ref of no kind %q", r.Kind)
	}
	return []byte(s), nil
}

// UnmarshalText parses a ref (ParseRef).
func (r *Ref) UnmarshalText(b []byte) error {
	p, err := ParseRef(string(b))
	if err != nil {
		return err
	}
	*r = p
	return nil
}
