package signal

import (
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/proto"
	"github.com/rocksolidlabs/conductor/internal/session"
)

// A hosted session's touched index (#304) takes the file entries its host
// reports, the paths made absolute under the session's working directory
// as the host's OS writes them; entries of other types, or file entries
// with no op or path, touch nothing; the newest touch comes first.
func TestHostedSessionTouchedIndex(t *testing.T) {
	for _, c := range []struct{ cwd, path, want string }{
		{"/home/me/proj", "src/a.go", "/home/me/proj/src/a.go"},
		{"/home/me/proj/", "./src/../b.go", "/home/me/proj/b.go"},
		{"/home/me/proj", "/etc/hosts", "/etc/hosts"},
		{`C:\Users\me\proj`, `src\a.go`, `C:\Users\me\proj\src\a.go`},
		{`C:\Users\me\proj\`, "src/a.go", `C:\Users\me\proj\src\a.go`},
		{`C:\Users\me\proj`, `D:\other\b.go`, `D:\other\b.go`},
		{`C:\Users\me\proj`, `\\share\x\c.go`, `\\share\x\c.go`},
		{"/home/me/proj", `C:\elsewhere\d.go`, `C:\elsewhere\d.go`},
	} {
		if got := HostAbs(c.cwd, c.path); got != c.want {
			t.Errorf("HostAbs(%q, %q) = %q, want %q", c.cwd, c.path, got, c.want)
		}
	}

	hs := &HostedSession{info: session.Info{ID: "h1", Cwd: "/home/me/proj"}}
	at := time.Date(2026, 10, 22, 8, 0, 0, 0, time.UTC)
	stamp := func(d time.Duration) string { return at.Add(d).Format(time.RFC3339Nano) }
	hs.HostActivity(proto.Activity{Type: session.ActivityFile, Op: "read", Path: "README.md", Tool: "Read", ByName: "codex", At: stamp(0)}, "")
	hs.HostActivity(proto.Activity{Type: session.ActivityFile, Op: "edit", Path: "src/a.go", Tool: "Edit", ByName: "codex", At: stamp(time.Second)}, "")
	hs.HostActivity(proto.Activity{Type: session.ActivityFile, Op: "write", Path: "/home/me/proj/src/a.go", Tool: "Write", ByName: "codex", At: stamp(2 * time.Second)}, "")
	hs.HostActivity(proto.Activity{Type: session.ActivityFile, Op: "burn", Path: "src/b.go", At: stamp(3 * time.Second)}, "") // an unknown op is cleaned away
	hs.HostActivity(proto.Activity{Type: session.ActivityFile, Op: "edit", At: stamp(3 * time.Second)}, "")                   // no path
	hs.HostActivity(proto.Activity{Type: session.ActivityProgress, Message: "a step", At: stamp(4 * time.Second)}, "")
	got := hs.TouchedFiles()
	if len(got) != 2 || got[0].Path != "/home/me/proj/src/a.go" || got[1].Path != "/home/me/proj/README.md" {
		t.Fatalf("touched %+v", got)
	}
	if a := got[0]; a.Op != "write" || a.Count != 2 || a.Tool != "Write" || a.By != "codex" || len(a.Ops) != 2 || a.First != stamp(time.Second) || a.Last != stamp(2*time.Second) {
		t.Fatalf("a.go %+v", a)
	}
}
