package crew

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/proto"
	"github.com/rocksolidlabs/conductor/internal/session"
	"github.com/rocksolidlabs/conductor/internal/store"
)

// The record's origin and the builds before it: a record that carries an
// origin is refused by an older build's strict reader and left as it was;
// a record without one reads and writes as before.

// olderRun is the run type of the last release before the record's
// origin (v0.7.0-rc.24's Run, frozen here: its members, in its order,
// with no Origin), and olderStoredRun its records' form on disk. Its
// nested types are this build's: what the older build refuses is the
// member it does not have.
type olderRun struct {
	ID          string                  `json:"id"`
	CrewID      string                  `json:"crewId"`
	Name        string                  `json:"name"`
	Label       string                  `json:"label,omitempty"`
	Goal        string                  `json:"goal"`
	Cwd         string                  `json:"cwd"`
	Isolation   string                  `json:"isolation"`
	StartedAt   time.Time               `json:"startedAt"`
	StoppedAt   *time.Time              `json:"stoppedAt,omitempty"`
	Members     []MemberState           `json:"members"`
	Yolo        bool                    `json:"yolo"`
	ResumedFrom string                  `json:"resumedFrom,omitempty"`
	ResumedBy   string                  `json:"resumedBy,omitempty"`
	State       string                  `json:"state"`
	NeedsInput  int                     `json:"needsInput"`
	Log         []session.ActivityEntry `json:"log"`
	Managed     *Managed                `json:"managed,omitempty"`
	Paused      bool                    `json:"paused,omitempty"`
	PausedWhy   string                  `json:"pausedWhy,omitempty"`
	Outcome     string                  `json:"outcome,omitempty"`
	Tasks       []TaskState             `json:"tasks,omitempty"`
	Manifest    *RunManifest            `json:"manifest,omitempty"`
	Cleaning    int                     `json:"cleaning,omitempty"`
	Chat        []proto.ChatMessage     `json:"chat,omitempty"`
	Ticket      *RunTicket              `json:"ticket,omitempty"`
	Class       string                  `json:"class,omitempty"`
	Request     string                  `json:"request,omitempty"`
	People      []Person                `json:"people,omitempty"`
	Compare     *RunCompare             `json:"compare,omitempty"`
}

type olderStoredRun struct {
	olderRun
	Bound map[string]session.BoundAccount `json:"bound,omitempty"`
}

// recordsIn is a Records over a store in a directory of the test's, and
// the directory.
func recordsIn(t *testing.T) (*Records, string) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	rs, err := NewRecords(st)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(rs.Close)
	return rs, dir
}

// stoppedRecord is a stopped managed run's record, with the origin given.
func stoppedRecord(origin *RunOrigin) Run {
	at := time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)
	return Run{ID: "api-sweep-0000beef", CrewID: "api-sweep", Name: "API sweep", Cwd: "/work", Isolation: IsolationWorktree, State: RunStopped,
		StartedAt: at, StoppedAt: &at, Managed: &Managed{Revision: 3}, Origin: origin,
		Log: []session.ActivityEntry{{Type: session.ActivityStatus, Message: "launched by Ada through example"}}}
}

// An older build reads a record strictly (store.DecodeStrict, as its
// Records do): a record that carries an origin is refused, naming the
// member, and the file is left byte for byte as it was. The same record
// with no origin is read by it.
func TestOlderReaderRefusesAndKeepsTheRecord(t *testing.T) {
	rs, dir := recordsIn(t)
	rec := stoppedRecord(&RunOrigin{Provider: "example", Ref: "example:42", Label: "Review of change 42", Deadline: time.Date(2026, 11, 18, 10, 0, 0, 0, time.UTC), LimitSeconds: 3600})
	if err := rs.Save(rec); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "runs", rec.ID+".json")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var older olderStoredRun
	if err := store.DecodeStrict(before, &older); err == nil || !strings.Contains(err.Error(), `"origin"`) {
		t.Fatalf("the older build's read of a record with an origin: %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(after, before) {
		t.Fatalf("the record changed: %v", err)
	}
	rec.ID, rec.Origin = "api-sweep-0000cafe", nil
	if err := rs.Save(rec); err != nil {
		t.Fatal(err)
	}
	plain, err := os.ReadFile(filepath.Join(dir, "runs", rec.ID+".json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.DecodeStrict(plain, &olderStoredRun{}); err != nil {
		t.Fatalf("the older build's read of a record with no origin: %v", err)
	}
}

// The frozen type is the run type as it was: every member of today's Run
// but Origin, in the same order with the same tags.
func TestTheOlderRunTypeIsTodaysWithoutOrigin(t *testing.T) {
	var today, older []string
	rt := reflect.TypeFor[Run]()
	for i := range rt.NumField() {
		if f := rt.Field(i); f.Name != "Origin" {
			today = append(today, f.Name+" "+string(f.Tag))
		}
	}
	ot := reflect.TypeFor[olderRun]()
	for i := range ot.NumField() {
		f := ot.Field(i)
		older = append(older, f.Name+" "+string(f.Tag))
	}
	if !reflect.DeepEqual(today, older) {
		t.Fatalf("the run type gained or lost a member besides Origin:\n%v\nfrozen:\n%v", today, older)
	}
}

// A record with no origin reads as before and is written back byte for
// byte: no origin member appears in it.
func TestOlderRecordReadsUnchanged(t *testing.T) {
	rs, dir := recordsIn(t)
	rec := stoppedRecord(nil)
	if err := rs.Save(rec); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "runs", rec.ID+".json")
	first, err := os.ReadFile(path)
	if err != nil || bytes.Contains(first, []byte(`"origin"`)) {
		t.Fatalf("the record: %v\n%s", err, first)
	}
	got, ok, err := rs.Get(rec.ID)
	if err != nil || !ok || got.Origin != nil {
		t.Fatalf("read back: %+v %v %v", got.Origin, ok, err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := rs.Save(got); err != nil {
		t.Fatal(err)
	}
	second, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(first, second) {
		t.Fatalf("written back:\n%s\nwas:\n%s", second, first)
	}
}

// A record's origin with every member at its bound, each byte one the
// encoder writes as six, is within maxRunOrigin, and reads back as it was.
func TestRunRecordOriginWithinItsBound(t *testing.T) {
	worst := func(n int) string { return strings.Repeat("\x01", n) }
	o := RunOrigin{Provider: strings.Repeat("p", 40), Ref: worst(200), Label: strings.Repeat(" ", 60), Subject: worst(512),
		SubjectURL: worst(2048), Prior: strings.Repeat("a", 64) + "-0000000f", Project: "A12345", StartCommit: strings.Repeat("b", 64),
		Deadline: time.Date(2026, 11, 18, 10, 0, 0, 0, time.UTC), LimitSeconds: 86400, Continued: true}
	b, err := json.Marshal(o)
	if err != nil {
		t.Fatal(err)
	}
	if len(b) > maxRunOrigin || len(b) < 6*(200+512+2048) {
		t.Fatalf("the origin at its bound encodes to %d bytes, the bound %d", len(b), maxRunOrigin)
	}
	var back RunOrigin
	if err := json.Unmarshal(b, &back); err != nil || back != o {
		t.Fatalf("read back: %+v %v", back, err)
	}
}
