package journal

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/store"
)

// quiet is a logger that says nothing.
var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

// openAt opens a journal over the data directory dir, closed when the test
// ends.
func openAt(t *testing.T, dir string, o Options) *Journal {
	t.Helper()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if o.Log == nil {
		o.Log = quiet
	}
	j, err := Open(st, o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { j.Close() })
	return j
}

func newJournal(t *testing.T, o Options) (*Journal, string) {
	t.Helper()
	dir := t.TempDir()
	return openAt(t, dir, o), dir
}

// sid is the session id of n.
func sid(n int) string { return fmt.Sprintf("%016x", n+0xa0) }

func serverRecord(id string) Record {
	return Record{ID: id, Name: "cat #1", Kind: "server", Agent: Agent{ID: "cat", Name: "cat"}, Host: Host{Name: "lab-box", User: "nate"}, Where: "native", Cwd: "/w"}
}

func hostedRecord(id, owner string) Record {
	return Record{ID: id, Name: "bash @ laptop", Kind: "hosted", Agent: Agent{ID: "bash"}, Host: Host{Name: "laptop", User: "ada", Owner: owner, Hosted: true}, Cwd: "/home/ada"}
}

// onDisk is the record of id as its file holds it, and the file's bytes.
func onDisk(t *testing.T, dir, id string) (Record, []byte) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, Dir, SessionsDir, id+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var r Record
	if err := json.Unmarshal(b, &r); err != nil {
		t.Fatal(err)
	}
	return r, b
}

func begin(t *testing.T, j *Journal, r Record) {
	t.Helper()
	if err := j.Begin(r); err != nil {
		t.Fatal(err)
	}
}

func kinds(r Record) []string {
	var out []string
	for _, e := range r.Entries {
		out = append(out, e.Kind)
	}
	return out
}

// A server session's record is on disk when Begin returns, before any
// entry; a hosted one's is written by the writer before the entries noted
// after it, at its first pass.
func TestARecordIsWrittenBeforeItsFirstEntry(t *testing.T) {
	j, dir := newJournal(t, Options{})
	begin(t, j, serverRecord(sid(1)))
	r, _ := onDisk(t, dir, sid(1))
	if r.ID != sid(1) || r.V != V || len(r.Entries) != 0 || r.Seq == 0 || r.StartedAt.IsZero() {
		t.Fatalf("the record as begun: %+v", r)
	}
	begin(t, j, hostedRecord(sid(2), "o1"))
	j.Note(sid(2), Activity{Type: actProgress, Message: "built"})
	j.Flush()
	h, _ := onDisk(t, dir, sid(2))
	if !h.Host.Hosted || h.Host.Owner != "o1" || len(h.Entries) != 1 || h.Entries[0].Message != "built" {
		t.Fatalf("the hosted record: %+v", h)
	}
}

// What a record keeps of a session's activity and chat is the evidence:
// lines Conductor typed, reports, status lines, questions and their ends,
// the chat's messages; never who joined, left or came through a link, an
// attention entry, nor a reply's input entry; tool uses counted.
func TestARecordKeepsEvidenceEntriesOnly(t *testing.T) {
	j, dir := newJournal(t, Options{})
	id := sid(1)
	begin(t, j, serverRecord(id))
	at := time.Date(2026, 11, 5, 9, 0, 0, 0, time.UTC)
	step := func() time.Time { at = at.Add(time.Second); return at }
	j.Note(id, Activity{At: step(), Type: "join", ByName: "Ada Viewer"})
	j.Note(id, Activity{At: step(), Type: "link", ByName: "a link named for Ada"})
	j.Note(id, Activity{At: step(), Type: "attention", Message: "needs input"})
	j.Note(id, Activity{At: step(), Type: actInput, By: "sub-1", ByName: "Ada", Message: "the question's words"})
	j.Note(id, Activity{At: step(), Type: actInput, ByName: "Conductor", Message: "build the thing", Op: opTyped})
	j.Note(id, Activity{At: step(), Type: actProgress, Message: "tests pass"})
	j.Note(id, Activity{At: step(), Type: actArtifact, URL: "https://example.test/x"})
	j.Note(id, Activity{At: step(), Type: actToolDenied, Tool: "Bash"})
	for range 3 {
		j.Note(id, Activity{At: step(), Type: actToolUse, Tool: "Edit"})
	}
	j.Note(id, Activity{At: step(), Type: "leave", ByName: "Ada Viewer"})
	j.Chat(id, ChatLine{ID: "m1", At: step(), Kind: chatSystem, Event: "join", ByName: "Ada Viewer"})
	j.Chat(id, ChatLine{ID: "m2", At: step(), Kind: chatMessage, ByName: "Ada", Text: "look at the login test"})
	j.Note(id, Activity{At: step(), Type: actStatus, Message: "exited (exit 0)"})
	j.Flush()
	r, raw := onDisk(t, dir, id)
	if want := []string{KindTyped, KindReport, KindReport, KindReport, KindChat, KindStatus}; !slices.Equal(kinds(r), want) {
		t.Fatalf("kinds %v, want %v", kinds(r), want)
	}
	if r.Tools["Edit"] != 3 || len(r.Tools) != 1 {
		t.Fatalf("tools %v", r.Tools)
	}
	for _, never := range []string{"Ada Viewer", "a link named for Ada", "the question's words", "needs input"} {
		if bytes.Contains(raw, []byte(never)) {
			t.Fatalf("the record keeps %q:\n%s", never, raw)
		}
	}
	if e := r.Entries[1]; e.Event != actProgress || e.Message != "tests pass" {
		t.Fatalf("the report %+v", e)
	}
}

// Two questions of the same words are two entries, each with its own id,
// and each answer names the one it ended.
func TestTwoQuestionsOfTheSameWordsAreTwoEntries(t *testing.T) {
	j, dir := newJournal(t, Options{})
	id := sid(1)
	begin(t, j, serverRecord(id))
	at := time.Date(2026, 11, 5, 9, 0, 0, 0, time.UTC)
	j.Chat(id, ChatLine{ID: "q1", At: at, Kind: chatQuestion, ByRole: "agent", Text: "Allow?", Choices: []string{"Yes", "No"}})
	j.Chat(id, ChatLine{ID: "a1", At: at.Add(1 * time.Second), Kind: chatSystem, Event: chatAnswered, Ref: "q1", Text: "Yes", ByName: "Ada"})
	j.Chat(id, ChatLine{ID: "q2", At: at.Add(2 * time.Second), Kind: chatQuestion, ByRole: "agent", Text: "Allow?", Choices: []string{"Yes", "No"}})
	j.Chat(id, ChatLine{ID: "a2", At: at.Add(3 * time.Second), Kind: chatSystem, Event: chatWithdrawn, Ref: "q2", Text: "agent"})
	j.Flush()
	r, _ := onDisk(t, dir, id)
	if len(r.Entries) != 4 {
		t.Fatalf("entries %+v", r.Entries)
	}
	q1, a1, q2, a2 := r.Entries[0], r.Entries[1], r.Entries[2], r.Entries[3]
	if q1.ID != "q1" || q2.ID != "q2" || !slices.Equal(q1.Choices, []string{"Yes", "No"}) || q1.Message != "Allow?" {
		t.Fatalf("the questions %+v %+v", q1, q2)
	}
	if a1.Ref != "q1" || a1.Outcome != OutcomeAnswered || a1.Message != "Yes" || a1.ByName != "Ada" {
		t.Fatalf("the first answer %+v", a1)
	}
	if a2.Ref != "q2" || a2.Outcome != OutcomeWithdrawn || a2.Message != "" {
		t.Fatalf("the second answer %+v", a2)
	}
}

// An entry an earlier callback still carried when the session's end was
// written (held at a gate here) is kept, in its place by its time; one
// after the end is dropped and counted.
func TestAnEarlierEntryHeldPastTheEndIsKept(t *testing.T) {
	j, dir := newJournal(t, Options{})
	id := sid(1)
	begin(t, j, serverRecord(id))
	at := time.Date(2026, 11, 5, 9, 0, 0, 0, time.UTC)
	j.Note(id, Activity{At: at, Type: actProgress, Message: "first"})
	gate := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		<-gate
		j.Note(id, Activity{At: at.Add(time.Second), Type: actProgress, Message: "held"})
	}()
	j.Note(id, Activity{At: at.Add(2 * time.Second), Type: actStatus, Message: "exited (exit 0)"})
	j.End(id, Ending{At: at.Add(2 * time.Second), How: "exited (exit 0)"})
	j.Flush()
	if r, _ := onDisk(t, dir, id); r.EndedAt.IsZero() || len(r.Entries) != 2 {
		t.Fatalf("the end as written: %+v", r)
	}
	close(gate)
	<-done
	j.Note(id, Activity{At: at.Add(3 * time.Second), Type: actProgress, Message: "too late"})
	j.Flush()
	r, _ := onDisk(t, dir, id)
	var msgs []string
	for _, e := range r.Entries {
		msgs = append(msgs, e.Message)
	}
	if !slices.Equal(msgs, []string{"first", "held", "exited (exit 0)"}) || r.Dropped != 1 || r.Ending != "exited (exit 0)" {
		t.Fatalf("entries %q dropped %d ending %q", msgs, r.Dropped, r.Ending)
	}
}

// End sets the end in memory and returns while the writer's disk is held:
// the caller (a sink, the registry's hook) never waits on a disk.
func TestEndWritesOffTheCallersGoroutine(t *testing.T) {
	j, dir := newJournal(t, Options{})
	begin(t, j, serverRecord(sid(1)))
	begin(t, j, serverRecord(sid(2)))
	entered := make(chan struct{}, 8)
	gate := make(chan struct{})
	beforeWrite = func(string) error {
		entered <- struct{}{}
		<-gate
		return nil
	}
	t.Cleanup(func() { beforeWrite = nil })
	j.End(sid(1), Ending{How: "stopped"})
	<-entered // the writer is at the disk, held
	j.End(sid(2), Ending{How: "exited (exit 0)"})
	j.Note(sid(2), Activity{Type: actProgress, Message: "noted while the disk is held"})
	j.Changed(sid(2))
	close(gate)
	j.Flush()
	beforeWrite = nil
	for _, id := range []string{sid(1), sid(2)} {
		if r, _ := onDisk(t, dir, id); r.EndedAt.IsZero() {
			t.Fatalf("%s did not end: %+v", id, r)
		}
	}
}

// A file the agent touched is one row per path and op, with its count
// and its last time.
func TestAFileFoldsIntoOneRowPerPathAndOp(t *testing.T) {
	j, dir := newJournal(t, Options{})
	id := sid(1)
	begin(t, j, serverRecord(id))
	at := time.Date(2026, 11, 5, 9, 0, 0, 0, time.UTC)
	j.Note(id, Activity{At: at, Type: actFile, Op: "read", Path: "/w/a.go"})
	j.Note(id, Activity{At: at.Add(time.Second), Type: actFile, Op: "read", Path: "/w/a.go"})
	j.Note(id, Activity{At: at.Add(2 * time.Second), Type: actFile, Op: "edit", Path: "/w/a.go"})
	j.Touch(id, File{Path: "/w/b.go", Op: "read", Last: at})
	j.Flush()
	r, _ := onDisk(t, dir, id)
	if len(r.Files) != 3 || len(r.Entries) != 0 {
		t.Fatalf("files %+v entries %+v", r.Files, r.Entries)
	}
	a := r.Files[0]
	if a.Path != "/w/a.go" || a.Op != "read" || a.Count != 2 || !a.Last.Equal(at.Add(time.Second)) {
		t.Fatalf("the read row %+v", a)
	}
	if r.Files[1].Op != "edit" || r.Files[1].Count != 1 || r.Files[2].Path != "/w/b.go" {
		t.Fatalf("the other rows %+v", r.Files[1:])
	}
}

// A path the session's deny list covers is never written.
func TestADenyListedPathIsNeverWritten(t *testing.T) {
	var asked []string
	var mu sync.Mutex
	j, dir := newJournal(t, Options{Denied: func(id, path string) bool {
		mu.Lock()
		defer mu.Unlock()
		asked = append(asked, id+" "+path)
		return strings.HasPrefix(path, "/data/")
	}})
	id := sid(1)
	begin(t, j, serverRecord(id))
	j.Note(id, Activity{Type: actFile, Op: "read", Path: "/data/conductor.json"})
	j.Note(id, Activity{Type: actFile, Op: "read", Path: "/w/main.go"})
	j.Flush()
	r, raw := onDisk(t, dir, id)
	if bytes.Contains(raw, []byte("conductor.json")) || len(r.Files) != 1 || r.Files[0].Path != "/w/main.go" {
		t.Fatalf("files %+v", r.Files)
	}
	mu.Lock()
	defer mu.Unlock()
	if !slices.Contains(asked, id+" /data/conductor.json") {
		t.Fatalf("Denied was asked %q", asked)
	}
}

// A launch's variables are kept by their names and one hash, never a
// value: the file holds no value.
func TestAVariableValueIsNeverWritten(t *testing.T) {
	j, dir := newJournal(t, Options{})
	id := sid(1)
	r := serverRecord(id)
	r.Launch = LaunchOf([]string{"--model", "x"}, map[string]string{"GOAL": "the-goal-value", "API_TOKEN": "hunter2-secret"}, true, false)
	begin(t, j, r)
	got, raw := onDisk(t, dir, id)
	for _, v := range []string{"hunter2-secret", "the-goal-value"} {
		if bytes.Contains(raw, []byte(v)) {
			t.Fatalf("the record holds the value %q:\n%s", v, raw)
		}
	}
	l := got.Launch
	if !slices.Equal(l.EnvNames, []string{"API_TOKEN", "GOAL"}) || l.EnvCount != 2 || len(l.EnvSHA256) != 64 || !l.ArgsKept || !slices.Equal(l.Args, []string{"--model", "x"}) || !l.Yolo {
		t.Fatalf("the launch %+v", l)
	}
	if again := LaunchOf(nil, map[string]string{"GOAL": "the-goal-value", "API_TOKEN": "other"}, false, false); again.EnvSHA256 == l.EnvSHA256 {
		t.Fatal("the hash does not change with a value")
	}
}

// padded is a record of n entries whose last is padded so that the record
// is exactly size bytes as store.Encode writes it.
func padded(t *testing.T, n, size int) Record {
	t.Helper()
	r := serverRecord(sid(1))
	r.V, r.StartedAt = V, time.Date(2026, 11, 5, 9, 0, 0, 0, time.UTC)
	for i := range n {
		r.Entries = append(r.Entries, Entry{Seq: int64(i + 1), At: r.StartedAt.Add(time.Duration(i) * time.Second), Kind: KindReport, Message: strings.Repeat("m", 200)})
	}
	b, _ := store.Encode(&r)
	if len(b) > size {
		t.Fatalf("%d entries are already %d bytes", n, len(b))
	}
	last := &r.Entries[n-1]
	last.Message += strings.Repeat("p", size-len(b))
	if b, _ = store.Encode(&r); len(b) != size {
		t.Fatalf("padded to %d, not %d", len(b), size)
	}
	return r
}

// A record of exactly MaxRecord bytes is written whole; one byte more and
// its oldest entry goes, counted.
func TestARecordIsBoundedOldestEntriesFirst(t *testing.T) {
	r := padded(t, 200, MaxRecord)
	bound(&r)
	if len(r.Entries) != 200 || r.Dropped != 0 {
		t.Fatalf("at the bound: %d entries, %d dropped", len(r.Entries), r.Dropped)
	}
	r = padded(t, 200, MaxRecord+1)
	bound(&r)
	b, _ := store.Encode(&r)
	if len(b) > MaxRecord || r.Dropped < 1 || r.Entries[0].Seq != int64(r.Dropped+1) || r.Entries[len(r.Entries)-1].Seq != 200 {
		t.Fatalf("one past: %d bytes, %d dropped, first seq %d", len(b), r.Dropped, r.Entries[0].Seq)
	}
	// Written through the journal, a record past the bound is under it on
	// disk, its newest entries kept.
	j, dir := newJournal(t, Options{})
	id := sid(1)
	begin(t, j, serverRecord(id))
	for i := range 400 {
		j.Note(id, Activity{At: time.Date(2026, 11, 5, 9, 0, i, 0, time.UTC), Type: actProgress, Message: fmt.Sprintf("%03d %s", i, strings.Repeat("x", 300))})
	}
	j.Flush()
	got, raw := onDisk(t, dir, id)
	if len(raw) > MaxRecord || got.Dropped == 0 || !strings.HasPrefix(got.Entries[len(got.Entries)-1].Message, "399 ") || got.Dropped+len(got.Entries) != 400 {
		t.Fatalf("%d bytes, %d dropped, %d kept", len(raw), got.Dropped, len(got.Entries))
	}
}

// The largest launch the API takes (64 arguments of 4 KiB) keeps its count
// and hash and no argument, and a record with every other field at its
// bound and as many entries and files as it is given stays under
// MaxRecord.
func TestTheLargestValidLaunchFits(t *testing.T) {
	args := make([]string, 64)
	env := map[string]string{}
	for i := range args {
		args[i] = strings.Repeat(string(rune('a'+i%26)), 4096)
		env[fmt.Sprintf("VAR_%03d_%s", i, strings.Repeat("N", 100))] = "v"
	}
	l := LaunchOf(args, env, true, true)
	if l.ArgsKept || l.Args != nil || l.ArgCount != 64 || len(l.ArgsSHA256) != 64 || encodedLen(l) > MaxLaunch {
		t.Fatalf("the launch: kept %v, %d args, count %d, %d bytes", l.ArgsKept, len(l.Args), l.ArgCount, encodedLen(l))
	}
	j, dir := newJournal(t, Options{})
	id := sid(1)
	quote := strings.Repeat(`"\`, MaxCwd)
	r := Record{ID: id, Name: quote, Kind: "server", Agent: Agent{ID: quote, Name: quote, Adapter: quote, Version: quote, Session: &AgentSession{ID: quote, Source: "hook"}},
		Host: Host{Name: quote, User: quote}, Where: quote, Cwd: quote, Repo: quote, RepoCommon: quote, Branch: quote, Start: quote, StartFrom: StartFromLaunch,
		Launch: l, Crew: &Crew{RunID: quote, CrewID: quote, Member: quote, Task: quote, Attempt: 3, Brakeman: quote}, ResumedFrom: quote}
	begin(t, j, r)
	for i := range MaxTools + 10 {
		j.Tool(id, fmt.Sprintf("%03d%s", i, strings.Repeat(`"`, MaxTool)))
	}
	for i := range 600 {
		j.Note(id, Activity{Type: actFile, Op: "edit", Path: fmt.Sprintf("/%d/%s", i, strings.Repeat(`"`, MaxPath))})
	}
	for i := range 300 {
		j.Note(id, Activity{Type: actError, Message: strings.Repeat(`\`, MaxMessage), URL: strings.Repeat(`"`, MaxURL), Tool: fmt.Sprint(i)})
	}
	j.Flush()
	got, raw := onDisk(t, dir, id)
	if len(raw) > MaxRecord {
		t.Fatalf("the record is %d bytes", len(raw))
	}
	if got.Launch == nil || got.Launch.ArgCount != 64 || got.Launch.ArgsSHA256 != l.ArgsSHA256 || got.Launch.ArgsKept || len(got.Tools) != MaxTools {
		t.Fatalf("the launch %+v, %d tools", got.Launch, len(got.Tools))
	}
}

// pend gives the record of id a pending resume's receipt and writes it.
func pend(t *testing.T, j *Journal, id string) {
	t.Helper()
	j.mu.Lock()
	x := j.recs[id]
	x.r.Resumes = append(x.r.Resumes, Resume{CommandID: "c1", Session: sid(99), State: ResumePending, At: time.Now().UTC()})
	x.dirty = true
	j.mu.Unlock()
	j.Flush()
}

// ended begins n server records, each ended, the oldest first.
func endedRecords(t *testing.T, j *Journal, from, n int) {
	t.Helper()
	at := time.Date(2026, 11, 5, 9, 0, 0, 0, time.UTC)
	for i := from; i < from+n; i++ {
		r := serverRecord(sid(i))
		r.StartedAt = at.Add(time.Duration(i) * time.Minute)
		begin(t, j, r)
		j.End(sid(i), Ending{At: r.StartedAt.Add(time.Second), How: "exited (exit 0)"})
	}
	j.Flush()
}

func exists(dir, id string) bool {
	_, err := os.Stat(filepath.Join(dir, Dir, SessionsDir, id+".json"))
	return err == nil
}

// Past MaxRecords the oldest ended records go; a live one and one holding
// a pending receipt stay, whatever their age, and are not counted.
func TestPruneKeepsLiveAndPendingRecords(t *testing.T) {
	maxRecords = 3
	t.Cleanup(func() { maxRecords = MaxRecords })
	j, dir := newJournal(t, Options{})
	live := serverRecord(sid(0))
	live.StartedAt = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	begin(t, j, live)
	endedRecords(t, j, 1, 1)
	pend(t, j, sid(1))
	endedRecords(t, j, 2, 5)
	j.Prune()
	for i, want := range []bool{true, true, false, false, true, true, true} {
		if exists(dir, sid(i)) != want {
			t.Fatalf("record %d kept %v, want %v", i, !want, want)
		}
	}
}

// When live records alone pass MaxRecords, every ended one past the bound
// goes and none of the live ones.
func TestLiveRecordsPastMaxRecords(t *testing.T) {
	maxRecords = 2
	t.Cleanup(func() { maxRecords = MaxRecords })
	j, dir := newJournal(t, Options{})
	for i := range 4 {
		begin(t, j, serverRecord(sid(10+i)))
	}
	endedRecords(t, j, 0, 4)
	j.Prune()
	for i := range 4 {
		if !exists(dir, sid(10+i)) {
			t.Fatalf("live record %d pruned", i)
		}
	}
	for i, want := range []bool{false, false, true, true} {
		if exists(dir, sid(i)) != want {
			t.Fatalf("ended record %d kept %v", i, !want)
		}
	}
}

// A server session's record found open after a stop is marked lost, never
// ended: LostAt its last write's time, EndedAt empty.
func TestRecoverMarksAnOpenRecordLost(t *testing.T) {
	dir := t.TempDir()
	j := openAt(t, dir, Options{})
	begin(t, j, serverRecord(sid(1)))
	j.Note(sid(1), Activity{Type: actProgress, Message: "working"})
	j.Close()
	fi, err := os.Stat(filepath.Join(dir, Dir, SessionsDir, sid(1)+".json"))
	if err != nil {
		t.Fatal(err)
	}
	openAt(t, dir, Options{})
	r, _ := onDisk(t, dir, sid(1))
	if r.Ending != EndingLost || !r.EndedAt.IsZero() || !r.LostAt.Equal(fi.ModTime().UTC()) || len(r.Entries) != 1 {
		t.Fatalf("recovered %+v (written at %v)", r, fi.ModTime())
	}
}

// A hosted session's record found open after a stop is marked host away,
// the same way.
func TestRecoverMarksAHostedRecordHostAway(t *testing.T) {
	dir := t.TempDir()
	j := openAt(t, dir, Options{})
	begin(t, j, hostedRecord(sid(1), "o1"))
	j.Flush()
	j.Close()
	openAt(t, dir, Options{})
	r, _ := onDisk(t, dir, sid(1))
	if r.Ending != EndingHostAway || !r.EndedAt.IsZero() || r.LostAt.IsZero() {
		t.Fatalf("recovered %+v", r)
	}
}

// The record takes the session as it is when the writer reads it
// (Current), never a snapshot a change handed over: two changes whose
// snapshots would arrive older last (Codex's lowest id, then a higher one;
// a resumable id, then one not resumable) leave the lowest, resumable.
func TestAnOlderChangeNeverOvertakesANewerOne(t *testing.T) {
	var mu sync.Mutex
	now := Meta{Name: "codex #1", AgentSession: &AgentSession{ID: "019a-lowest", Source: "hook", Resumable: true}}
	j, dir := newJournal(t, Options{Current: func(string) (Meta, bool) {
		mu.Lock()
		defer mu.Unlock()
		return now, true
	}})
	id := sid(1)
	begin(t, j, serverRecord(id))
	j.Changed(id) // its snapshot: the higher id
	j.Changed(id) // its snapshot: not resumable
	j.Flush()
	r, _ := onDisk(t, dir, id)
	if s := r.Agent.Session; s == nil || s.ID != "019a-lowest" || !s.Resumable || r.Name != "codex #1" {
		t.Fatalf("the agent's session %+v, name %q", r.Agent.Session, r.Name)
	}
	mu.Lock()
	now.Crew = &Crew{RunID: "todo-1", CrewID: "todo", Member: "lead"}
	mu.Unlock()
	j.Changed(id)
	j.Flush()
	if r, _ := onDisk(t, dir, id); r.Crew == nil || r.Crew.RunID != "todo-1" {
		t.Fatalf("the crew taken after the start: %+v", r.Crew)
	}
}

// The same host's hosted session registering under the same id takes its
// record up again, marked host away or lost: its entries and Seq kept,
// counted, a status entry saying so. Another owner, or a server session's
// Begin of that id, is refused.
func TestBeginReopensTheSameHostsRecord(t *testing.T) {
	dir := t.TempDir()
	j := openAt(t, dir, Options{})
	id := sid(1)
	begin(t, j, hostedRecord(id, "o1"))
	j.Note(id, Activity{Type: actProgress, Message: "before"})
	j.Close()
	j = openAt(t, dir, Options{})
	away, _ := onDisk(t, dir, id)
	if away.Ending != EndingHostAway {
		t.Fatalf("not away: %+v", away)
	}
	if err := j.Begin(hostedRecord(id, "o2")); !errors.Is(err, ErrExists) {
		t.Fatalf("another owner's: %v", err)
	}
	if err := j.Begin(serverRecord(id)); !errors.Is(err, ErrExists) {
		t.Fatalf("a server session's: %v", err)
	}
	begin(t, j, hostedRecord(id, "o1"))
	j.Note(id, Activity{Type: actProgress, Message: "after"})
	j.Flush()
	r, _ := onDisk(t, dir, id)
	var msgs []string
	for _, e := range r.Entries {
		msgs = append(msgs, e.Message)
	}
	if r.Reopened != 1 || !r.LostAt.IsZero() || r.Ending != "" || r.Seq <= away.Seq || !slices.Equal(msgs, []string{"before", "taken up again by its host", "after"}) {
		t.Fatalf("reopened %d, lost %v, ending %q, seq %d after %d, entries %q", r.Reopened, r.LostAt, r.Ending, r.Seq, away.Seq, msgs)
	}
	// Lost (its host let go of here), then the same host again.
	j.End(id, Ending{How: EndingLost, Lost: true})
	j.Flush()
	begin(t, j, hostedRecord(id, "o1"))
	j.Flush()
	if again, _ := onDisk(t, dir, id); again.Reopened != 2 || again.Ending != "" || again.Seq <= r.Seq || len(again.Entries) != 4 {
		t.Fatalf("reopened again: %+v", again)
	}
	// An observed end is never taken up again.
	j.End(id, Ending{How: "exited (exit 0)"})
	j.Flush()
	if err := j.Begin(hostedRecord(id, "o1")); !errors.Is(err, ErrExists) {
		t.Fatalf("an ended record taken up again: %v", err)
	}
}

// writeRecord puts r in dir's sessions as a server stopped would have
// left it.
func writeRecord(t *testing.T, dir string, r Record) {
	t.Helper()
	st, err := store.Open(filepath.Join(dir, Dir, SessionsDir))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Save(r.ID+".json", &r); err != nil {
		t.Fatal(err)
	}
}

// A resume's receipt a stop left pending is settled by what the data
// directory shows: the allocated session's record there, started; none,
// uncertain.
func TestRecoverSettlesAPendingResume(t *testing.T) {
	dir := t.TempDir()
	at := time.Date(2026, 11, 5, 9, 0, 0, 0, time.UTC)
	r := serverRecord(sid(1))
	r.V, r.Seq, r.StartedAt, r.EndedAt, r.Ending, r.Entries = V, 3, at, at.Add(time.Minute), "exited (exit 0)", []Entry{}
	r.Resumes = []Resume{{CommandID: "c1", Session: sid(2), State: ResumePending, At: at}, {CommandID: "c2", Session: sid(3), State: ResumePending, At: at}}
	writeRecord(t, dir, r)
	started := serverRecord(sid(2))
	started.V, started.Seq, started.StartedAt, started.Entries = V, 4, at.Add(2*time.Minute), []Entry{}
	writeRecord(t, dir, started)
	openAt(t, dir, Options{})
	got, _ := onDisk(t, dir, sid(1))
	if got.Resumes[0].State != ResumeStarted || got.Resumes[0].Session != sid(2) || got.Resumes[1].State != ResumeUncertain || got.Resumes[1].Error == "" {
		t.Fatalf("receipts %+v", got.Resumes)
	}
	if s, _ := onDisk(t, dir, sid(2)); s.Ending != EndingLost {
		t.Fatalf("the started session's record %+v", s)
	}
}

// Seq never goes back: a stop between the state file and the record (a
// write that fails there) leaves the state ahead, and every later write
// takes a greater one.
func TestSeqNeverGoesBack(t *testing.T) {
	dir := t.TempDir()
	j := openAt(t, dir, Options{})
	begin(t, j, serverRecord(sid(1)))
	first, _ := onDisk(t, dir, sid(1))
	crash := errors.New("the server stopped here")
	beforeWrite = func(name string) error {
		if name == SessionsDir+"/"+sid(1)+".json" {
			return crash
		}
		return nil
	}
	t.Cleanup(func() { beforeWrite = nil })
	j.Note(sid(1), Activity{Type: actProgress, Message: "lost with the crash"})
	j.Flush()
	var st struct{ Seq int64 }
	b, _ := os.ReadFile(filepath.Join(dir, Dir, StateFile))
	json.Unmarshal(b, &st)
	j.Close()
	beforeWrite = nil
	if st.Seq <= first.Seq {
		t.Fatalf("the state %d is not past the record's %d", st.Seq, first.Seq)
	}
	j = openAt(t, dir, Options{})
	recovered, _ := onDisk(t, dir, sid(1))
	begin(t, j, serverRecord(sid(2)))
	next, _ := onDisk(t, dir, sid(2))
	if recovered.Seq <= st.Seq || next.Seq <= recovered.Seq {
		t.Fatalf("seqs: first %d, state %d, recovered %d, next %d", first.Seq, st.Seq, recovered.Seq, next.Seq)
	}
}

// Forget refuses a live session's record and one holding a pending
// receipt; ForgetAll keeps both, counts them, and forgets the rest.
func TestForgetRefusesALiveSession(t *testing.T) {
	j, dir := newJournal(t, Options{})
	begin(t, j, serverRecord(sid(0)))
	endedRecords(t, j, 1, 3)
	pend(t, j, sid(1))
	if err := j.Forget(sid(0)); !errors.Is(err, ErrLive) {
		t.Fatalf("live: %v", err)
	}
	if err := j.Forget(sid(1)); !errors.Is(err, ErrResumePending) {
		t.Fatalf("pending: %v", err)
	}
	if err := j.Forget(sid(2)); err != nil || exists(dir, sid(2)) {
		t.Fatalf("ended: %v", err)
	}
	if err := j.Forget(sid(2)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("again: %v", err)
	}
	forgotten, kept, err := j.ForgetAll()
	if err != nil || forgotten != 1 || kept != 2 || !exists(dir, sid(0)) || !exists(dir, sid(1)) || exists(dir, sid(3)) {
		t.Fatalf("forget all: %d forgotten, %d kept, %v", forgotten, kept, err)
	}
}

// A record of a later version is listed as newer and read as it is, and
// never rewritten, not even by Recover, though it has no end.
func TestANewerRecordIsReadNotRewritten(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, Dir, SessionsDir, sid(1)+".json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"v": 2, "id": "` + sid(1) + `", "seq": 9, "name": "from later", "kind": "server", "agent": {"id": "cat"}, "host": {}, "cwd": "/w", "startedAt": "2026-11-05T09:00:00Z", "entries": [], "future": {"x": 1}}` + "\n")
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	j := openAt(t, dir, Options{})
	page, err := j.List("", 10)
	if err != nil || len(page.Records) != 1 || !page.Records[0].Newer || page.Records[0].Name != "from later" {
		t.Fatalf("list %+v %v", page, err)
	}
	r, newer, err := j.Get(sid(1))
	if err != nil || !newer || r.Name != "from later" {
		t.Fatalf("get %+v %v %v", r, newer, err)
	}
	j.Note(sid(1), Activity{Type: actProgress, Message: "x"})
	j.Flush()
	if got, _ := os.ReadFile(path); !bytes.Equal(got, body) {
		t.Fatalf("rewritten:\n%s", got)
	}
}

// The helper holds the journal's owner lock of the data directory
// JOURNAL_HOLD until its stdin closes.
func TestHelperHoldsTheJournalsLock(t *testing.T) {
	dir := os.Getenv("JOURNAL_HOLD")
	if dir == "" {
		t.Skip("a helper")
	}
	st, err := store.Open(dir)
	if err != nil {
		fmt.Println("error:", err)
		os.Exit(2)
	}
	j, err := Open(st, Options{Log: quiet})
	if err != nil {
		fmt.Println("error:", err)
		os.Exit(2)
	}
	if err := j.Begin(serverRecord(sid(1))); err != nil {
		fmt.Println("error:", err)
		os.Exit(2)
	}
	fmt.Println("held")
	bufio.NewReader(os.Stdin).ReadString('\n')
	j.Close()
}

// A journal another process holds the lock of is read-only: it reads the
// records from the files, writes nothing, and says why.
func TestReadOnlyWithoutTheLock(t *testing.T) {
	dir := t.TempDir()
	helper := exec.Command(os.Args[0], "-test.run=^TestHelperHoldsTheJournalsLock$", "-test.v")
	helper.Env = append(os.Environ(), "JOURNAL_HOLD="+dir)
	stdin, err := helper.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := helper.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := helper.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		stdin.Close()
		helper.Wait()
	}()
	sc := bufio.NewScanner(stdout)
	held := false
	for sc.Scan() {
		if sc.Text() == "held" {
			held = true
			break
		}
		if strings.HasPrefix(sc.Text(), "error:") {
			t.Fatal(sc.Text())
		}
	}
	if !held {
		t.Fatal("the helper did not take the lock")
	}
	old := LockWait
	LockWait = 300 * time.Millisecond
	t.Cleanup(func() { LockWait = old })
	j := openAt(t, dir, Options{})
	ro, why := j.ReadOnly()
	if !ro || !strings.Contains(why, "another server holds") {
		t.Fatalf("read-only %v: %s", ro, why)
	}
	if err := j.Begin(serverRecord(sid(2))); !errors.Is(err, ErrNotOwner) {
		t.Fatalf("a begin: %v", err)
	}
	if err := j.Forget(sid(1)); !errors.Is(err, ErrNotOwner) {
		t.Fatalf("a forget: %v", err)
	}
	page, err := j.List("", 10)
	if err != nil || len(page.Records) != 1 || page.Records[0].ID != sid(1) {
		t.Fatalf("the list read from the files: %+v %v", page, err)
	}
	if r, _, err := j.Get(sid(1)); err != nil || r.ID != sid(1) {
		t.Fatalf("a get: %+v %v", r, err)
	}
	if exists(dir, sid(2)) {
		t.Fatal("a read-only journal wrote")
	}
}

// List pages the records newest first, its cursor naming the next page.
func TestListPagesNewestFirst(t *testing.T) {
	j, _ := newJournal(t, Options{})
	endedRecords(t, j, 0, 5)
	var seen []string
	after := ""
	for {
		page, err := j.List(after, 2)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range page.Records {
			seen = append(seen, r.ID)
		}
		if page.Next == "" {
			break
		}
		after = page.Next
	}
	if !slices.Equal(seen, []string{sid(4), sid(3), sid(2), sid(1), sid(0)}) {
		t.Fatalf("listed %v", seen)
	}
	if _, err := j.List("not a cursor", 2); !errors.Is(err, ErrBadCursor) {
		t.Fatalf("a bad cursor: %v", err)
	}
}

// 32 goroutines noting into 8 records at once: every entry is kept once,
// in order of its time then its Seq.
func TestManyNotersIntoManyRecords(t *testing.T) {
	j, dir := newJournal(t, Options{})
	for r := range 8 {
		begin(t, j, serverRecord(sid(r)))
	}
	const each = 25
	var wg sync.WaitGroup
	for g := range 32 {
		wg.Go(func() {
			for i := range each {
				j.Note(sid(g%8), Activity{Type: actProgress, Message: fmt.Sprintf("g%02d-%02d", g, i)})
				if i%7 == 0 {
					j.Changed(sid(g % 8))
				}
			}
		})
	}
	wg.Wait()
	j.Flush()
	for r := range 8 {
		got, _ := onDisk(t, dir, sid(r))
		if len(got.Entries) != 4*each {
			t.Fatalf("record %d holds %d entries", r, len(got.Entries))
		}
		seen := map[string]bool{}
		for i, e := range got.Entries {
			if seen[e.Message] {
				t.Fatalf("record %d holds %s twice", r, e.Message)
			}
			seen[e.Message] = true
			if i > 0 && after(got.Entries[i-1], e) {
				t.Fatalf("record %d: entry %d out of order", r, i)
			}
		}
	}
}

// fitPath's cut fits its bound as JSON writes the string, escapes
// included, and cuts nothing that fits.
func TestFitPathCountsEscapes(t *testing.T) {
	for _, s := range []string{strings.Repeat(`"`, 100), strings.Repeat("\x01", 100), strings.Repeat("é", 100), "a b\tc\\d" + strings.Repeat("x", 90), "plain"} {
		for _, n := range []int{0, 1, 5, 17, 64, 1000} {
			got := fitPath(s, n)
			b, err := store.Encode(got)
			if err != nil {
				t.Fatal(err)
			}
			if size := len(bytes.TrimSpace(b)) - 2; size > n || !strings.HasPrefix(s, got) {
				t.Fatalf("fitPath(%q, %d) = %q, %d bytes", s, n, got, size)
			}
		}
	}
	if fitPath("plain", 5) != "plain" {
		t.Fatal("a string that fits was cut")
	}
}
