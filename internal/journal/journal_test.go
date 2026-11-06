package journal

import (
	"bufio"
	"bytes"
	"context"
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
	j, dir := newJournal(t, Options{Denied: func(id, path string) (bool, bool) {
		mu.Lock()
		defer mu.Unlock()
		asked = append(asked, id+" "+path)
		return strings.HasPrefix(path, "/data/"), true
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

// The head of a record (all but its entries and files) at every bound,
// its strings written in quotes and backslashes, 16 receipts and 64 tools
// of the longest names, fits MaxRecord with room left for entries:
// dropping entries and files always brings a record under the bound.
func TestTheHeadAtEveryBoundLeavesRoom(t *testing.T) {
	q := strings.Repeat(`"\`, MaxCwd)
	args := make([]string, 64)
	for i := range args {
		args[i] = strings.Repeat("a", 4096)
	}
	env := map[string]string{}
	for i := range MaxEnvNames {
		env[fmt.Sprintf("V%03d%s", i, strings.Repeat("N", 300))] = "v"
	}
	r := Record{V: V, ID: sid(1), Seq: 1 << 62, Name: q, Kind: "server", Agent: Agent{ID: q, Name: q, Adapter: q, Version: q, Session: &AgentSession{ID: q, Source: q, Resumable: true}},
		Host: Host{Name: q, User: q, Owner: q, Hosted: true}, Where: q, Cwd: q, Repo: q, RepoCommon: q, Branch: q, Start: q, StartFrom: q, End: q,
		Launch: LaunchOf(args, env, true, true), Crew: &Crew{RunID: q, CrewID: q, Member: q, Task: q, Attempt: 1 << 30, Brakeman: q}, ResumedFrom: q,
		StartedAt: time.Now(), EndedAt: time.Now(), LostAt: time.Now(), Ending: q, Reopened: 1 << 30, Dropped: 1 << 30, Tools: map[string]int{}}
	for i := range MaxResumes + 4 {
		r.Resumes = append(r.Resumes, Resume{CommandID: q, Actor: q, Session: q, State: q, Error: q, At: time.Now()})
		_ = i
	}
	fitRecord(&r)
	for i := range MaxTools {
		r.Tools[fitPath(fmt.Sprintf("%03d%s", i, q), MaxTool)] = 1 << 30
	}
	b, err := store.Encode(&r)
	if err != nil {
		t.Fatal(err)
	}
	parts := map[string]int{}
	for name, v := range map[string]any{"launch": r.Launch, "resumes": r.Resumes, "tools": r.Tools, "cwd": r.Cwd, "agent": r.Agent, "host": r.Host, "crew": r.Crew} {
		pb, _ := store.Encode(v)
		parts[name] = len(pb)
	}
	t.Logf("the head at every bound: %d bytes (%v)", len(b), parts)
	if len(b) > maxHead {
		t.Fatalf("the head is %d bytes, past %d", len(b), maxHead)
	}
}

// clock is a test's clock, moved by hand.
type clock struct {
	mu  sync.Mutex
	now time.Time
}

func newClock() *clock { return &clock{now: time.Date(2026, 11, 5, 9, 0, 0, 0, time.UTC)} }

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

// inMemory says whether the record of id is held in memory.
func inMemory(j *Journal, id string) bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	_, ok := j.recs[id]
	return ok
}

// slowFlush makes the writer's tick never come within a test, so only
// what is urgent is written unless the test flushes.
func slowFlush(t *testing.T) {
	old := flushEvery
	flushEvery = time.Hour
	t.Cleanup(func() { flushEvery = old })
}

// An entry an earlier callback still carried when the record left memory
// after its end is kept in its place all the same, and a rename of the
// ended session reaches the record: the record is read back from its file
// and what came is merged in; one after the end is dropped and counted.
func TestALateHookAfterEvictionIsKept(t *testing.T) {
	c := newClock()
	name := "cat #1"
	var mu sync.Mutex
	j, dir := newJournal(t, Options{Now: c.Now, Current: func(string) (Meta, bool) {
		mu.Lock()
		defer mu.Unlock()
		return Meta{Name: name}, true
	}})
	id := sid(1)
	begin(t, j, serverRecord(id))
	at := c.Now()
	j.Note(id, Activity{At: at, Type: actProgress, Message: "first"})
	j.End(id, Ending{At: at.Add(2 * time.Second), How: "exited (exit 0)"})
	j.Flush()
	c.add(time.Minute)
	j.Flush()
	if inMemory(j, id) {
		t.Fatal("the ended record is still in memory")
	}
	j.Note(id, Activity{At: at.Add(time.Second), Type: actProgress, Message: "held"})
	j.Note(id, Activity{At: at.Add(time.Hour), Type: actProgress, Message: "too late"})
	mu.Lock()
	name = "renamed"
	mu.Unlock()
	j.Changed(id)
	j.Flush()
	r, _ := onDisk(t, dir, id)
	var msgs []string
	for _, e := range r.Entries {
		msgs = append(msgs, e.Message)
	}
	if !slices.Equal(msgs, []string{"first", "held"}) || r.Dropped != 1 || r.Name != "renamed" || r.Ending != "exited (exit 0)" || r.EndedAt.IsZero() {
		t.Fatalf("after the late hooks: entries %q dropped %d name %q ending %q", msgs, r.Dropped, r.Name, r.Ending)
	}
	// It leaves memory again.
	c.add(time.Minute)
	j.Flush()
	if inMemory(j, id) {
		t.Fatal("the record brought back stayed in memory")
	}
}

// gateLoad holds the merge of a record's earlier file at the read, until
// the test lets it go.
func gateLoad(t *testing.T) (entered <-chan string, release func()) {
	in := make(chan string, 8)
	gate := make(chan struct{})
	var once sync.Once
	beforeLoad = func(id string) error {
		in <- id
		<-gate
		return nil
	}
	release = func() { once.Do(func() { close(gate) }) }
	t.Cleanup(func() {
		release()
		beforeLoad = nil
	})
	return in, release
}

// awayRecord is a hosted record its host went away from, as a server
// that stopped leaves it, with one entry.
func awayRecord(id, owner string) Record {
	r := hostedRecord(id, owner)
	at := time.Date(2026, 11, 5, 8, 0, 0, 0, time.UTC)
	r.V, r.StartedAt, r.LostAt, r.Ending = V, at, at.Add(time.Minute), EndingHostAway
	r.Entries = []Entry{{Seq: 1, At: at.Add(time.Second), Kind: KindReport, Event: actProgress, Message: "before"}}
	return r
}

// An end observed while the earlier file of a hosted record taken up again
// is still being read stands once it is merged: the record is ended, never
// open again.
func TestAnEndDuringTheMergeStands(t *testing.T) {
	dir := t.TempDir()
	id := sid(1)
	writeRecord(t, dir, awayRecord(id, "o1"))
	j := openAt(t, dir, Options{})
	entered, release := gateLoad(t)
	begin(t, j, hostedRecord(id, "o1"))
	<-entered
	j.End(id, Ending{How: "exited (exit 0)"})
	release()
	j.Flush()
	r, _ := onDisk(t, dir, id)
	if r.EndedAt.IsZero() || r.Ending != "exited (exit 0)" || !r.LostAt.IsZero() || r.Reopened != 1 {
		t.Fatalf("the end observed during the merge: %+v", r)
	}
	if got := kinds(r); !slices.Equal(got, []string{KindReport, KindStatus}) || r.Entries[0].Message != "before" {
		t.Fatalf("entries %+v", r.Entries)
	}
}

// A hosted record whose earlier file could not be read is never written
// over: it is read again at the next pass and merged then, what came
// meanwhile kept; one found of a later version is left as it is.
func TestAFailedMergeReadIsReadAgainNotOverwritten(t *testing.T) {
	dir := t.TempDir()
	id := sid(1)
	writeRecord(t, dir, awayRecord(id, "o1"))
	_, before := onDisk(t, dir, id)
	j := openAt(t, dir, Options{})
	var fails sync.Mutex
	failing := true
	beforeLoad = func(string) error {
		fails.Lock()
		defer fails.Unlock()
		if failing {
			return errors.New("a disk that did not answer")
		}
		return nil
	}
	t.Cleanup(func() { beforeLoad = nil })
	begin(t, j, hostedRecord(id, "o1"))
	j.Note(id, Activity{Type: actProgress, Message: "after"})
	j.Flush()
	if _, now := onDisk(t, dir, id); !bytes.Equal(now, before) {
		t.Fatalf("the record was written over after a failed read:\n%s", now)
	}
	fails.Lock()
	failing = false
	fails.Unlock()
	j.Flush()
	r, _ := onDisk(t, dir, id)
	var msgs []string
	for _, e := range r.Entries {
		msgs = append(msgs, e.Message)
	}
	if r.Reopened != 1 || !r.LostAt.IsZero() || !slices.Equal(msgs, []string{"before", "taken up again by its host", "after"}) {
		t.Fatalf("after the read: reopened %d lost %v entries %q", r.Reopened, r.LostAt, msgs)
	}

	// A file found of a later version as it is merged: never written.
	id2 := sid(2)
	writeRecord(t, dir, awayRecord(id2, "o1"))
	j2 := reopenJournal(t, j, dir)
	later := awayRecord(id2, "o1")
	later.V = V + 1
	writeRecord(t, dir, later)
	_, laterBytes := onDisk(t, dir, id2)
	begin(t, j2, hostedRecord(id2, "o1"))
	j2.Flush()
	if _, now := onDisk(t, dir, id2); !bytes.Equal(now, laterBytes) {
		t.Fatalf("a later version's record was written over:\n%s", now)
	}
	if page, _ := j2.List("", 10); !slices.ContainsFunc(page.Records, func(s Summary) bool { return s.ID == id2 && s.Newer }) {
		t.Fatalf("the later version's row: %+v", page.Records)
	}
}

// reopenJournal closes j and opens another over dir, as a restart does.
func reopenJournal(t *testing.T, j *Journal, dir string) *Journal {
	t.Helper()
	j.Close()
	return openAt(t, dir, Options{})
}

// A record file this version cannot decode is kept as it is and is in
// the index: listed as unreadable, refused to a Begin of its id (so
// nothing writes over it), counted toward MaxRecords by its last write,
// and forgettable.
func TestAnUnreadableRecordIsListedBoundedAndForgettable(t *testing.T) {
	t.Cleanup(func() { maxRecords = MaxRecords })
	dir := t.TempDir()
	sess := filepath.Join(dir, Dir, SessionsDir)
	if err := os.MkdirAll(sess, 0o700); err != nil {
		t.Fatal(err)
	}
	bad := func(id string) string {
		return `{"v": 1, "id": "` + id + `", "unknown": 1, "entries": [{"message": "evidence before"}]}`
	}
	old := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, id := range []string{sid(1), sid(2)} {
		p := filepath.Join(sess, id+".json")
		if err := os.WriteFile(p, []byte(bad(id)), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, old, old); err != nil {
			t.Fatal(err)
		}
	}
	j := openAt(t, dir, Options{})
	page, err := j.List("", 10)
	if err != nil || len(page.Records) != 2 || !page.Records[0].Unreadable || !page.Records[1].Unreadable {
		t.Fatalf("the list: %+v %v", page.Records, err)
	}
	if err := j.Begin(hostedRecord(sid(1), "o1")); !errors.Is(err, ErrExists) {
		t.Fatalf("a hosted begin over an unreadable record: %v", err)
	}
	if err := j.Begin(serverRecord(sid(2))); !errors.Is(err, ErrExists) {
		t.Fatalf("a begin over an unreadable record: %v", err)
	}
	j.Flush()
	for _, id := range []string{sid(1), sid(2)} {
		if b, err := os.ReadFile(filepath.Join(sess, id+".json")); err != nil || string(b) != bad(id) {
			t.Fatalf("the unreadable %s was written over: %s %v", id, b, err)
		}
	}
	if err := j.Forget(sid(1)); err != nil || exists(dir, sid(1)) {
		t.Fatalf("forget an unreadable record: %v, still there %v", err, exists(dir, sid(1)))
	}
	// One ended record past a bound of one: the unreadable one, last
	// written before it ended, goes.
	maxRecords = 1
	endedRecords(t, j, 3, 1)
	j.Prune()
	if exists(dir, sid(2)) || !exists(dir, sid(3)) {
		t.Fatalf("pruned: unreadable kept %v, ended kept %v", exists(dir, sid(2)), exists(dir, sid(3)))
	}
}

// Prune keeps the records that ended last: a session that started first
// and ran longest, ended after two later ones, is kept as the newest ended
// one, and the one that ended first goes.
func TestPruneKeepsTheLastToEnd(t *testing.T) {
	maxRecords = 2
	t.Cleanup(func() { maxRecords = MaxRecords })
	j, dir := newJournal(t, Options{})
	long := serverRecord(sid(0))
	long.StartedAt = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	begin(t, j, long)
	endedRecords(t, j, 1, 2)
	j.End(sid(0), Ending{At: time.Date(2026, 11, 6, 0, 0, 0, 0, time.UTC), How: "exited (exit 0)"})
	j.Flush()
	if !exists(dir, sid(0)) || exists(dir, sid(1)) || !exists(dir, sid(2)) {
		t.Fatalf("kept: long %v, first to end %v, second %v", exists(dir, sid(0)), exists(dir, sid(1)), exists(dir, sid(2)))
	}
}

// Close gives the end commits still being read closeWait, together, and
// returns with every end written, without the commits not read by then,
// even when the reads never return. Close begins once one read is under
// way, whichever pass took the others: every end is written all the same.
func TestCloseGivesUpOnEndCommits(t *testing.T) {
	old := closeWait
	closeWait = 50 * time.Millisecond
	t.Cleanup(func() { closeWait = old })
	hung := make(chan struct{})
	t.Cleanup(func() { close(hung) })
	started := make(chan struct{}, 8)
	j, dir := newJournal(t, Options{EndCommit: func(context.Context, Record) string {
		started <- struct{}{}
		<-hung
		return "0123456789abcdef0123456789abcdef01234567"
	}})
	for i := range 3 {
		begin(t, j, serverRecord(sid(i)))
	}
	for i := range 3 {
		j.End(sid(i), Ending{How: "stopped"})
	}
	<-started // a read under way, never returning
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	for i := range 3 {
		if r, _ := onDisk(t, dir, sid(i)); r.EndedAt.IsZero() || r.Ending != "stopped" || r.End != "" {
			t.Fatalf("record %d after Close: %+v", i, r)
		}
	}
}

// A crew a session takes after its launch is written at once, not at the
// next flush: the writer writes the record with the crew while its tick
// cannot come.
func TestACrewTakenIsWrittenAtOnce(t *testing.T) {
	slowFlush(t)
	var mu sync.Mutex
	var crew *Crew
	j, dir := newJournal(t, Options{Current: func(string) (Meta, bool) {
		mu.Lock()
		defer mu.Unlock()
		return Meta{Name: "cat #1", Crew: crew}, true
	}})
	id := sid(1)
	begin(t, j, serverRecord(id))
	written := make(chan string, 8)
	beforeWrite = func(name string) error {
		written <- name
		return nil
	}
	t.Cleanup(func() { beforeWrite = nil })
	mu.Lock()
	crew = &Crew{RunID: "run-1", CrewID: "team", Member: "lead"}
	mu.Unlock()
	j.Changed(id)
	for name := range written {
		if name == SessionsDir+"/"+id+".json" {
			break
		}
	}
	// The write begun is the crew's: the file holds it once it is done,
	// with no flush and no tick (the deadline only bounds a hang).
	deadline := time.Now().Add(10 * time.Second)
	for {
		if r, _ := onDisk(t, dir, id); r.Crew != nil && r.Crew.RunID == "run-1" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the crew was never written at once")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// The deny list judges the path a touch came with, never the path cut to
// MaxPath: a denied name past the bound is not kept.
func TestTheDenyListJudgesTheWholePath(t *testing.T) {
	j, dir := newJournal(t, Options{Denied: func(_, path string) (bool, bool) { return strings.HasSuffix(path, "/.env"), true }})
	id := sid(1)
	begin(t, j, serverRecord(id))
	long := "/w/" + strings.Repeat("a", MaxPath) + "/.env"
	j.Touch(id, File{Path: long, Op: "read"})
	j.Touch(id, File{Path: "/w/main.go", Op: "read"})
	j.Flush()
	if r, _ := onDisk(t, dir, id); len(r.Files) != 1 || r.Files[0].Path != "/w/main.go" {
		t.Fatalf("files %+v", r.Files)
	}
}

// What a session was as it left the server and its deny list (Gone) serve
// the work still pending once the server no longer lists it: its touches
// are judged by its own deny list, not refused for want of one, and its
// last name is taken.
func TestAGoneSessionsPendingWorkIsKept(t *testing.T) {
	slowFlush(t)
	j, dir := newJournal(t, Options{
		Current: func(string) (Meta, bool) { return Meta{}, false },
		Denied:  func(string, string) (bool, bool) { return true, true },
	})
	id := sid(1)
	begin(t, j, serverRecord(id))
	j.Touch(id, File{Path: "/w/main.go", Op: "edit"})
	j.Touch(id, File{Path: "/w/secret.key", Op: "read"})
	j.Gone(id, Meta{Name: "its last name"}, func(path string) bool { return path == "/w/secret.key" })
	j.End(id, Ending{How: "stopped"})
	j.Flush()
	r, _ := onDisk(t, dir, id)
	if len(r.Files) != 1 || r.Files[0].Path != "/w/main.go" || r.Name != "its last name" || r.EndedAt.IsZero() {
		t.Fatalf("the gone session's record: files %+v name %q ended %v", r.Files, r.Name, r.EndedAt)
	}
}

// heldThenFree makes the owner lock held for the journal until free is
// called, then taken as the store takes it; failing makes every try after
// the first an error instead.
func heldThenFree(t *testing.T) (free func()) {
	var mu sync.Mutex
	held := true
	takeLock = func(path string) (*store.OwnerLock, string, bool, error) {
		mu.Lock()
		h := held
		mu.Unlock()
		if h {
			return nil, "another server holds it (a test)", true, nil
		}
		return store.TakeOwnerLock(path)
	}
	t.Cleanup(func() { takeLock = store.TakeOwnerLock })
	return func() {
		mu.Lock()
		held = false
		mu.Unlock()
	}
}

// OnOwned's function never runs once Close has begun: a Close that comes
// while the records are recovered (the lock just taken, the journal's lock
// let go, its writes held) returns without it, and it is never begun.
func TestOnOwnedNeverRunsAfterClose(t *testing.T) {
	old := LockWait
	LockWait = time.Minute
	t.Cleanup(func() { LockWait = old })
	dir := t.TempDir()
	open := serverRecord(sid(1))
	open.V, open.StartedAt = V, time.Date(2026, 11, 5, 9, 0, 0, 0, time.UTC)
	writeRecord(t, dir, open)
	free := heldThenFree(t)
	j := openAt(t, dir, Options{})
	var ran sync.Mutex
	called := false
	j.OnOwned(func() {
		ran.Lock()
		called = true
		ran.Unlock()
	})
	entered := make(chan struct{}, 1)
	gate := make(chan struct{})
	beforeWrite = func(name string) error {
		if name == SessionsDir+"/"+sid(1)+".json" {
			select {
			case entered <- struct{}{}:
			default:
			}
			<-gate
		}
		return nil
	}
	t.Cleanup(func() { beforeWrite = nil })
	free()
	<-entered // recovery is writing the record found open, the lock held
	closed := make(chan struct{})
	go func() {
		j.Close()
		close(closed)
	}()
	for {
		j.mu.Lock()
		c := j.closed
		j.mu.Unlock()
		if c {
			break
		}
		time.Sleep(time.Millisecond)
	}
	close(gate)
	<-closed
	j.mu.Lock()
	notified := j.notified
	j.mu.Unlock()
	ran.Lock()
	defer ran.Unlock()
	if notified || called {
		t.Fatalf("OnOwned's function was begun after Close (notified %v, ran %v)", notified, called)
	}
}

// Open says it is read-only before the waiter for the lock starts, which
// changes what it says under the journal's lock (the race detector's
// test: nothing else here takes that lock before the waiter writes).
func TestOpenSaysReadOnlyBeforeItsWaiterRuns(t *testing.T) {
	old := LockWait
	LockWait = time.Minute
	t.Cleanup(func() { LockWait = old })
	tried := make(chan struct{})
	var once sync.Once
	calls := 0
	takeLock = func(string) (*store.OwnerLock, string, bool, error) {
		calls++
		if calls == 1 {
			return nil, "another server holds it (a test)", true, nil
		}
		once.Do(func() { close(tried) })
		return nil, "", false, errors.New("a lock that failed")
	}
	t.Cleanup(func() { takeLock = store.TakeOwnerLock })
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	j, err := Open(st, Options{Log: quiet})
	if err != nil {
		t.Fatal(err)
	}
	<-tried
	j.Close()
	if ro, why := j.ReadOnly(); !ro || why != "a lock that failed" {
		t.Fatalf("read-only %v: %s", ro, why)
	}
}

// A record is measured with the Seq it is written with: one at exactly
// MaxRecord whose Seq gains a digit as it is written is brought under the
// bound, not written past it.
func TestTheBoundCountsTheSeqWritten(t *testing.T) {
	j, dir := newJournal(t, Options{})
	id := sid(1)
	begin(t, j, serverRecord(id))
	j.mu.Lock()
	x := j.recs[id]
	j.seq = 99
	x.r.Seq = 99
	at := time.Date(2026, 11, 5, 9, 0, 0, 0, time.UTC)
	for i := 0; ; i++ {
		x.r.Entries = append(x.r.Entries, Entry{Seq: int64(i + 1), At: at, Kind: KindStatus, Message: strings.Repeat("x", 400)})
		if b, _ := store.Encode(&x.r); len(b) > MaxRecord-600 {
			break
		}
	}
	last := &x.r.Entries[len(x.r.Entries)-1]
	b, _ := store.Encode(&x.r)
	last.Message += strings.Repeat("y", MaxRecord-len(b))
	if b, _ := store.Encode(&x.r); len(b) != MaxRecord {
		j.mu.Unlock()
		t.Fatalf("the record is %d bytes, want exactly %d", len(b), MaxRecord)
	}
	x.dirty = true
	j.mu.Unlock()
	j.Flush()
	r, raw := onDisk(t, dir, id)
	if r.Seq != 100 || len(raw) > MaxRecord {
		t.Fatalf("written at Seq %d, %d bytes (bound %d)", r.Seq, len(raw), MaxRecord)
	}
}

// A record whose file could not be deleted stays indexed, listed and
// forgettable again; ForgetAll counts only what it deleted.
func TestAFailedDeleteKeepsTheRecord(t *testing.T) {
	j, dir := newJournal(t, Options{})
	endedRecords(t, j, 1, 2)
	beforeDelete = func(string) error { return errors.New("a disk that did not answer") }
	t.Cleanup(func() { beforeDelete = nil })
	if err := j.Forget(sid(1)); err == nil {
		t.Fatal("a forget whose delete failed answered nil")
	}
	if n, _, err := j.ForgetAll(); err == nil || n != 0 {
		t.Fatalf("forget all whose deletes failed: %d forgotten, %v", n, err)
	}
	if page, _ := j.List("", 10); len(page.Records) != 2 {
		t.Fatalf("listed after the failed deletes: %+v", page.Records)
	}
	beforeDelete = nil
	if err := j.Forget(sid(1)); err != nil || exists(dir, sid(1)) {
		t.Fatalf("forget again: %v", err)
	}
	if n, _, err := j.ForgetAll(); err != nil || n != 1 || exists(dir, sid(2)) {
		t.Fatalf("forget all again: %d %v", n, err)
	}
}

// A journal that does not write answers its list from the files, reading
// a file again only when its size or time changed: a file changed behind
// its back with both kept is not read again, one changed is, one removed
// leaves the list.
func TestAReadOnlyListReadsOnlyWhatChanged(t *testing.T) {
	dir := t.TempDir()
	for i := 1; i <= 3; i++ {
		r := serverRecord(sid(i))
		r.V, r.StartedAt, r.EndedAt = V, time.Date(2026, 11, 5, 9, i, 0, 0, time.UTC), time.Date(2026, 11, 5, 10, 0, 0, 0, time.UTC)
		writeRecord(t, dir, r)
	}
	heldThenFree(t)
	j := openAt(t, dir, Options{})
	if ro, _ := j.ReadOnly(); !ro {
		t.Fatal("the journal holds the lock")
	}
	names := func() map[string]string {
		t.Helper()
		page, err := j.List("", 10)
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]string{}
		for _, s := range page.Records {
			out[s.ID] = s.Name
		}
		return out
	}
	if got := names(); len(got) != 3 {
		t.Fatalf("the list: %v", got)
	}
	path := func(id string) string { return filepath.Join(dir, Dir, SessionsDir, id+".json") }
	// The same size and time, other bytes: not read again.
	fi, err := os.Stat(path(sid(1)))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path(sid(1)), bytes.Repeat([]byte{' '}, int(fi.Size())), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path(sid(1)), fi.ModTime(), fi.ModTime()); err != nil {
		t.Fatal(err)
	}
	// Renamed, a new size: read again.
	r := serverRecord(sid(2))
	r.V, r.Name, r.StartedAt, r.EndedAt = V, "renamed since", time.Date(2026, 11, 5, 9, 2, 0, 0, time.UTC), time.Date(2026, 11, 5, 10, 0, 0, 0, time.UTC)
	writeRecord(t, dir, r)
	if err := os.Remove(path(sid(3))); err != nil {
		t.Fatal(err)
	}
	got := names()
	if len(got) != 2 || got[sid(1)] != "cat #1" || got[sid(2)] != "renamed since" {
		t.Fatalf("the list after the changes: %v", got)
	}
}

// staleQuietly marks the record of id stale without waking the writer, as
// a Changed whose wake the writer has not yet taken.
func staleQuietly(j *Journal, id string) {
	j.mu.Lock()
	j.recs[id].stale = true
	j.mu.Unlock()
}

// An end the writer finds in the metadata it reads is written by the same
// pass: a Flush returns with it written, and so does Close, whose pass is
// the last.
func TestAnEndFoundInTheMetadataIsWrittenByItsPass(t *testing.T) {
	slowFlush(t)
	end := time.Date(2026, 11, 5, 10, 0, 0, 0, time.UTC)
	j, dir := newJournal(t, Options{Current: func(string) (Meta, bool) {
		return Meta{Name: "cat #1", Ended: true, EndedAt: end, Ending: "exited (exit 0)"}, true
	}})
	begin(t, j, serverRecord(sid(1)))
	begin(t, j, serverRecord(sid(2)))
	staleQuietly(j, sid(1))
	j.Flush()
	if r, _ := onDisk(t, dir, sid(1)); !r.EndedAt.Equal(end) {
		t.Fatalf("after the Flush: %+v", r)
	}
	staleQuietly(j, sid(2))
	j.Close()
	if r, _ := onDisk(t, dir, sid(2)); !r.EndedAt.Equal(end) {
		t.Fatalf("after Close: %+v", r)
	}
}

// gateDelete holds every delete of a record's file until the test lets it
// go, answering fail's error then.
func gateDelete(t *testing.T, fail error) (entered <-chan string, release func()) {
	in := make(chan string, 8)
	gate := make(chan struct{})
	var once sync.Once
	beforeDelete = func(name string) error {
		in <- name
		<-gate
		return fail
	}
	release = func() { once.Do(func() { close(gate) }) }
	t.Cleanup(func() {
		release()
		beforeDelete = nil
	})
	return in, release
}

// messages is what the entries of r say, in their order.
func messages(r Record) []string {
	var out []string
	for _, e := range r.Entries {
		out = append(out, e.Message)
	}
	return out
}

// A hosted Begin of an id whose record a removal is deleting is a record
// begun after the removal: the removal deletes the record it chose, and
// the new one is written once the delete is done, nothing of the old one
// in it. Should the delete fail, the record kept is taken up again, as a
// Begin after the failed removal would: from its file, or from memory
// when it was still there, what came meanwhile put among it.
func TestAHostedBeginDuringItsRemoval(t *testing.T) {
	for _, tc := range []struct {
		name             string
		inMemory, failed bool
	}{
		{"from its file", false, false},
		{"from memory", true, false},
		{"from its file, the delete failed", false, true},
		{"from memory, the delete failed", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			slowFlush(t)
			dir := t.TempDir()
			id := sid(1)
			if !tc.inMemory {
				writeRecord(t, dir, awayRecord(id, "o1"))
			}
			j := openAt(t, dir, Options{})
			if tc.inMemory {
				begin(t, j, hostedRecord(id, "o1"))
				j.Note(id, Activity{Type: actProgress, Message: "before"})
				j.End(id, Ending{How: EndingHostAway, Lost: true})
				j.Flush()
				if !inMemory(j, id) {
					t.Fatal("the record left memory")
				}
			}
			var fail error
			if tc.failed {
				fail = errors.New("a disk that did not answer")
			}
			entered, release := gateDelete(t, fail)
			forgot := make(chan error, 1)
			go func() { forgot <- j.Forget(id) }()
			<-entered // the removal chose the record and holds its delete
			begin(t, j, hostedRecord(id, "o1"))
			j.Note(id, Activity{Type: actProgress, Message: "after"})
			release()
			if err := <-forgot; (err != nil) != tc.failed {
				t.Fatalf("the removal answered %v", err)
			}
			j.Flush()
			r, _ := onDisk(t, dir, id)
			want, reopened := []string{"after"}, 0
			if tc.failed {
				want, reopened = []string{"before", "taken up again by its host", "after"}, 1
			}
			if got := messages(r); !slices.Equal(got, want) || r.Reopened != reopened || !r.LostAt.IsZero() || r.Ending != "" {
				t.Fatalf("the record: entries %q reopened %d lost %v ending %q", got, r.Reopened, r.LostAt, r.Ending)
			}
			if page, _ := j.List("", 10); len(page.Records) != 1 || !page.Records[0].LostAt.IsZero() {
				t.Fatalf("listed %+v", page.Records)
			}
			if err := j.Forget(id); !errors.Is(err, ErrLive) {
				t.Fatalf("forget the record begun: %v", err)
			}
		})
	}
}

// gateDenied is a Denied that holds its first question until the test
// lets it go, then answers every path as answer does.
func gateDenied(t *testing.T, answer func(path string) (bool, bool)) (o Options, entered <-chan string, release func()) {
	in := make(chan string, 64)
	gate := make(chan struct{})
	var once sync.Once
	release = func() { once.Do(func() { close(gate) }) }
	t.Cleanup(release)
	o.Denied = func(_, path string) (bool, bool) {
		select {
		case in <- path:
		default:
		}
		<-gate
		return answer(path)
	}
	return o, in, release
}

// A removal (Forget, ForgetAll, Prune) against a record in each state the
// writer's fold passes through: an open record is refused or kept while it
// folds; an ended one being folded (its deny list asked, held here) or
// folded and being written (its write held) is deleted, and nothing of it
// is written after.
func TestARemovalAgainstTheFold(t *testing.T) {
	removals := []struct {
		name   string
		setup  func(t *testing.T)
		remove func(j *Journal, id string) (removed bool, err error)
	}{
		{"forget", func(*testing.T) {}, func(j *Journal, id string) (bool, error) {
			err := j.Forget(id)
			return err == nil, err
		}},
		{"forget all", func(*testing.T) {}, func(j *Journal, _ string) (bool, error) {
			n, _, err := j.ForgetAll()
			return n == 1, err
		}},
		{"prune", func(t *testing.T) {
			// Set before the journal opens: its writer prunes too.
			maxRecords = 0
			t.Cleanup(func() { maxRecords = MaxRecords })
		}, func(j *Journal, id string) (bool, error) {
			j.Prune()
			j.mu.Lock()
			defer j.mu.Unlock()
			_, kept := j.index[id]
			return !kept, nil
		}},
	}
	at := time.Date(2026, 11, 5, 9, 0, 0, 0, time.UTC)
	for _, rm := range removals {
		t.Run(rm.name+", open, folding", func(t *testing.T) {
			slowFlush(t)
			rm.setup(t)
			o, entered, release := gateDenied(t, func(string) (bool, bool) { return false, true })
			j, dir := newJournal(t, o)
			id := sid(1)
			begin(t, j, serverRecord(id))
			j.Touch(id, File{Path: "/w/main.go", Op: "edit", Last: at})
			go j.Flush()
			<-entered
			removed, err := rm.remove(j, id)
			if removed || (rm.name == "forget" && !errors.Is(err, ErrLive)) {
				t.Fatalf("an open record removed: %v %v", removed, err)
			}
			release()
			j.Flush()
			if r, _ := onDisk(t, dir, id); len(r.Files) != 1 {
				t.Fatalf("the open record's files %+v", r.Files)
			}
		})
		t.Run(rm.name+", ended, folding", func(t *testing.T) {
			slowFlush(t)
			rm.setup(t)
			o, entered, release := gateDenied(t, func(string) (bool, bool) { return false, true })
			j, dir := newJournal(t, o)
			id := sid(1)
			begin(t, j, serverRecord(id))
			j.Touch(id, File{Path: "/w/main.go", Op: "edit", Last: at})
			j.End(id, Ending{At: at.Add(time.Second), How: "exited (exit 0)"})
			<-entered // the end's pass asks the deny list, held
			if removed, err := rm.remove(j, id); !removed || err != nil {
				t.Fatalf("the ended record was not removed: %v", err)
			}
			release()
			j.Flush()
			if exists(dir, id) || inMemory(j, id) {
				t.Fatal("the record removed while it folded was written again")
			}
			if page, _ := j.List("", 10); len(page.Records) != 0 {
				t.Fatalf("listed %+v", page.Records)
			}
		})
		t.Run(rm.name+", ended, folded", func(t *testing.T) {
			slowFlush(t)
			rm.setup(t)
			j, dir := newJournal(t, Options{})
			id := sid(1)
			begin(t, j, serverRecord(id))
			j.Touch(id, File{Path: "/w/main.go", Op: "edit", Last: at})
			in := make(chan struct{}, 8)
			gate := make(chan struct{})
			beforeWrite = func(name string) error {
				if name == SessionsDir+"/"+id+".json" {
					in <- struct{}{}
					<-gate
				}
				return nil
			}
			t.Cleanup(func() { beforeWrite = nil })
			j.End(id, Ending{At: at.Add(time.Second), How: "exited (exit 0)"})
			<-in // folded, its write under way and held
			done := make(chan error, 1)
			go func() {
				removed, err := rm.remove(j, id)
				if err == nil && !removed {
					err = errors.New("not removed")
				}
				done <- err
			}()
			close(gate)
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			beforeWrite = nil
			j.Flush()
			if exists(dir, id) || inMemory(j, id) {
				t.Fatal("the record removed after its fold was written again")
			}
		})
	}
}

// A touch the server can no longer judge (Denied answers it does not list
// the session) is judged by the deny list the session left with (Gone),
// also when the session left while the fold was asking: the list is read
// again for it. Until that list comes the touch waits, written nowhere; in
// a record a late hook brought back, whose Gone is past, it is refused.
func TestATouchWaitsForTheListTheSessionLeftWith(t *testing.T) {
	slowFlush(t)
	c := newClock()
	o, entered, release := gateDenied(t, func(string) (bool, bool) { return false, false })
	o.Now = c.Now
	j, dir := newJournal(t, o)
	id := sid(1)
	begin(t, j, serverRecord(id))
	j.Touch(id, File{Path: "/w/secret.key", Op: "read"})
	j.Touch(id, File{Path: "/w/main.go", Op: "edit"})
	go j.Flush()
	<-entered // the fold asks Denied, which will answer it no longer lists the session
	j.Gone(id, Meta{Name: "cat #1"}, func(path string) bool { return path == "/w/secret.key" })
	release()
	j.Flush()
	if r, raw := onDisk(t, dir, id); len(r.Files) != 1 || r.Files[0].Path != "/w/main.go" || bytes.Contains(raw, []byte("secret.key")) {
		t.Fatalf("files %+v", r.Files)
	}

	// Another session, no list yet: the touch waits for it.
	other := sid(2)
	begin(t, j, serverRecord(other))
	j.Touch(other, File{Path: "/w/kept.go", Op: "edit"})
	j.Flush()
	if r, _ := onDisk(t, dir, other); len(r.Files) != 0 {
		t.Fatalf("judged with no list: %+v", r.Files)
	}
	j.Gone(other, Meta{Name: "cat #1"}, func(string) bool { return false })
	j.End(other, Ending{At: c.Now().Add(time.Second), How: "exited (exit 0)"})
	j.Flush()
	if r, _ := onDisk(t, dir, other); len(r.Files) != 1 || r.Files[0].Path != "/w/kept.go" {
		t.Fatalf("after its list came: %+v", r.Files)
	}
	// It leaves memory; a late touch brings it back, and no list will come.
	c.add(2 * time.Hour)
	j.Flush()
	if inMemory(j, other) {
		t.Fatal("the ended record is still in memory")
	}
	j.Touch(other, File{Path: "/w/late.go", Op: "edit", Last: c.Now().Add(-time.Hour)})
	j.Flush()
	if r, _ := onDisk(t, dir, other); len(r.Files) != 1 {
		t.Fatalf("a late touch no list judged was kept: %+v", r.Files)
	}
}

// A hosted record's entries and files carry its host's clock and its end
// the server's: what the host sends is kept whatever the two clocks say,
// in its place by the host's; a server record still drops what comes
// after its end.
func TestAHostedRecordKeepsItsHostsEvidenceByItsClock(t *testing.T) {
	j, dir := newJournal(t, Options{})
	hosted, server := sid(1), sid(2)
	begin(t, j, hostedRecord(hosted, "o1"))
	begin(t, j, serverRecord(server))
	end := time.Date(2026, 11, 5, 9, 0, 0, 0, time.UTC)
	ahead := end.Add(time.Hour) // the host's clock, an hour ahead of the server's
	for _, id := range []string{hosted, server} {
		j.End(id, Ending{At: end, How: "exited (exit 0)"})
		j.Note(id, Activity{At: ahead, Type: actProgress, Message: "before the exit, by the host's clock"})
		j.Touch(id, File{Path: "/w/main.go", Op: "edit", Last: ahead})
	}
	j.Flush()
	h, _ := onDisk(t, dir, hosted)
	if got := messages(h); !slices.Equal(got, []string{"before the exit, by the host's clock"}) || len(h.Files) != 1 || h.Dropped != 0 {
		t.Fatalf("the hosted record: entries %q files %+v dropped %d", got, h.Files, h.Dropped)
	}
	s, _ := onDisk(t, dir, server)
	if len(s.Entries) != 0 || len(s.Files) != 0 || s.Dropped != 2 {
		t.Fatalf("the server record: entries %+v files %+v dropped %d", s.Entries, s.Files, s.Dropped)
	}
}

// Close's last pass held at its write: what comes then is refused (an
// end, an entry), Close has not returned, and once it does the record
// holds its first end, once, and what the last pass wrote; an end commit
// read that answers after Close changes nothing on disk.
func TestCloseLastPassHeld(t *testing.T) {
	slowFlush(t)
	old := closeWait
	closeWait = 50 * time.Millisecond
	t.Cleanup(func() { closeWait = old })
	late := make(chan struct{})
	lateClosed := sync.OnceFunc(func() { close(late) })
	t.Cleanup(lateClosed)
	started := make(chan struct{}, 1)
	returned := make(chan struct{})
	first, second := sid(1), sid(2)
	j, dir := newJournal(t, Options{EndCommit: func(_ context.Context, r Record) string {
		if r.ID != second {
			return ""
		}
		defer close(returned)
		started <- struct{}{}
		<-late
		return "0123456789abcdef0123456789abcdef01234567"
	}})
	begin(t, j, serverRecord(first))
	begin(t, j, serverRecord(second))
	at := time.Now().UTC()
	j.End(first, Ending{At: at.Add(time.Second), How: "exited (exit 0)"})
	j.Flush()
	j.Note(first, Activity{At: at, Type: actStatus, Message: "kept by the last pass"})
	j.End(second, Ending{How: "stopped"})
	<-started
	in := make(chan struct{}, 8)
	gate := make(chan struct{})
	beforeWrite = func(name string) error {
		if name == SessionsDir+"/"+first+".json" {
			in <- struct{}{}
			<-gate
		}
		return nil
	}
	t.Cleanup(func() { beforeWrite = nil })
	closed := make(chan struct{})
	go func() {
		j.Close()
		close(closed)
	}()
	<-in // the last pass writes the first record, held
	j.End(first, Ending{How: "stopped"})
	j.Note(first, Activity{At: at, Type: actStatus, Message: "after Close began"})
	select {
	case <-closed:
		t.Fatal("Close returned with its last pass held")
	default:
	}
	close(gate)
	<-closed
	r, _ := onDisk(t, dir, first)
	if got := messages(r); r.Ending != "exited (exit 0)" || !slices.Contains(got, "kept by the last pass") || slices.Contains(got, "after Close began") {
		t.Fatalf("the first record after Close: ending %q entries %q", r.Ending, got)
	}
	before, _ := os.ReadFile(filepath.Join(dir, Dir, SessionsDir, second+".json"))
	lateClosed()
	<-returned
	j.Flush()
	after, _ := os.ReadFile(filepath.Join(dir, Dir, SessionsDir, second+".json"))
	if s, _ := onDisk(t, dir, second); s.EndedAt.IsZero() || s.End != "" || !bytes.Equal(before, after) {
		t.Fatalf("the second record after its late read: %+v", s)
	}
}

// A fold under way (held at its deny list) while a removal deletes the
// record and a hosted Begin of its id comes: the delete done, the record
// begun after it takes nothing of the old one's touch; the delete failed,
// the record taken up again takes it.
func TestAFoldUnderWayFollowsTheRecordItsRemovalLeaves(t *testing.T) {
	for _, failed := range []bool{false, true} {
		t.Run(fmt.Sprintf("the delete failed: %v", failed), func(t *testing.T) {
			slowFlush(t)
			// The deny list's first question is the touch below.
			o, folding, releaseFold := gateDenied(t, func(string) (bool, bool) { return false, true })
			j, dir := newJournal(t, o)
			id := sid(1)
			begin(t, j, hostedRecord(id, "o1"))
			j.Note(id, Activity{Type: actProgress, Message: "before"})
			j.End(id, Ending{How: EndingHostAway, Lost: true})
			j.Flush()
			j.Touch(id, File{Path: "/w/old.go", Op: "edit"})
			flushed := make(chan struct{})
			go func() {
				j.Flush()
				close(flushed)
			}()
			<-folding // the old record's touch is being judged, held
			var fail error
			if failed {
				fail = errors.New("a disk that did not answer")
			}
			deleting, releaseDelete := gateDelete(t, fail)
			forgot := make(chan error, 1)
			go func() { forgot <- j.Forget(id) }()
			<-deleting
			begin(t, j, hostedRecord(id, "o1"))
			j.Note(id, Activity{Type: actProgress, Message: "after"})
			releaseDelete()
			if err := <-forgot; (err != nil) != failed {
				t.Fatalf("the removal answered %v", err)
			}
			releaseFold()
			<-flushed
			j.Flush()
			r, _ := onDisk(t, dir, id)
			want, files := []string{"after"}, 0
			if failed {
				want, files = []string{"before", "taken up again by its host", "after"}, 1
			}
			if got := messages(r); !slices.Equal(got, want) || len(r.Files) != files {
				t.Fatalf("the record: entries %q files %+v", got, r.Files)
			}
		})
	}
}

// listed says whether the journal's list holds id.
func listed(t *testing.T, j *Journal, id string) bool {
	t.Helper()
	page, err := j.List("", MaxPage)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range page.Records {
		if s.ID == id {
			return true
		}
	}
	return false
}

// A forgotten record leaves what a reader is handed before its file goes:
// held between the two (the delete held, the file still there), Get
// answers ErrNotFound and List leaves it out, though the record was in
// memory; a delete that fails puts it back. A Get already reading the
// record's file when Forget begins ends before the record is hidden and
// its file goes, and none after Forget hands it out.
func TestAForgottenRecordIsHiddenBeforeItsFileGoes(t *testing.T) {
	for _, failed := range []bool{false, true} {
		t.Run(fmt.Sprintf("the delete failed: %v", failed), func(t *testing.T) {
			slowFlush(t)
			j, dir := newJournal(t, Options{})
			id := sid(1)
			begin(t, j, serverRecord(id))
			j.End(id, Ending{How: "exited (exit 0)"})
			j.Flush()
			if !inMemory(j, id) {
				t.Fatal("the record left memory")
			}
			var fail error
			if failed {
				fail = errors.New("a disk that did not answer")
			}
			deleting, release := gateDelete(t, fail)
			forgot := make(chan error, 1)
			go func() { forgot <- j.Forget(id) }()
			<-deleting // its file still there, its delete held
			if !exists(dir, id) {
				t.Fatal("the file went before the delete")
			}
			if _, _, err := j.Get(id); !errors.Is(err, ErrNotFound) {
				t.Fatalf("Get during the forget: %v", err)
			}
			if listed(t, j, id) {
				t.Fatal("listed during the forget")
			}
			release()
			if err := <-forgot; (err != nil) != failed {
				t.Fatalf("the forget answered %v", err)
			}
			_, _, err := j.Get(id)
			if failed != (err == nil) || failed != listed(t, j, id) {
				t.Fatalf("after the forget: Get %v, listed %v", err, listed(t, j, id))
			}
		})
	}
	t.Run("a read under way", func(t *testing.T) {
		slowFlush(t)
		c := newClock()
		j, dir := newJournal(t, Options{Now: c.Now})
		id := sid(1)
		begin(t, j, serverRecord(id))
		j.End(id, Ending{At: c.Now(), How: "exited (exit 0)"})
		j.Flush()
		c.add(2 * time.Hour)
		j.Flush()
		if inMemory(j, id) {
			t.Fatal("the record is still in memory")
		}
		reading, gate := make(chan struct{}), make(chan struct{})
		beforeGet = func(string) {
			close(reading)
			<-gate
		}
		t.Cleanup(func() { beforeGet = nil })
		got := make(chan error, 1)
		go func() {
			_, _, err := j.Get(id)
			got <- err
		}()
		<-reading
		beforeGet = nil
		deleting, release := gateDelete(t, nil)
		forgot := make(chan error, 1)
		go func() { forgot <- j.Forget(id) }()
		// The forget waits for the read under way before it hides the
		// record (rmu): nothing is deleted meanwhile.
		select {
		case <-deleting:
			t.Fatal("the file went while a read begun before was under way")
		case err := <-forgot:
			t.Fatalf("the forget returned while a read was under way: %v", err)
		default:
		}
		close(gate)
		if err := <-got; err != nil {
			t.Fatalf("the read begun before the forget: %v", err)
		}
		<-deleting
		release()
		if err := <-forgot; err != nil || exists(dir, id) {
			t.Fatalf("the forget: %v", err)
		}
		if _, _, err := j.Get(id); !errors.Is(err, ErrNotFound) {
			t.Fatalf("a Get after the forget: %v", err)
		}
	})
}
