package ext_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/ext"
)

// The one form of a notification's ref: decision:dr-<n>, n of 1 to 18
// digits with no leading zero; DecisionRef writes what ParseDecisionRef
// reads back.
func TestDecisionRefForm(t *testing.T) {
	for _, c := range []struct{ ref, id string }{
		{"decision:dr-7", "dr-7"},
		{"decision:dr-1", "dr-1"},
		{"decision:dr-10", "dr-10"},
		{"decision:dr-999999999999999999", "dr-999999999999999999"},
	} {
		id, ok := ext.ParseDecisionRef(c.ref)
		if !ok || id != c.id {
			t.Errorf("%q: %q %v, want %q", c.ref, id, ok, c.id)
		}
		if got := ext.DecisionRef(id); got != c.ref {
			t.Errorf("DecisionRef(%q) = %q, want %q", id, got, c.ref)
		}
		if len(c.ref) > ext.MaxNotificationRef {
			t.Errorf("%q is past MaxNotificationRef", c.ref)
		}
	}
	if longest := "decision:dr-999999999999999999"; len(longest) != ext.MaxNotificationRef {
		t.Errorf("MaxNotificationRef %d, the longest ref %d bytes", ext.MaxNotificationRef, len(longest))
	}
	for _, ref := range []string{
		"decision:7",
		"decision:dr-07",
		"decision:dr-0",
		"decision:dr-1234567890123456789",
		"decisions:dr-2,dr-7",
		"decision:dr-2,dr-7",
		"decision:dr-7 ",
		" decision:dr-7",
		"decision:dr-7\n",
		"decision:dr-7\ndecision:dr-8",
		"Decision:dr-7",
		"decision:DR-7",
		"decision:dr-",
		"decision:dr--7",
		"decision:dr-+7",
		"decision:dr-7/1",
		"decision:",
		"dr-7",
		"question:x/y",
		"",
	} {
		if id, ok := ext.ParseDecisionRef(ref); ok || id != "" {
			t.Errorf("%q taken as %q", ref, id)
		}
	}
}

// A provider's notice may carry no ref or one of the one form; any other
// is refused with the field named and without its text.
func TestNotificationRefValid(t *testing.T) {
	notice := ext.Notification{Message: "a decision waits"}
	if err := notice.Valid(); err != nil {
		t.Fatalf("no ref: %v", err)
	}
	notice.Ref = "decision:dr-42"
	if err := notice.Valid(); err != nil {
		t.Fatalf("a valid ref: %v", err)
	}
	for _, ref := range []string{"decision:7", "decision:dr-07", "decision:dr-0", "decision:dr-1234567890123456789",
		"decisions:dr-2,dr-7", "decision:dr-7 ", "question:x/y", "decision:dr-7\ndecision:dr-8", strings.Repeat("x", 4000)} {
		notice.Ref = ref
		err := notice.Valid()
		if err == nil {
			t.Errorf("%q taken", ref)
			continue
		}
		if !strings.Contains(err.Error(), "ref ") {
			t.Errorf("%q: the refusal %q names no field", ref, err)
		}
		if len(ref) > 4 && strings.Contains(err.Error(), ref) {
			t.Errorf("%q: the refusal %q repeats the text", ref, err)
		}
	}
}

// A notification with no ref marshals as it did before the field: no
// "ref" key, every other byte the same.
func TestNotificationRefOmittedFromJSON(t *testing.T) {
	n := ext.Notification{Event: "decision", Type: "decision", At: time.Date(2026, 11, 19, 9, 0, 0, 0, time.UTC),
		Session: ext.SessionRef{ID: "s1", Run: "team-00000001", Task: "t1"}, State: "open", Message: "Ship it?", URL: "/runs/team-00000001?task=t1"}
	got, err := json.Marshal(n)
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"event":"decision","type":"decision","at":"2026-11-19T09:00:00Z","session":{"id":"s1","run":"team-00000001","task":"t1"},"state":"open","message":"Ship it?","url":"/runs/team-00000001?task=t1"}`
	if string(got) != want {
		t.Fatalf("with no ref:\n%s\nwant\n%s", got, want)
	}
	n.Ref = "decision:dr-3"
	got, err = json.Marshal(n)
	if err != nil {
		t.Fatal(err)
	}
	if want := strings.TrimSuffix(want, "}") + `,"ref":"decision:dr-3"}`; string(got) != want {
		t.Fatalf("with a ref:\n%s\nwant\n%s", got, want)
	}
}
