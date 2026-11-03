package session

import (
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/rocksolidlabs/conductor/internal/proto"
)

func TestActivityRingKeepsNewestOldestFirst(t *testing.T) {
	var r activityRing
	for i := 0; i < MaxActivity+25; i++ {
		r.Add(ActivityEntry{Type: "link", Message: fmt.Sprintf("m%d", i)})
	}
	snap := r.Snapshot()
	if len(snap) != MaxActivity {
		t.Fatalf("len %d, want %d", len(snap), MaxActivity)
	}
	if snap[0].Message != "m25" || snap[len(snap)-1].Message != fmt.Sprintf("m%d", MaxActivity+24) {
		t.Fatalf("order: first %q last %q", snap[0].Message, snap[len(snap)-1].Message)
	}
	if r.Add(ActivityEntry{Type: "link"}); r.Snapshot()[0].Message != "m26" {
		t.Fatal("ring did not drop the oldest entry")
	}
}

func TestActivityRingStampsTime(t *testing.T) {
	var r activityRing
	r.Add(ActivityEntry{Type: "join", ByName: "Priya"})
	if r.Snapshot()[0].At.IsZero() {
		t.Fatal("At must be stamped when missing")
	}
}

func TestCleanEntryCapsFields(t *testing.T) {
	e := CleanEntry(ActivityEntry{Type: "artifact", URL: strings.Repeat("u", 3000), To: strings.Repeat("t", 100), Tool: strings.Repeat("x", 200), Message: "a\x1bb"})
	if len(e.URL) != MaxEventURL || len([]rune(e.To)) != MaxEventTo || len(e.Tool) != MaxEventTool || e.Message != "ab" {
		t.Fatalf("%+v", e)
	}
	if ValidEventType("bogus") || !ValidEventType("handoff") || !ValidEventType("join") {
		t.Fatal("ValidEventType")
	}
}

func TestValidEventType(t *testing.T) {
	for _, typ := range []string{
		ActivityAttention, ActivityInput, ActivityJoin, ActivityLeave, ActivityLink, ActivityStatus,
		ActivityProgress, ActivityArtifact, ActivityHandoff, ActivityToolUse, ActivityToolDenied, ActivityError, ActivityFile,
	} {
		if !ValidEventType(typ) {
			t.Errorf("%q must be a valid type", typ)
		}
	}
	// Attention states are not entry types, and matching is exact.
	for _, typ := range []string{"", "bogus", "Progress", " progress", "tool-use", "needs_input", "working", "done", "clear"} {
		if ValidEventType(typ) {
			t.Errorf("%q must not be a valid type", typ)
		}
	}
}

func TestCleanEntryStripsAndTrims(t *testing.T) {
	got := CleanEntry(ActivityEntry{
		Type:    ActivityArtifact,
		ByName:  " agent\x00 ",
		Message: "  line one\nline two\x07 ",
		URL:     " https://example.com/a\r\nb\x00 ",
		To:      "\treviewer\n",
		Tool:    " Bash\x1b[31m ",
	})
	// Message and Tool follow CleanMessage, which keeps line breaks; URL, To and
	// ByName are single-line.
	want := ActivityEntry{Type: ActivityArtifact, ByName: "agent", Message: "line one\nline two", URL: "https://example.com/ab", To: "reviewer", Tool: "Bash[31m"}
	if got != want {
		t.Fatalf("got  %+v\nwant %+v", got, want)
	}
}

func TestCleanEntryLeavesEmptyFieldsEmpty(t *testing.T) {
	want := ActivityEntry{Type: ActivityProgress}
	if got := CleanEntry(want); got != want {
		t.Fatalf("got %+v, want the entry unchanged (no default name, nothing invented)", got)
	}
}

func TestCleanEntryCutsAtCharacterBoundaries(t *testing.T) {
	// "€" is three bytes: a byte limit that is not a multiple of three lands
	// inside a character, and the cut must back up to the one before it.
	e := CleanEntry(ActivityEntry{
		To:   strings.Repeat("é", 100), // the limit counts runes
		Tool: strings.Repeat("€", 100), // the limit counts bytes: 33 whole characters fit in 100
		URL:  strings.Repeat("€", 1000),
	})
	if got := len([]rune(e.To)); got != MaxEventTo || !utf8.ValidString(e.To) {
		t.Errorf("To: %d runes, valid %v", got, utf8.ValidString(e.To))
	}
	if len(e.Tool) != 99 || !utf8.ValidString(e.Tool) {
		t.Errorf("Tool: %d bytes, valid %v; want 99 bytes", len(e.Tool), utf8.ValidString(e.Tool))
	}
	if len(e.URL) != 2046 || !utf8.ValidString(e.URL) {
		t.Errorf("URL: %d bytes, valid %v; want 2046 bytes", len(e.URL), utf8.ValidString(e.URL))
	}
}

// JSON escapes & < > as six bytes each, so text near its limits can outgrow
// one CONTROL frame. The relay rejects such a frame and closes the host's
// connection, so a cleaned entry must always fit.
func TestCleanEntryFitsOneControlFrame(t *testing.T) {
	worst := ActivityEntry{
		Type:    ActivityArtifact,
		By:      strings.Repeat("b", IDLen),
		ByName:  strings.Repeat("&", 200),
		Message: strings.Repeat("<", 1000),
		URL:     strings.Repeat("&", 3000),
		To:      strings.Repeat(">", 100),
		Tool:    strings.Repeat("&", 300),
	}
	e := CleanEntry(worst)
	e.At = longestStamp // stamped, as the ring stamps what it stores
	frame := proto.MustControl(EntryToProto(e))
	if _, err := proto.Decode(frame); err != nil {
		t.Fatalf("cleaned entry does not fit a control frame: %v (%d bytes)", err, len(frame))
	}
	if e.URL != "" {
		t.Errorf("a URL of %d escaped bytes must be dropped, got %d bytes", 6*MaxEventURL, len(e.URL))
	}
	if e.Message == "" || e.To == "" || e.Tool == "" || e.ByName == "" {
		t.Errorf("only the URL may be dropped: %+v", e)
	}

	// A busy query string that still fits is kept whole.
	url := "https://example.com/?" + strings.Repeat("a=b&", 500)
	if got := CleanEntry(ActivityEntry{Type: ActivityArtifact, URL: url}); got.URL != url {
		t.Errorf("a URL that fits was changed: %d bytes -> %d", len(url), len(got.URL))
	}
}

// An entry is cleaned before the ring stamps it, and a stamp adds up to ten
// bytes to the message: a URL that fits only without one must not survive.
func TestCleanEntryLeavesRoomForTheTimestamp(t *testing.T) {
	// Measured with the shortest stamp there is: no fraction of a second.
	shortest, err := json.Marshal(EntryToProto(ActivityEntry{Type: ActivityArtifact, At: time.Date(2026, 9, 29, 17, 14, 20, 0, time.UTC)}))
	if err != nil {
		t.Fatal(err)
	}
	room := proto.MaxControl - len(shortest) - len(`,"url":""`)
	e := CleanEntry(ActivityEntry{Type: ActivityArtifact, URL: strings.Repeat("&", room/6)}) // 6 bytes each once escaped
	e.At = time.Date(2026, 9, 29, 17, 14, 20, 999999999, time.UTC)                           // the longest RFC 3339 stamp
	b, err := json.Marshal(EntryToProto(e))
	if err != nil {
		t.Fatal(err)
	}
	if len(b) > proto.MaxControl {
		t.Fatalf("cleaned, then stamped: %d bytes, limit %d", len(b), proto.MaxControl)
	}
}

// CleanID holds an id from outside the session to one line: control
// characters and surrounding space go, and an id still longer than its limit
// is dropped, not cut, since a cut id would name someone else.
func TestCleanID(t *testing.T) {
	for in, want := range map[string]string{
		"0123456789abcdef":               "0123456789abcdef",
		" 01234567\x0089abcdef\n":        "0123456789abcdef",
		"\x1b[31m":                       "[31m",
		"\x00\x07\t ":                    "",
		strings.Repeat("b", 17):          "",
		"\x7f" + strings.Repeat("b", 16): strings.Repeat("b", 16),
		strings.Repeat("é", 9):           "", // 9 characters, 18 bytes
		"":                               "",
	} {
		if got := CleanID(in, 16); got != want {
			t.Errorf("CleanID(%q, 16) = %q, want %q", in, got, want)
		}
	}
}

func TestActivityRingCleansEntries(t *testing.T) {
	var r activityRing
	e := r.Add(ActivityEntry{Type: ActivityArtifact, URL: strings.Repeat("u", 3000), To: strings.Repeat("t", 100), Tool: strings.Repeat("x", 200)})
	if len(e.URL) != MaxEventURL || len([]rune(e.To)) != MaxEventTo || len(e.Tool) != MaxEventTool {
		t.Fatalf("Add returned an unbounded entry: %d %d %d", len(e.URL), len(e.To), len(e.Tool))
	}
	if s := r.Snapshot()[0]; s != e {
		t.Fatalf("stored %+v, returned %+v", s, e)
	}
}

func TestEventBucketRefillsAtTheConfiguredRate(t *testing.T) {
	var b EventBucket
	start := time.Now()
	took := func(at time.Time, attempts int) (n int) {
		for range attempts {
			if b.Take(at) {
				n++
			}
		}
		return n
	}
	if got := took(start, EventBurst+10); got != EventBurst {
		t.Fatalf("a fresh bucket allows a burst of %d, took %d", EventBurst, got)
	}
	if got := took(start, 10); got != 0 {
		t.Fatalf("an empty bucket must not refill without time passing, took %d", got)
	}
	if got := took(start.Add(500*time.Millisecond), 100); got != EventRatePerSecond/2 {
		t.Fatalf("half a second refills %d, took %d", EventRatePerSecond/2, got)
	}
	// A long pause banks one burst at most.
	if got := took(start.Add(time.Hour), 3*EventBurst); got != EventBurst {
		t.Fatalf("after a long pause a burst is %d, took %d", EventBurst, got)
	}
	// A clock that steps back neither refills nor breaks the bucket.
	if got := took(start, 5); got != 0 {
		t.Fatalf("time going backwards refilled %d", got)
	}
	if got := took(start.Add(time.Hour+time.Second), 100); got != EventRatePerSecond {
		t.Fatalf("one second after the last call refills %d, took %d", EventRatePerSecond, got)
	}
}

// The zero bucket is full, so an owner needs no constructor: a HostedSession
// holds one as a field, under its own lock.
func TestEventBucketStartsFull(t *testing.T) {
	var b EventBucket
	now := time.Now()
	for i := 0; i < EventBurst; i++ {
		if !b.Take(now) {
			t.Fatalf("token %d of %d refused by a bucket nobody has touched", i+1, EventBurst)
		}
	}
	if b.Take(now) {
		t.Fatalf("token %d granted", EventBurst+1)
	}
}

// Server and host exchange entries as `activity` messages, and a viewer gets
// the same message from the session: one pair of functions makes it, so a
// field added to an entry cannot be sent one way and lost the other.
func TestEntryProtoCarriesEveryField(t *testing.T) {
	var e ActivityEntry
	v := reflect.ValueOf(&e).Elem()
	for i := 0; i < v.NumField(); i++ {
		f, name := v.Field(i), v.Type().Field(i).Name
		switch {
		case f.Kind() == reflect.String:
			f.SetString("value of " + name)
		case f.Type() == reflect.TypeOf(time.Time{}):
			f.Set(reflect.ValueOf(time.Date(2026, 9, 29, 12, 0, 0, 5, time.UTC)))
		default:
			t.Fatalf("ActivityEntry.%s is a %s: teach EntryToProto and EntryFromProto, and this test, about it", name, f.Type())
		}
	}
	msg := EntryToProto(e)
	if got := EntryFromProto(msg); got != e {
		t.Fatalf("entry -> message -> entry\n got %+v\nwant %+v", got, e)
	}
	if msg.T != proto.CtlActivity {
		t.Fatalf("message discriminator %q", msg.T)
	}
	// And the other way: no field of the message is left behind either.
	var m proto.Activity
	mv := reflect.ValueOf(&m).Elem()
	for i := 0; i < mv.NumField(); i++ {
		f, name := mv.Field(i), mv.Type().Field(i).Name
		if f.Kind() != reflect.String {
			t.Fatalf("proto.Activity.%s is a %s: teach this test about it", name, f.Type())
		}
		f.SetString("value of " + name)
	}
	m.T, m.At = proto.CtlActivity, "2026-09-29T12:00:00.000000005Z"
	if got := EntryToProto(EntryFromProto(m)); got != m {
		t.Fatalf("message -> entry -> message\n got %+v\nwant %+v", got, m)
	}
}

func TestEntryToProtoLeavesOutAMissingTime(t *testing.T) {
	msg := EntryToProto(ActivityEntry{Type: ActivityProgress, Message: "1/7"})
	if msg.At != "" {
		t.Fatalf("an entry without a time was sent at %q", msg.At)
	}
	stamped := EntryToProto(ActivityEntry{Type: ActivityProgress, At: time.Date(2026, 9, 29, 14, 0, 0, 0, time.FixedZone("CDT", -5*3600))})
	if stamped.At != "2026-09-29T19:00:00Z" {
		t.Fatalf("time %q: want UTC", stamped.At)
	}
}

func TestEntryFromProtoLeavesAnUnreadableTimeZero(t *testing.T) {
	for _, at := range []string{"", "yesterday", "2026-09-29", "0000-00-00T00:00:00Z", "0001-01-01T00:00:00Z"} {
		if e := EntryFromProto(proto.Activity{Type: ActivityProgress, At: at}); !e.At.IsZero() {
			t.Errorf("at %q was read as %v", at, e.At)
		}
	}
	want := time.Date(2026, 9, 29, 12, 0, 0, 123456789, time.UTC)
	if e := EntryFromProto(proto.Activity{Type: ActivityProgress, At: "2026-09-29T07:00:00.123456789-05:00"}); !e.At.Equal(want) || e.At.Location() != time.UTC {
		t.Fatalf("at read as %v, want %v in UTC", e.At, want)
	}
}

// ExitCode reads the code of a process that exited on its own from the
// message of its status entry, as the session writes it, and nothing else: a
// process an admin stopped ends with a signal, which is not the agent
// failing. It agrees with exitCode in web/app/utils/events.ts.
func TestExitCode(t *testing.T) {
	for _, tc := range []struct {
		message string
		code    int
		ok      bool
	}{
		{"exited (exit 1)", 1, true},
		{"exited (exit 0)", 0, true},
		{"exited (exit 143)", 143, true},
		{"exited (exit -1)", -1, true},
		{"exited (exit 007)", 7, true},
		{"exited (exit 99999999999999999999)", math.MaxInt, true},
		{"stopped (exit 143)", 0, false},
		{"stopped", 0, false},
		{"exited", 0, false},
		{"exited (exit )", 0, false},
		{"exited (exit -)", 0, false},
		{"exited (exit 1x)", 0, false},
		{"exited (exit +1)", 0, false},
		{"exited (exit ١)", 0, false},
		{" exited (exit 1)", 0, false},
		{"exited (exit 1) ", 0, false},
		{"exited (exit 1", 0, false},
		{"running", 0, false},
		{"", 0, false},
	} {
		if code, ok := ExitCode(tc.message); code != tc.code || ok != tc.ok {
			t.Errorf("ExitCode(%q) = %d, %v; want %d, %v", tc.message, code, ok, tc.code, tc.ok)
		}
	}
}

// A file event names its op and path within bounds; a repeat of the newest
// one (the same path and op within FileCoalesce) is one entry whose time
// moves; another file, another op or a later repeat is its own line.
func TestFileEventsAreBoundedAndCoalesced(t *testing.T) {
	e := CleanEntry(ActivityEntry{Type: ActivityFile, Op: "edit", Path: " /r/" + strings.Repeat("p", 2000) + "\x00x "})
	if e.Op != "edit" || len(e.Path) != MaxEventPath || strings.Contains(e.Path, "\x00") {
		t.Fatalf("cleaned %+v", e)
	}
	if e := CleanEntry(ActivityEntry{Type: ActivityFile, Op: "burn", Path: "x"}); e.Op != "" {
		t.Fatalf("an unknown op kept: %q", e.Op)
	}
	s, _ := newLocal(t, t.TempDir())
	at := time.Date(2026, 10, 8, 8, 33, 0, 0, time.UTC)
	if !s.Record(ActivityEntry{Type: ActivityFile, Op: "edit", Path: "/r/a.go", Tool: "Edit", At: at}) {
		t.Fatal("the first was dropped")
	}
	if !s.Record(ActivityEntry{Type: ActivityFile, Op: "edit", Path: "/r/a.go", Tool: "Edit", At: at.Add(time.Second)}) {
		t.Fatal("the repeat was dropped")
	}
	s.Record(ActivityEntry{Type: ActivityFile, Op: "read", Path: "/r/a.go", Tool: "Read", At: at.Add(2 * time.Second)})
	s.Record(ActivityEntry{Type: ActivityFile, Op: "edit", Path: "/r/b.go", Tool: "Edit", At: at.Add(3 * time.Second)})
	s.Record(ActivityEntry{Type: ActivityFile, Op: "edit", Path: "/r/b.go", Tool: "Edit", At: at.Add(10 * time.Second)})
	// A tools launch reports the call itself between the base hook's file and its own: still one line.
	s.Record(ActivityEntry{Type: "tool_use", Tool: "Edit", At: at.Add(11 * time.Second)})
	s.Record(ActivityEntry{Type: ActivityFile, Op: "edit", Path: "/r/b.go", Tool: "Edit", At: at.Add(12 * time.Second)})
	var files []ActivityEntry
	for _, e := range s.Activity() {
		if e.Type == ActivityFile {
			files = append(files, e)
		}
	}
	if len(files) != 4 {
		t.Fatalf("%d file entries: %+v", len(files), files)
	}
	if files[0].Path != "/r/a.go" || files[0].Op != "edit" || !files[0].At.Equal(at.Add(time.Second)) {
		t.Fatalf("the coalesced entry: %+v", files[0])
	}
	if files[1].Op != "read" || files[2].Path != "/r/b.go" || files[3].Path != "/r/b.go" || !files[3].At.Equal(at.Add(12*time.Second)) {
		t.Fatalf("the rest: %+v", files[1:])
	}
}

// An entry stamped before it is added (an attention's, whose time is its
// Since, added once the session's lock is let go) holds the window's
// reach: a read between its stamp and its add is whole only up to just
// before it, so a reader that moves past what it read never moves past
// the entry still to come; once it is added the reach is now again, and
// an entry added later is of a time after a reach read before it.
func TestAStampedEntryHoldsTheWindowsReach(t *testing.T) {
	var r activityRing
	r.Add(ActivityEntry{Type: ActivityProgress, Message: "before"})
	at := r.Stamp(2)
	r.Add(ActivityEntry{Type: ActivityProgress, Message: "between"})
	snap, through := r.SnapshotThrough()
	if !through.Before(at) {
		t.Fatalf("read between the stamp and its entries: whole up to %v, the stamp %v", through, at)
	}
	for _, e := range snap {
		if e.Type == ActivityAttention {
			t.Fatalf("the stamped entry is in the read: %+v", snap)
		}
	}
	r.Add(ActivityEntry{At: at, Type: ActivityAttention, Message: "done"})
	if _, through = r.SnapshotThrough(); !through.Before(at) {
		t.Fatalf("one of the stamp's two entries still to come: whole up to %v, the stamp %v", through, at)
	}
	r.Add(ActivityEntry{At: at, Type: ActivityAttention, Message: "asked"})
	snap, through = r.SnapshotThrough()
	if through.Before(at) || len(snap) != 4 {
		t.Fatalf("every entry added: whole up to %v, the stamp %v, %d entries", through, at, len(snap))
	}
	if e := r.Add(ActivityEntry{Type: ActivityProgress, Message: "after"}); e.At.Before(through) {
		t.Fatalf("an entry added after a read is of %v, before the read's reach %v", e.At, through)
	}
}

// A session's attention change stamps its entry with the attention's
// Since, and the window's reach covers it once it is added.
func TestAnAttentionsEntryIsOfItsSince(t *testing.T) {
	s, _ := newLocal(t, t.TempDir())
	s.SetAttention(AttentionNeedsInput, "Proceed?", SourceAPI)
	since := s.Info().Attention.Since
	window, through := s.ActivityThrough()
	if since == nil || len(window) == 0 {
		t.Fatalf("no attention entry: %v %+v", since, window)
	}
	last := window[len(window)-1]
	if last.Type != ActivityAttention || !last.At.Equal(*since) || through.Before(*since) {
		t.Fatalf("the entry %+v, Since %v, whole up to %v", last, *since, through)
	}
}
