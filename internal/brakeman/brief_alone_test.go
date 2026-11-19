package brakeman

import (
	"errors"
	"os"
	"testing"
)

// BriefAlone answers a kept brief without reading its resolutions: a
// refs document that does not read refuses Brief and leaves BriefAlone's
// answer as kept. A request with no brief, and one whose brief is gone,
// answer ErrNotFound.
func TestBriefAloneReadsNoResolutions(t *testing.T) {
	f := newFixture(t)
	r := f.keepBriefed("op-1", goodBrief(t))
	if err := os.WriteFile(f.reqPath(refsDoc(r.ID)), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.s.Brief(r.ID); err == nil {
		t.Fatal("Brief read a refs document that does not read")
	}
	b, err := f.s.BriefAlone(r.ID)
	if err != nil || b.Title != "Overnight" || len(b.Items) == 0 {
		t.Fatalf("%+v %v", b, err)
	}
	open := f.start("op-2")
	if _, err := f.s.BriefAlone(open.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("no brief: %v", err)
	}
	if err := os.Remove(f.reqPath(briefDoc(r.ID))); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.BriefAlone(r.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a brief gone: %v", err)
	}
}
