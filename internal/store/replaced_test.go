package store_test

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/rocksolidlabs/conductor/internal/capacity"
	"github.com/rocksolidlabs/conductor/internal/config"
	"github.com/rocksolidlabs/conductor/internal/session"
	"github.com/rocksolidlabs/conductor/internal/store"
)

// A document replaced by a link between LoadLimit's look and its open is
// refused (ErrChanged), never read through the link. The accounts document
// so replaced is the deny list's error, as the Store's: every file is
// refused (the list holds the root) and the file beside is not served.
func TestADocumentReplacedAfterItsLookIsRefused(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	data := filepath.Join(home, "data")
	doc := filepath.Join(data, capacity.Dir, capacity.AccountsFile)
	other := filepath.Join(home, "other.json")
	write := func(path, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	folder := filepath.Join(home, "logins", "work")
	write(filepath.Join(folder, "auth.json"), "login\n")
	write(filepath.Join(home, "notes.md"), "# beside\n")
	write(other, `{"rev":1,"accounts":[]}`)
	replaced := func() {
		write(doc, `{"rev":1,"accounts":[{"id":"a-aaaaaaaaaaaa","adapter":"codex","label":"L","folder":"`+folder+`","createdAt":"2030-01-02T03:04:05Z"}]}`)
		store.SetBeforeOpen(t, func(path string) {
			if path != doc {
				return
			}
			store.SetBeforeOpen(t, nil)
			if err := os.Remove(doc); err != nil {
				t.Error(err)
			}
			if err := os.Symlink(other, doc); err != nil {
				t.Error(err)
			}
		})
	}
	replaced()
	if got, err := capacity.Folders(data); !errors.Is(err, store.ErrChanged) {
		t.Errorf("the folders of a document replaced: %q, %v", got, err)
	}
	replaced()
	list := (&config.Config{DataDir: data}).FileDeny()
	if !slices.Contains(list, string(filepath.Separator)) {
		t.Errorf("the deny list of a document replaced: %q, want the root among it", list)
	}
	for _, p := range []string{"notes.md", "logins/work/auth.json"} {
		if r, err := session.ResolvePath(home, p, list); err == nil {
			t.Errorf("%s resolved to %s, want it refused", p, r)
		}
	}
}
