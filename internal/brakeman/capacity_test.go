package brakeman

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/capacity"
)

// TestVisibilityCapacityDefaults: a new configuration sees capacity; one
// saved before the class existed, which holds no capacity field, reads it
// off, and a turn under it does not see the class.
func TestVisibilityCapacityDefaults(t *testing.T) {
	fx := newFixture(t)
	c, err := fx.s.Config()
	if err != nil {
		t.Fatal(err)
	}
	if !c.Visibility.Capacity || !c.Visibility.Has(ClassCapacity) {
		t.Fatalf("a new configuration's visibility: %+v", c.Visibility)
	}
	b, err := json.Marshal(DefaultConfig("/srv/work"))
	if err != nil {
		t.Fatal(err)
	}
	var old map[string]any
	if err := json.Unmarshal(b, &old); err != nil {
		t.Fatal(err)
	}
	delete(old["visibility"].(map[string]any), "capacity")
	old["rev"] = 4
	if err := fx.s.st.Save(ConfigFile, old); err != nil {
		t.Fatal(err)
	}
	c, err = fx.s.Config()
	if err != nil {
		t.Fatal(err)
	}
	if c.Visibility.Capacity || c.Visibility.Has(ClassCapacity) || !c.Visibility.Runs || c.Rev != 4 {
		t.Fatalf("a configuration saved before: %+v", c)
	}
}

// TestCapacityRefKind: capacity:<agent>/<account>@<readAt> reads back as it
// was written, its readAt in UTC as time.RFC3339Nano writes it; any other
// shape is refused; the table names its form.
func TestCapacityRefKind(t *testing.T) {
	for _, s := range []string{
		"capacity:claude/default@2026-11-11T03:00:00Z",
		"capacity:codex-2/a-abcdefghijk2@2026-11-11T03:00:00.123456789Z",
	} {
		r, err := ParseRef(s)
		if err != nil || r.Kind != RefCapacity || r.String() != s || r.Valid() != nil {
			t.Errorf("%s: %+v %v", s, r, err)
		}
	}
	r, _ := ParseRef("capacity:claude/default@2026-11-11T03:00:00Z")
	if r.Agent != "claude" || r.Account != "default" || r.ReadAt != "2026-11-11T03:00:00Z" {
		t.Errorf("the parts: %+v", r)
	}
	for _, s := range []string{
		"capacity:claude/default",
		"capacity:claude@2026-11-11T03:00:00Z",
		"capacity:Claude/default@2026-11-11T03:00:00Z",
		"capacity:claude/work@2026-11-11T03:00:00Z",
		"capacity:claude/unlisted@2026-11-11T03:00:00Z",
		"capacity:claude/default@2026-11-11T03:00:00+01:00",
		"capacity:claude/default@2026-11-11T03:00:00.100Z",
		"capacity:claude/default@yesterday",
		"capacity:claude/default/x@2026-11-11T03:00:00Z",
	} {
		if r, err := ParseRef(s); err == nil {
			t.Errorf("%s parsed: %+v", s, r)
		}
	}
	if (Ref{Kind: RefCapacity, Agent: "claude", Account: "default", ReadAt: "2026-11-11T03:00:00Z", Run: "x"}).Valid() == nil {
		t.Error("a capacity ref holding a run is valid")
	}
	found := false
	for _, f := range RefForms() {
		if f.Kind == RefCapacity {
			found = f.Form == "capacity:<agent>/<account>@<readAt>"
			if _, err := ParseRef(f.Example); err != nil {
				t.Errorf("the example: %v", err)
			}
		}
	}
	if !found {
		t.Error("the table has no capacity form")
	}
}

// largestView is a view with every field the capacity view holds at its
// bound: 16 windows and 4 credit figures, each text at its longest in
// characters of four bytes, a label of 60 characters.
func largestView(at time.Time) capacity.View {
	long := func(n int) string { return strings.Repeat("𝄞", n/4) }
	yes := true
	fig := long(capacity.MaxFigure)
	since := at.Add(-time.Hour)
	v := capacity.View{
		Agent:   capacity.ViewAgent{ID: strings.Repeat("a", 32), Name: strings.Repeat("𝄞", 60), Adapter: "claude", Version: strings.Repeat("9", capacity.MaxVersion)},
		Account: capacity.ViewAccount{ID: "a-abcdefghijk2", Label: strings.Repeat("𝄞", capacity.MaxLabel), Default: true, Plan: long(capacity.MaxPlan)},
		Host:    capacity.ViewHost{Kind: capacity.HostNative, Name: "the-servers-own-name"},
		State:   capacity.StateOK, Words: "a reader's own words",
		Limits: capacity.Limits{Reached: long(capacity.MaxShort), SpendControl: &yes, OrdinaryAllowed: &yes},
		Counts: capacity.Counts{Running: 1 << 20, Started5h: 1 << 20, Started7d: 1 << 20, Since: &since},
		ReadAt: &at, ReadBy: &capacity.ReadBy{Program: long(capacity.MaxShort), Version: strings.Repeat("9", capacity.MaxVersion), Qualified: strings.Repeat("9", capacity.MaxVersion)},
		Fresh: capacity.FreshNow, EntryLogin: "CLAUDE_CONFIG_DIR", Env: "hash", Filed: "claude:default",
	}
	for range capacity.MaxWindows {
		reset := at.Add(time.Hour)
		v.Windows = append(v.Windows, capacity.Window{Kind: long(capacity.MaxShort), Name: long(capacity.MaxName), Minutes: 1 << 20, Percent: 100, ResetsAt: &reset, Severity: long(capacity.MaxShort), Active: &yes, Limit: long(capacity.MaxShort)})
	}
	for range capacity.MaxCredits {
		v.Credits = append(v.Credits, capacity.Credit{Kind: long(capacity.MaxShort), Enabled: &yes, Used: &fig, Limit: &fig, Balance: &fig, Unlimited: true, Currency: long(capacity.MaxShort)})
	}
	return v
}

// TestCapacityOfKeepsItsList: what Brakeman reads of a view is the listed
// fields, at most MaxCapacity bytes with each at its largest: never the
// server's name, the variable an entry sets its login with, a reader's
// words or the reading's hashes; the words are the state's own; the ref
// names the reading; a version past its bound is left out; and nothing
// it holds is the view's own pointer.
func TestCapacityOfKeepsItsList(t *testing.T) {
	at := time.Date(2026, 11, 11, 3, 0, 0, 123, time.FixedZone("CST", -6*3600))
	v := largestView(at)
	c := CapacityOf(v, at)
	b, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	if len(b) > MaxCapacity || checkCapacity("c", &c) != nil {
		t.Fatalf("the largest is %d bytes, more than %d", len(b), MaxCapacity)
	}
	for _, leak := range []string{"the-servers-own-name", "CLAUDE_CONFIG_DIR", "a reader's own words", `"hash"`, "claude:default", "entryLogin", `"name":"the`} {
		if strings.Contains(string(b), leak) {
			t.Errorf("it holds %q: %s", leak, b)
		}
	}
	var fields map[string]any
	if err := json.Unmarshal(b, &fields); err != nil {
		t.Fatal(err)
	}
	for k := range fields {
		switch k {
		case "ref", "agent", "account", "host", "state", "words", "windows", "credits", "limits", "counts", "countedAt", "readAt", "readBy", "fresh":
		default:
			t.Errorf("a field not listed: %s", k)
		}
	}
	if c.Words != capacity.StateWords(capacity.StateOK) || c.Host != capacity.HostNative || c.Ref != "capacity:"+v.Agent.ID+"/a-abcdefghijk2@2026-11-11T09:00:00.000000123Z" {
		t.Errorf("the words, host and ref: %q %q %q", c.Words, c.Host, c.Ref)
	}
	if c.Agent.Version == "" || len(c.Windows) != capacity.MaxWindows || len(c.Credits) != capacity.MaxCredits || c.Counts.Running != 1<<20 || !c.CountedAt.Equal(at) {
		t.Errorf("a field lost: %+v", c)
	}
	*v.Windows[0].ResetsAt = at.Add(48 * time.Hour)
	*v.Credits[0].Used = "changed"
	*v.Counts.Since = at
	if c.Windows[0].ResetsAt.Equal(*v.Windows[0].ResetsAt) || *c.Credits[0].Used == "changed" || c.Counts.Since.Equal(at) {
		t.Error("it shares a pointer with the view")
	}
	v.Agent.Version += "9"
	if c := CapacityOf(v, at); c.Agent.Version != "" {
		t.Errorf("a version past its bound: %q", c.Agent.Version)
	}
	never := CapacityOf(capacity.View{Agent: capacity.ViewAgent{ID: "claude"}, Account: capacity.ViewAccount{ID: "default"}, State: capacity.StateNeverRead, Words: "x", Fresh: capacity.FreshNone}, at)
	if never.Ref != "" || never.ReadAt != nil || never.Windows == nil || never.Credits == nil || never.Words != capacity.StateWords(capacity.StateNeverRead) {
		t.Errorf("a view never read: %+v", never)
	}
}

// A request and a brief's resolution keep a capacity view of at most
// MaxCapacity bytes and refuse a larger one, writing nothing.
func TestCapacityKeptWithinItsBound(t *testing.T) {
	fx := newFixture(t)
	at := fx.clock.now()
	c := CapacityOf(largestView(at), at)
	r, created, err := fx.s.Create(Request{OpID: "cap-1", Kind: KindBriefing, Capacity: &c})
	if err != nil || !created {
		t.Fatalf("a request with the largest view: %v", err)
	}
	if got, err := fx.s.Get(r.ID); err != nil || got.Capacity == nil || got.Capacity.Ref != c.Ref || len(got.Capacity.Windows) != capacity.MaxWindows {
		t.Fatalf("read back: %+v %v", got.Capacity, err)
	}
	big := c
	big.Windows = append(append([]capacity.Window{}, c.Windows...), c.Windows...)
	if _, _, err := fx.s.Create(Request{OpID: "cap-2", Kind: KindBriefing, Capacity: &big}); !errors.Is(err, ErrBound) {
		t.Fatalf("a request past the bound: %v", err)
	}
	if _, found, _ := fx.s.ByOp("cap-2"); found {
		t.Fatal("a refused request was kept")
	}
}
