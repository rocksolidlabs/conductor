// Package journal is the session journal (docs/plans/history.md, Program
// item HS1): one record of every session, kept as a JSON document
// in the data directory's history/sessions/<id>.json (the directory 0700,
// each file 0600) from the session's start: its agent and that agent's own
// session, its host and where it ran, its folder and commits, its launch,
// its crew, and the evidence Conductor saw pass through it (the lines
// Conductor typed into it, its questions and how each ended, its chat,
// what its agent reported, the files it reported touching). What a person
// types into a terminal or a reply box is never kept, nor a variable's
// value. A hosted session's record holds what its host reports of it,
// nothing of the server's machine, and is taken up again when its host
// registers the same session again (hosted.go).
//
// Like internal/dispatch and internal/brakeman it has no HTTP, no session
// and no process: it imports the standard library and internal/store
// alone, and the API is its caller, which maps a session's activity and
// chat onto Activity and ChatLine, reads its State, and answers EndCommit
// and Denied.
// The holder of history/owner.lock writes; another server reads the files.
//
// # Writers, removers and the rule between them
//
// A record is written by:
//
//   - Begin: a server session's record, written before Begin returns (a
//     write that fails is the writer's to try again).
//   - Settle: what is true of the session now, as its server reads it
//     from the session itself: its name, its agent's own session, its
//     crew, and its end once it has one. Its caller reconciles the record
//     with the session (internal/api's history_journal.go says when); a
//     State settled again changes nothing, and an end, once recorded,
//     stands, observed or lost.
//   - the streams: Note and Chat (the session's activity and chat, a file
//     it touched among them) and Tool. The promise: what a stream brings
//     is kept while its record is open and dropped, counted in Dropped,
//     once its end is recorded, whatever its time says. What the session
//     would have told of a question after its end is the end's: an
//     observed end ends every question still open in the record as
//     withdrawn, as the session's own end withdraws it.
//   - the writer, outside the journal's lock: its end commit read
//     (EndCommit), its touches asked of Denied (fold), then the record
//     written (pass).
//
// An end, once observed, is written whether or not its commit could be
// read: the end commit is an enrichment, written when it arrives, never a
// condition of the end's write. A pass reads the end commits of the
// records it found ended before it writes, so an end and its commit
// usually go out together; an end settled while a pass reads is written
// by that pass without its commit, which the next pass reads and writes;
// and once Close has given up on the reads (closeWait), every end is
// written with the commit it has, none for a read not answered.
//   - the rule for a record with no instance (below): one whose file has
//     no end is marked lost and written so.
//
// and removed by Forget, ForgetAll and Prune (and by Abandon, for a
// session the registry refused).
//
// The rule every writer passes through: what is written is an instance's.
// Begin makes an instance of a record and answers it (Instance); every
// write names the instance it was begun for, never an id alone, and is
// taken only while that instance is the record of its id in memory. A
// record stays in memory until its session's last State is in
// (State.Final) and its end is written, so no write of a live instance
// ever finds its record gone; nothing brings a record back from its file.
// The writer's work on a record, done outside the lock, is kept only
// while that record is still the one in memory. A removal ends the
// instance it chooses, under the lock, at once and for good (choose). So,
// for each pair:
//
//   - a write and a removal: the write first, what it brought is in the
//     record and goes with it; the removal first, the write is dropped,
//     whatever holds its id by then.
//   - the writer and a removal: the removal first, the writer's work finds
//     its record gone from memory and keeps nothing of it; a write under
//     way ends before the removal's delete (wmu), and none follows it.
//   - a removal whose delete fails: it answers the error, and that answer
//     is the truth: the file stays as last written, its row is put back
//     from it, forgettable again, without an instance; what the ended
//     instance held unwritten is dropped, and nothing of it is ever
//     written. No later Begin takes its id.
//   - the load at Open and the rest: it runs before the first Begin. A row
//     read from a file has no instance: no write takes it.
//   - Close and the rest: the last pass writes every record; a write or a
//     Begin after Close is refused. A process killed instead loses what was
//     not yet written: the next Open marks each record still open on disk
//     lost, never ended.
//
// # A record with no instance is never open
//
// Wherever a record with no instance comes from (a delete that failed,
// the files read at Open, the files read once the owner lock is taken
// late), the journal that holds the owner lock lists one whose file has
// no end as lost (LostAt its file's last write, Ending lost, no EndedAt),
// holds it in memory, dirty, and writes that end at its next pass, Close's
// last among them. A read-only journal lists another server's files as
// they are: their sessions are that server's.
package journal

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

// The versions of the records this package writes. A record is written at
// the lowest version that holds it (versionOf): V while none of its
// resumes' receipts carries a Code, VCode, the version that added
// Resume.Code, once one does. So a server that knows version 1 alone still
// reads strictly every record without a failed receipt's code, and reads
// one with it as a later version's: as it is, never rewritten. This
// package reads both versions strictly, a version 1 record as before; a
// record of a version past VCode is read as it is and never rewritten.
const (
	V     = 1
	VCode = 2
	// maxV is the latest version this package reads strictly.
	maxV = VCode
)

// versionOf is the version r is written at: VCode when a receipt of its
// carries a Code, else V.
func versionOf(r *Record) int {
	for _, x := range r.Resumes {
		if x.Code != "" {
			return VCode
		}
	}
	return V
}

// The record's bounds. A record is at most MaxRecord bytes as store.Encode
// writes it: past that its oldest entries go first, then its oldest files,
// each counted in Dropped. What is neither an entry nor a file is bounded
// to fit beside them (the launch at MaxLaunch, MaxResumes receipts,
// MaxTools tools, every string at its own bound), so dropping entries and
// files always brings a record under MaxRecord.
const (
	MaxRecord   = 64 << 10
	MaxLaunch   = 16 << 10
	MaxResumes  = 16
	MaxTools    = 64
	MaxFiles    = 500
	MaxEnvNames = 64
	MaxRecords  = 2000 // ended records kept (Prune)
	MaxPage     = 100  // records a List page holds

	// An entry's texts: a message as the session bounds an activity
	// message, a chat line as the chat bounds one, a path, a URL and a
	// tool as an event's, a name as a display name.
	MaxMessage  = 500
	MaxChatLine = 2 << 10
	MaxPath     = 1024
	MaxURL      = 2048
	MaxTool     = 100
	MaxName     = 160
	MaxID       = 64
	MaxChoices  = 6
	MaxChoice   = 60 // runes
	MaxCwd      = 4096
	MaxBranch   = 200
	MaxArg      = 4096
	maxField    = 200 // an agent's or a host's field, a session's name
	maxResumeEr = 200
	// maxResumeCode bounds a failed receipt's code (Resume.Code).
	maxResumeCode = 32
	maxEnvName    = 128
	// maxHead is what all of a record but its entries and files takes at
	// most, every field at its bound (TestTheHeadAtEveryBoundLeavesRoom):
	// the rest of MaxRecord is its entries' and files'.
	maxHead = 42 << 10
)

// How a record ended without an observed end (Ending): the server stopped
// observing it, and whether its process ended is not known.
const (
	EndingLost     = "lost"      // a server session's record found open after a stop
	EndingHostAway = "host away" // a hosted session's: its host is not connected
)

// Where a record's start commit came from (Record.StartFrom).
const (
	StartFromLaunch  = "launch"  // read before the process started
	StartFromAttempt = "attempt" // the ledger's, for a managed run's attempt
)

// The kinds of a kept entry (Entry.Kind).
const (
	KindTyped    = "typed"    // a line Conductor typed: a crew's prompt, a broadcast, a handoff
	KindQuestion = "question" // the agent's question, with its choices
	KindAnswer   = "answer"   // how a question ended (Outcome), Ref the question's id
	KindChat     = "chat"     // a chat message, or a mark that one was sent to the agent
	KindReport   = "report"   // what the agent reported: Event is its type
	KindStatus   = "status"   // the session's status lines and its end
)

// How a question ended (Entry.Outcome of an answer).
const (
	OutcomeAnswered           = "answered"             // through Conductor: Message is the choice's label, or nothing for a line typed
	OutcomeAnsweredInTerminal = "answered_in_terminal" // keys typed in the terminal answered it: nothing of them kept
	OutcomeWithdrawn          = "withdrawn"            // the agent moved on
)

// The states of a resume's receipt (Resume.State).
const (
	ResumePending   = "pending"
	ResumeStarted   = "started"
	ResumeFailed    = "failed"
	ResumeUncertain = "uncertain"
)

// Record is one session's record, a JSON document of its own.
type Record struct {
	V   int    `json:"v"`
	ID  string `json:"id"`  // the session's id: 16 hex digits
	Seq int64  `json:"seq"` // from the journal's counter, bumped on every write
	// Name is the session's name as it is now; Kind its session kind
	// ("server", "hosted").
	Name  string `json:"name"`
	Kind  string `json:"kind"`
	Agent Agent  `json:"agent"`
	Host  Host   `json:"host"`
	// Where is the Runtime the session ran in: always set for a server
	// session ("native" or the launch's), a hosted one's as its host
	// reports it, "" only when it reported none (unknown, never native).
	Where string `json:"where,omitempty"`
	Cwd   string `json:"cwd"`
	// Repo is the worktree's top and RepoCommon the repository's common git
	// directory, made absolute: one for every linked worktree. Both, Branch
	// and Start are read before the process started, for a native server
	// session only (StartFrom); End after its end, an observation.
	Repo        string    `json:"repo,omitempty"`
	RepoCommon  string    `json:"repoCommon,omitempty"`
	Branch      string    `json:"branch,omitempty"`
	Start       string    `json:"startCommit,omitempty"`
	StartFrom   string    `json:"startFrom,omitempty"`
	End         string    `json:"endCommit,omitempty"`
	Launch      *Launch   `json:"launch,omitempty"` // server sessions only
	Crew        *Crew     `json:"crew,omitempty"`
	ResumedFrom string    `json:"resumedFrom,omitempty"`
	StartedAt   time.Time `json:"startedAt"`
	// EndedAt is an observed end only (an exit or a stop the server saw);
	// LostAt when the server stopped observing the session (EndingLost,
	// EndingHostAway), its process's state unknown.
	EndedAt  time.Time `json:"endedAt,omitzero"`
	LostAt   time.Time `json:"lostAt,omitzero"`
	Ending   string    `json:"ending,omitempty"`
	Reopened int       `json:"reopened,omitempty"` // a hosted record taken up again by its host
	Entries  []Entry   `json:"entries"`
	// Dropped counts the entries and files the bounds let go, oldest
	// first, and the entries noted after the end.
	Dropped int            `json:"dropped,omitempty"`
	Files   []File         `json:"files,omitempty"`
	Tools   map[string]int `json:"tools,omitempty"` // tool_use counts by tool
	Resumes []Resume       `json:"resumes,omitempty"`
}

// Agent is the session's agent: its catalog id and name, its adapter, its
// version (the server's probe of it; "" while unknown) and its own session.
type Agent struct {
	ID      string        `json:"id"`
	Name    string        `json:"name,omitempty"`
	Adapter string        `json:"adapter,omitempty"`
	Version string        `json:"version,omitempty"`
	Session *AgentSession `json:"session,omitempty"`
}

// AgentSession is the agent's own session as the session knows it.
type AgentSession struct {
	ID        string `json:"id"`
	Source    string `json:"source,omitempty"`
	Resumable bool   `json:"resumable"`
}

// Host is the machine the session ran on: the server's for a server
// session, its host's for a hosted one, whose Owner is the hash its hub
// keeps of the host's instance.
type Host struct {
	Name   string `json:"name,omitempty"`
	User   string `json:"user,omitempty"`
	Owner  string `json:"owner,omitempty"`
	Hosted bool   `json:"hosted,omitempty"`
}

// Launch is what a server session was launched with. Its arguments are
// kept only when the launch fits MaxLaunch (ArgsKept), else their count
// and hash alone; its variables never: their names (at most MaxEnvNames,
// past it their count alone) and one hash over the sorted name=value
// lines.
type Launch struct {
	Args       []string `json:"args,omitempty"`
	ArgCount   int      `json:"argCount"`
	ArgsSHA256 string   `json:"argsSha256,omitempty"`
	ArgsKept   bool     `json:"argsKept"`
	EnvNames   []string `json:"envNames,omitempty"`
	EnvCount   int      `json:"envCount,omitempty"`
	EnvSHA256  string   `json:"envSha256,omitempty"`
	Yolo       bool     `json:"yolo,omitempty"`
	Renamed    bool     `json:"renamed,omitempty"`
}

// Crew is the run the session is a member of.
type Crew struct {
	RunID    string `json:"runId"`
	CrewID   string `json:"crewId,omitempty"`
	Member   string `json:"member,omitempty"`
	Task     string `json:"task,omitempty"`
	Attempt  int    `json:"attempt,omitempty"`
	Brakeman string `json:"brakeman,omitempty"` // a Brakeman turn's request
}

// Entry is one kept entry, with the identity its source gave it: ID a chat
// message's id (a question's is its attention's), Ref the id of what it
// answers or withdraws, Event a report's type or a chat mark's.
type Entry struct {
	Seq     int64     `json:"seq"`
	At      time.Time `json:"at"`
	Kind    string    `json:"kind"`
	ID      string    `json:"id,omitempty"`
	Ref     string    `json:"ref,omitempty"`
	Event   string    `json:"event,omitempty"`
	ByName  string    `json:"byName,omitempty"`
	Message string    `json:"message,omitempty"`
	Op      string    `json:"op,omitempty"`
	Path    string    `json:"path,omitempty"`
	Tool    string    `json:"tool,omitempty"`
	URL     string    `json:"url,omitempty"`
	To      string    `json:"to,omitempty"`
	Choices []string  `json:"choices,omitempty"`
	Outcome string    `json:"outcome,omitempty"`
}

// File is one path and op the agent reported, folded: how often and when
// last.
type File struct {
	Path  string    `json:"path"`
	Op    string    `json:"op"`
	Count int       `json:"count"`
	Last  time.Time `json:"last"`
}

// Resume is the receipt of a resume from the record: its command id, its
// actor, the session id allocated for it, its state and why it failed.
type Resume struct {
	CommandID string `json:"commandId"`
	Actor     string `json:"actor,omitempty"`
	Session   string `json:"session,omitempty"`
	State     string `json:"state"`
	// Code is a failed receipt's refusal, the code its route answered
	// with; Error says why in words.
	// A record that holds one is written at VCode (versionOf).
	Code  string    `json:"code,omitempty"`
	Error string    `json:"error,omitempty"`
	At    time.Time `json:"at"`
}

// open says the record has no end, observed or not.
func (r *Record) open() bool { return r.EndedAt.IsZero() && r.LostAt.IsZero() }

// pending says the record holds a resume's receipt still pending.
func (r *Record) pending() bool {
	return slices.ContainsFunc(r.Resumes, func(x Resume) bool { return x.State == ResumePending })
}

// LaunchOf is the launch of args, env, yolo and renamed as a record keeps
// it: the arguments when the launch fits MaxLaunch, else their count and
// hash alone; the variables' names, sorted, and their hash, never a value.
func LaunchOf(args []string, env map[string]string, yolo, renamed bool) *Launch {
	l := &Launch{ArgCount: len(args), Yolo: yolo, Renamed: renamed}
	if len(args) > 0 {
		h := sha256.New()
		for _, a := range args {
			h.Write([]byte(a))
			h.Write([]byte{0})
		}
		l.ArgsSHA256 = hex.EncodeToString(h.Sum(nil))
	}
	if len(env) > 0 {
		names := make([]string, 0, len(env))
		for k := range env {
			names = append(names, k)
		}
		slices.Sort(names)
		h := sha256.New()
		for _, k := range names {
			h.Write([]byte(k + "=" + env[k] + "\n"))
		}
		l.EnvSHA256 = hex.EncodeToString(h.Sum(nil))
		l.EnvCount = len(names)
		if len(names) <= MaxEnvNames {
			for _, k := range names {
				l.EnvNames = append(l.EnvNames, fitPath(k, maxEnvName))
			}
		}
	}
	l.Args, l.ArgsKept = slices.Clone(args), true
	if encodedLen(l) > MaxLaunch {
		l.Args, l.ArgsKept = nil, false
	}
	if encodedLen(l) > MaxLaunch {
		l.EnvNames = nil
	}
	return l
}

// encodedLen is v's length as json.Marshal writes it, indented as a record
// nests it.
func encodedLen(v any) int {
	b, err := json.MarshalIndent(v, "  ", "  ")
	if err != nil {
		return 0
	}
	return len(b)
}

// cutBytes is s cut to at most n bytes at a rune boundary.
func cutBytes(s string, n int) string {
	s = strings.ToValidUTF8(s, "�")
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// cutRunes is s cut to at most n runes.
func cutRunes(s string, n int) string {
	s = strings.ToValidUTF8(s, "�")
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

// fitPath is s cut so that its JSON form, escapes included, is at most n
// bytes between its quotes: a path can hold quotes, backslashes and
// control characters, each written longer than it is.
func fitPath(s string, n int) string {
	s = strings.ToValidUTF8(s, "\uFFFD")
	size := 0
	for i, r := range s {
		size += escapedLen(r)
		if size > n {
			return s[:i]
		}
	}
	return s
}

// maxEscape is the most bytes one character takes in a JSON string
// (escapedLen): a control character's \u escape.
const maxEscape = 6

// CwdMayBeCut says a record's working directory may be the cut that
// fitRecord made of a longer one at MaxCwd, its JSON form, escapes
// included, being within maxEscape of the bound: fitPath stops only where
// the next character would pass it, so every cut one is, those of the
// records an earlier version wrote among them. Such a folder is not the
// session's: it may be its parent, or another folder. One that is whole
// and that long is judged cut all the same.
func CwdMayBeCut(cwd string) bool {
	size := 0
	for _, r := range cwd {
		size += escapedLen(r)
	}
	return size > MaxCwd-maxEscape
}

// escapedLen is how many bytes r takes in a JSON string as store.Encode
// writes one (HTML characters as they are).
func escapedLen(r rune) int {
	switch {
	case r == '"' || r == '\\' || r == '\n' || r == '\r' || r == '\t':
		return 2
	case r < 0x20 || r == 0x2028 || r == 0x2029:
		return 6
	}
	return utf8.RuneLen(r)
}

// fitRecord cuts every string of r that is not an entry, a file or the
// launch to its bound, and the launch's arguments to their own, so that
// the head of a record (all but its entries and files) is bounded.
func fitRecord(r *Record) {
	r.Name = fitPath(r.Name, maxField)
	r.Kind, r.Ending = fitPath(r.Kind, 16), fitPath(r.Ending, maxField)
	r.Agent.ID = fitPath(r.Agent.ID, maxField)
	r.Agent.Name = fitPath(r.Agent.Name, maxField)
	r.Agent.Adapter = fitPath(r.Agent.Adapter, maxField)
	r.Agent.Version = fitPath(r.Agent.Version, maxField)
	if s := r.Agent.Session; s != nil {
		c := *s
		c.ID, c.Source = fitPath(c.ID, maxField), fitPath(c.Source, 16)
		r.Agent.Session = &c
	}
	r.Host.Name = fitPath(r.Host.Name, maxField)
	r.Host.User = fitPath(r.Host.User, maxField)
	r.Host.Owner = fitPath(r.Host.Owner, maxField)
	r.Where = fitPath(r.Where, maxField)
	r.Cwd = fitPath(r.Cwd, MaxCwd)
	r.Repo = fitPath(r.Repo, MaxCwd)
	r.RepoCommon = fitPath(r.RepoCommon, MaxCwd)
	r.Branch = fitPath(r.Branch, MaxBranch)
	r.Start = fitPath(r.Start, 64)
	r.End = fitPath(r.End, 64)
	r.StartFrom = fitPath(r.StartFrom, 16)
	r.ResumedFrom = fitPath(r.ResumedFrom, MaxID)
	if c := r.Crew; c != nil {
		x := *c
		x.RunID, x.CrewID, x.Member, x.Task, x.Brakeman = fitPath(x.RunID, maxField), fitPath(x.CrewID, maxField), fitPath(x.Member, maxField), fitPath(x.Task, maxField), fitPath(x.Brakeman, maxField)
		r.Crew = &x
	}
	if l := r.Launch; l != nil && encodedLen(l) > MaxLaunch {
		x := *l
		x.Args, x.ArgsKept = nil, false
		if encodedLen(&x) > MaxLaunch {
			x.EnvNames = nil
		}
		r.Launch = &x
	}
	if len(r.Resumes) > MaxResumes {
		r.Resumes = r.Resumes[:MaxResumes]
	}
	for i := range r.Resumes {
		x := &r.Resumes[i]
		x.CommandID, x.Actor, x.Session = fitPath(x.CommandID, 32), fitPath(x.Actor, MaxID), fitPath(x.Session, 16)
		x.State, x.Code, x.Error = fitPath(x.State, 16), fitPath(x.Code, maxResumeCode), fitPath(x.Error, maxResumeEr)
	}
	if r.Entries == nil {
		r.Entries = []Entry{}
	}
}

// fitEntry cuts each of e's texts to its bound: a chat line's message at
// MaxChatLine, any other's at MaxMessage.
func fitEntry(e *Entry) {
	msg := MaxMessage
	if e.Kind == KindChat || e.Kind == KindQuestion {
		msg = MaxChatLine
	}
	e.Message = fitPath(e.Message, msg)
	e.ID, e.Ref = fitPath(e.ID, MaxID), fitPath(e.Ref, MaxID)
	e.Event, e.Outcome, e.Op = cutBytes(e.Event, 32), cutBytes(e.Outcome, 32), cutBytes(e.Op, 32)
	e.ByName = fitPath(cutRunes(e.ByName, 40), MaxName)
	e.Path = fitPath(e.Path, MaxPath)
	e.URL = fitPath(e.URL, MaxURL)
	e.Tool = fitPath(e.Tool, MaxTool)
	e.To = fitPath(cutRunes(e.To, 40), MaxName)
	if len(e.Choices) > MaxChoices {
		e.Choices = e.Choices[:MaxChoices]
	}
	for i, c := range e.Choices {
		e.Choices[i] = fitPath(cutRunes(c, MaxChoice), 4*MaxChoice)
	}
}
