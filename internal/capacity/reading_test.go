package capacity

import (
	"bytes"
	"context"
	"encoding/json"
	"maps"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

func viewJSON(t *testing.T, v View) map[string]any {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	// What the CLI decodes strictly reads back whole.
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	var back View
	if err := dec.Decode(&back); err != nil {
		t.Fatalf("the view does not decode strictly: %v", err)
	}
	return m
}

func TestMissingWindowIsAbsent(t *testing.T) {
	clock := newClock()
	reads := []Reading{
		{State: StateOK, Windows: []Window{{Kind: "weekly_all", Name: "Weekly, all models", Percent: 0}}}, // reported at 0%
		{State: StateOK},      // no window reported
		{State: StateKeyPaid}, // a key-paid login
		{State: StateError, Words: "the tool's words"},
	}
	for i, rd := range reads {
		c := NewCache(CacheOptions{Clock: clock, Read: func(context.Context, Key, Intent) Reading { return rd }})
		v := c.Read(context.Background(), key(i), ReadOptions{})
		c.Close()
		m := viewJSON(t, v)
		ws, ok := m["windows"].([]any)
		if !ok || len(ws) != len(rd.Windows) {
			t.Errorf("reading %d: windows %v", i, m["windows"])
		}
		b, _ := json.Marshal(v)
		if got, want := strings.Count(string(b), `"percent"`), len(rd.Windows); got != want {
			t.Errorf("reading %d: %d percents for %d windows: %s", i, got, want, b)
		}
	}
	// A reported window at 0% says 0; an absent one says nothing.
	c := NewCache(CacheOptions{Clock: clock, Read: func(context.Context, Key, Intent) Reading { return reads[0] }})
	defer c.Close()
	b, _ := json.Marshal(c.Read(context.Background(), key(9), ReadOptions{}))
	if !strings.Contains(string(b), `"percent":0`) || !strings.Contains(string(b), `"source":"provider"`) {
		t.Errorf("a window at 0%%: %s", b)
	}
	// Never read: no window, no figure, no time.
	m := viewJSON(t, c.View(key(10)))
	if len(m["windows"].([]any)) != 0 || len(m["credits"].([]any)) != 0 || m["readAt"] != nil || m["readBy"] != nil {
		t.Errorf("never read: %v", m)
	}
}

func TestCountsApartFromProvider(t *testing.T) {
	yes := true
	used, limit := "12.50", "50.00"
	reset := t0.Add(time.Hour)
	since := t0.Add(-time.Hour)
	v := View{
		State:   StateOK,
		Windows: []Window{{Source: "conductor", Kind: "session", Name: "5-hour", Minutes: 300, Percent: 95, ResetsAt: &reset, Severity: "critical"}},
		Credits: []Credit{{Kind: "extra_usage", Enabled: &yes, Used: &used, Limit: &limit, Currency: "XXX"}},
		Limits:  Limits{Source: "conductor", OrdinaryAllowed: &yes},
		// A count set with the wrong marks is still written as the
		// server's estimate.
		Counts: Counts{Source: "provider", Estimate: false, Running: 2, Started5h: 5, Started7d: 9, Since: &since},
		Fresh:  FreshNow,
	}
	m := viewJSON(t, v)
	counts := m["counts"].(map[string]any)
	if counts["source"] != SourceConductor || counts["estimate"] != true || counts["running"] != 2.0 || counts["started5h"] != 5.0 {
		t.Errorf("counts: %v", counts)
	}
	for _, w := range m["windows"].([]any) {
		if w.(map[string]any)["source"] != SourceProvider {
			t.Errorf("a window: %v", w)
		}
		if _, ok := w.(map[string]any)["estimate"]; ok {
			t.Errorf("a window marked an estimate: %v", w)
		}
	}
	for _, c := range m["credits"].([]any) {
		if c.(map[string]any)["source"] != SourceProvider {
			t.Errorf("a credit: %v", c)
		}
	}
	if m["limits"].(map[string]any)["source"] != SourceProvider {
		t.Errorf("limits: %v", m["limits"])
	}
	// The counts' fields are under counts alone.
	for _, f := range []string{"running", "started5h", "started7d", "estimate"} {
		for name, val := range m {
			if name == "counts" {
				continue
			}
			b, _ := json.Marshal(val)
			if strings.Contains(string(b), `"`+f+`"`) {
				t.Errorf("%s under %s: %s", f, name, b)
			}
		}
	}
	// Limits not reported are absent, never false.
	b, _ := json.Marshal(Limits{})
	if string(b) != `{"source":"provider"}` {
		t.Errorf("limits not reported: %s", b)
	}
}

func TestEveryStateHasWords(t *testing.T) {
	states := []string{"ok", "reading", "unsupported", "not_installed", "signed_out", "key_paid", "unqualified", "remote", "env_set", "unlisted", "not_owner", "error", "never_read"}
	for _, s := range states {
		if StateWords(s) == "" {
			t.Errorf("%s has no words", s)
		}
	}
	if got := slices.Sorted(maps.Keys(stateWords)); !slices.Equal(got, slices.Sorted(slices.Values(states))) {
		t.Errorf("the states with words are %v", got)
	}
	// A kept reading of every state but ok says why, in its own words when
	// it has them and in its state's otherwise.
	clock := newClock()
	for i, s := range []string{StateSignedOut, StateKeyPaid, StateUnqualified, StateError} {
		c := NewCache(CacheOptions{Clock: clock, Read: func(context.Context, Key, Intent) Reading { return Reading{State: s} }})
		v := c.Read(context.Background(), key(i), ReadOptions{})
		c.Close()
		if v.State != s || v.Words != StateWords(s) {
			t.Errorf("%s kept as %s: %q", s, v.State, v.Words)
		}
	}
	c := NewCache(CacheOptions{Clock: clock, Read: func(context.Context, Key, Intent) Reading {
		return Reading{State: StateError, Words: strings.Repeat("w", 2*MaxWords)}
	}})
	defer c.Close()
	// Words are for people: cut to the bound, and the cut is said.
	if v := c.Read(context.Background(), key(9), ReadOptions{}); len(v.Words) > MaxWords || !strings.HasSuffix(v.Words, cutMark) || !strings.HasPrefix(v.Words, "www") {
		t.Errorf("the tool's words kept as %d bytes: %q", len(v.Words), v.Words)
	}
}

func TestViewMarkEnv(t *testing.T) {
	clock := newClock()
	c := NewCache(CacheOptions{Clock: clock, Read: func(context.Context, Key, Intent) Reading {
		return Reading{State: StateOK, Env: "sum-a"}
	}})
	defer c.Close()
	v := c.Read(context.Background(), key(1), ReadOptions{})
	if v.MarkEnv("sum-a"); v.Fresh != FreshNow || v.Words != "" {
		t.Errorf("the same environment: %s %q", v.Fresh, v.Words)
	}
	if v.MarkEnv("sum-b"); v.Fresh != FreshStale || v.Words != EnvWords {
		t.Errorf("another environment: %s %q", v.Fresh, v.Words)
	}
	if b, _ := json.Marshal(v); strings.Contains(string(b), "sum-a") {
		t.Errorf("the hash in the JSON: %s", b)
	}
	never := c.View(key(2))
	if never.MarkEnv("sum-b"); never.Fresh != FreshNone {
		t.Errorf("never read: %s", never.Fresh)
	}
}

// A reading's Filed, its reader's caller's, reaches the View as it was,
// for every state a reading keeps, and never its JSON.
func TestReadingFiledCarried(t *testing.T) {
	for i, r := range []Reading{{State: StateOK}, {State: StateSignedOut}, {State: "nonsense"}} {
		r.Filed = "claude:default"
		c := NewCache(CacheOptions{Clock: newClock(), Read: func(context.Context, Key, Intent) Reading { return r }})
		v := c.Read(context.Background(), key(i), ReadOptions{})
		c.Close()
		if b, _ := json.Marshal(v); v.Filed != r.Filed || strings.Contains(string(b), r.Filed) {
			t.Errorf("%s: filed %q, %s", r.State, v.Filed, b)
		}
	}
}

// TestReadingFields holds the fields of what the cache keeps and shows.
// A field added to one of them must be added here, and, when it is a
// pointer, a slice or a map, to the copies Reading.kept and Reading.view
// make (clone) and to TestCacheKeepsCopies, so no kept value is shared.
func TestReadingFields(t *testing.T) {
	fields := func(v any) []string {
		var out []string
		ty := reflect.TypeOf(v)
		for i := range ty.NumField() {
			out = append(out, ty.Field(i).Name+" "+ty.Field(i).Type.String())
		}
		return out
	}
	for _, c := range []struct {
		v    any
		want []string
	}{
		{Reading{}, []string{"Key capacity.Key", "State string", "Words string", "Plan string", "Windows []capacity.Window", "Credits []capacity.Credit", "Limits capacity.Limits",
			"Program string", "Version string", "Qualified string", "ReadAt time.Time", "Env string", "Filed string"}},
		{Window{}, []string{"Source string", "Kind string", "Name string", "Minutes int", "Percent int", "ResetsAt *time.Time", "Severity string", "Active *bool", "Limit string"}},
		{Credit{}, []string{"Source string", "Kind string", "Enabled *bool", "Used *string", "Limit *string", "Balance *string", "Unlimited bool", "Currency string"}},
		{Limits{}, []string{"Source string", "Reached string", "SpendControl *bool", "OrdinaryAllowed *bool"}},
		{View{}, []string{"Agent capacity.ViewAgent", "Model capacity.ViewModel", "Account capacity.ViewAccount", "Host capacity.ViewHost", "State string", "Words string",
			"Windows []capacity.Window", "Credits []capacity.Credit", "Limits capacity.Limits", "Counts capacity.Counts", "ReadAt *time.Time", "ReadBy *capacity.ReadBy",
			"Fresh string", "NextRead *time.Time", "EntryLogin string", "Env string", "Filed string"}},
	} {
		if got := fields(c.v); !slices.Equal(got, c.want) {
			t.Errorf("%T's fields:\n%v\nwant\n%v", c.v, got, c.want)
		}
	}
}
