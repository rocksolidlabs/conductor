package journal

import (
	"bytes"
	"testing"
)

// A record whose launch names its login (an account's id and source) or
// its model is written at VLogin, so a server that knows only the
// versions before it reads it as a later version's, never strictly
// without them; a launch naming neither stays at V. Both read back
// strictly here.
func TestALaunchsLoginIsWrittenAtItsOwnVersion(t *testing.T) {
	dir := t.TempDir()
	j := openAt(t, dir, Options{})
	bound := serverRecord(sid(1))
	bound.Launch = LaunchOf(nil, nil, false, false)
	bound.Launch.Account, bound.Launch.Source, bound.Launch.Model = "a-aaaaaaaaaaaa", "account", "opus"
	begin(t, j, bound)
	plain := serverRecord(sid(2))
	plain.Launch = LaunchOf(nil, nil, false, false)
	begin(t, j, plain)
	j.Flush()
	if r, b := onDisk(t, dir, sid(1)); r.V != VLogin || r.Launch.Account != "a-aaaaaaaaaaaa" || r.Launch.Source != "account" || r.Launch.Model != "opus" {
		t.Fatalf("the bound launch's record, version %d:\n%s", r.V, b)
	}
	if r, b := onDisk(t, dir, sid(2)); r.V != V || bytes.Contains(b, []byte(`"source"`)) {
		t.Fatalf("a launch naming no login, version %d:\n%s", r.V, b)
	}
	for _, id := range []string{sid(1), sid(2)} {
		if _, newer, err := j.Get(id); err != nil || newer {
			t.Fatalf("%s read back: newer %v, %v", id, newer, err)
		}
	}
}
