package exttest

import (
	"errors"
	"slices"
	"testing"

	"github.com/rocksolidlabs/conductor/ext"
)

// The Sink check hands a sink that lists decision, and one that takes
// every entry, a durable decision's notification as the server types it:
// event and type decision, state open, its ref, a question and the run
// page's address; a sink that lists neither is handed none.
func TestSinkCheckHandsADecisionWithItsRef(t *testing.T) {
	n := NewNetwork()
	for _, c := range []struct {
		name   string
		events []string
		want   bool
	}{
		{"lists-decision", []string{"decision"}, true},
		{"takes-all", nil, true},
		{"lists-needs-input", []string{"needs_input"}, false},
	} {
		sink := NewFakeSink(c.name, c.events)
		Sink(t, n, sink)
		var got []ext.Notification
		for _, note := range sink.Notifications() {
			if note.Type == "decision" {
				got = append(got, note)
			}
		}
		if !c.want {
			if len(got) != 0 {
				t.Errorf("%s: handed a decision: %+v", c.name, got)
			}
			continue
		}
		if len(got) == 0 {
			t.Fatalf("%s: handed no decision's notification", c.name)
		}
		for _, d := range got {
			id, ok := ext.ParseDecisionRef(d.Ref)
			if d.Event != "decision" || d.State != "open" || !ok || id != "dr-1" || d.Message == "" || d.URL == "" || d.Valid() != nil {
				t.Errorf("%s: a decision's notification not as the server types it: %+v", c.name, d)
			}
		}
	}
}

// The fake's OpenDecisionRefs: an after that is no decision's ref refused
// as the server refuses it, no refs by default, what OnOpenDecisionRefs
// set answered, every call recorded.
func TestFakeHostOpenDecisionRefs(t *testing.T) {
	ctx := waitCtx(t)
	h := NewFakeHost(t)
	refs, next, err := h.OpenDecisionRefs(ctx, "")
	if err != nil || refs != nil || next != "" {
		t.Fatalf("by default: %v %q %v", refs, next, err)
	}
	for _, after := range []string{"dr-7", "decision:dr-07", "decision:7", "question:x/y"} {
		if refs, _, err := h.OpenDecisionRefs(ctx, after); !errors.Is(err, ext.ErrRefused) || refs != nil {
			t.Errorf("%q: %v %v", after, refs, err)
		}
	}
	var asked []string
	h.OnOpenDecisionRefs(func(after string) ([]string, string, error) {
		asked = append(asked, after)
		if after == "" {
			return []string{"decision:dr-1", "decision:dr-4"}, "decision:dr-4", nil
		}
		return []string{"decision:dr-9"}, "", nil
	})
	if refs, next, err := h.OpenDecisionRefs(ctx, ""); err != nil || !slices.Equal(refs, []string{"decision:dr-1", "decision:dr-4"}) || next != "decision:dr-4" {
		t.Fatalf("the first page: %v %q %v", refs, next, err)
	}
	if refs, next, err := h.OpenDecisionRefs(ctx, "decision:dr-4"); err != nil || !slices.Equal(refs, []string{"decision:dr-9"}) || next != "" {
		t.Fatalf("the last page: %v %q %v", refs, next, err)
	}
	if _, _, err := h.OpenDecisionRefs(ctx, "decision:dr-04"); !errors.Is(err, ext.ErrRefused) {
		t.Fatalf("a malformed after with a script set: %v", err)
	}
	if !slices.Equal(asked, []string{"", "decision:dr-4"}) {
		t.Fatalf("the script was asked %q", asked)
	}
	want := []string{"", "dr-7", "decision:dr-07", "decision:7", "question:x/y", "", "decision:dr-4", "decision:dr-04"}
	if got := h.OpenDecisionRefsAsked(); !slices.Equal(got, want) {
		t.Fatalf("recorded %q, want %q", got, want)
	}
	h.Close()
	if _, _, err := h.OpenDecisionRefs(ctx, ""); err == nil {
		t.Fatal("answered once closed")
	}
}
