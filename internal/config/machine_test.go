package config

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"

	"github.com/rocksolidlabs/conductor/internal/machine"
	"github.com/rocksolidlabs/conductor/internal/session"
)

// The directory of this machine's id is refused to every session: a
// server's list and a host's both name machine.Dir() under a test home, and
// a session rooted at the home resolves neither the machine-id file nor the
// directory, while a file beside the directory resolves. With
// XDG_CONFIG_HOME moving it, the default under the home stays refused too.
func TestFileDenyRefusesMachineDir(t *testing.T) {
	home := realTempDir(t)
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("CONDUCTOR_DATA_DIR", "")
	t.Setenv("CONDUCTOR_CATALOG_PATH", "")
	if runtime.GOOS == "windows" {
		// The user's config directory is %AppData% there, never HOME's.
		t.Setenv("AppData", filepath.Join(home, "AppData", "Roaming"))
	}
	t.Chdir(realTempDir(t))
	dir, err := machine.Dir()
	if err != nil {
		t.Fatal(err)
	}
	if rel, err := filepath.Rel(home, dir); err != nil || !filepath.IsLocal(rel) {
		t.Fatalf("the machine's directory %s is not under the test home %s", dir, home)
	}
	if _, err := machine.ID(dir); err != nil {
		t.Fatal(err)
	}
	beside := filepath.Join(filepath.Dir(dir), "notes.txt")
	if err := os.WriteFile(beside, []byte("beside"), 0o600); err != nil {
		t.Fatal(err)
	}
	server := (&Config{DataDir: filepath.Join(home, "data")}).FileDenyFunc()()
	host, err := LocalFileDeny("")
	if err != nil {
		t.Fatal(err)
	}
	idRel, _ := filepath.Rel(home, filepath.Join(dir, machine.FileName))
	besideRel, _ := filepath.Rel(home, beside)
	if _, err := session.ResolvePath(home, idRel, nil); err != nil {
		t.Fatalf("the id without a list: %v", err)
	}
	for name, deny := range map[string][]string{"a server's": server, "a host's": host} {
		if !slices.Contains(deny, dir) {
			t.Errorf("%s list does not name %s: %q", name, dir, deny)
		}
		if p, err := session.ResolvePath(home, idRel, deny); err == nil {
			t.Errorf("%s session resolved the machine's id at %s", name, p)
		}
		if _, err := session.ResolvePath(home, filepath.Dir(idRel), deny); err == nil {
			t.Errorf("%s session resolved the machine's directory", name)
		}
		if _, err := session.ResolvePath(home, besideRel, deny); err != nil {
			t.Errorf("%s session refused a file beside the directory: %v", name, err)
		}
	}
	// A server started with XDG_CONFIG_HOME elsewhere still refuses the
	// default place, where a host started without it keeps its id.
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		return
	}
	t.Setenv("XDG_CONFIG_HOME", realTempDir(t))
	moved := (&Config{DataDir: filepath.Join(home, "data")}).FileDenyFunc()()
	if _, err := session.ResolvePath(home, idRel, moved); err == nil {
		t.Error("with XDG_CONFIG_HOME elsewhere, the default place's id resolved")
	}
	movedHost, err := LocalFileDeny("")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := session.ResolvePath(home, idRel, movedHost); err == nil {
		t.Error("a host's list, with XDG_CONFIG_HOME elsewhere, let the default place's id resolve")
	}
}
