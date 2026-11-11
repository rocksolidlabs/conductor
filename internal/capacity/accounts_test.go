package capacity

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/store"
)

func longFolder(n int) string { return "/" + strings.Repeat("f", n-1) }

func TestAccountsValidate(t *testing.T) {
	f := newFacts()
	f.refuse["/srv/not-mine"] = "a folder the server's user does not own"
	ok := Account{ID: "a-abcdefgh2345", Adapter: "claude", Label: "Work", Folder: "/home/u/work", CreatedAt: t0}
	if err := Validate(ok, f); err != nil {
		t.Fatalf("a good account: %v", err)
	}
	edge := []func(a *Account){
		func(a *Account) { a.Label = strings.Repeat("é", MaxLabel) }, // characters, not bytes
		func(a *Account) { a.Folder = longFolder(MaxFolder) },
		func(a *Account) { a.Adapter = "codex" },
	}
	for i, ch := range edge {
		a := ok
		ch(&a)
		if err := Validate(a, f); err != nil {
			t.Errorf("edge %d: %v", i, err)
		}
	}
	bad := []struct {
		field string
		ch    func(a *Account)
	}{
		{"id", func(a *Account) { a.ID = DefaultID }},
		{"id", func(a *Account) { a.ID = "a-ABCDEFGH2345" }},
		{"id", func(a *Account) { a.ID = "a-abcdefgh234" }},
		{"id", func(a *Account) { a.ID = "a-abcdefgh23451" }},
		{"id", func(a *Account) { a.ID = "work" }},
		{"adapter", func(a *Account) { a.Adapter = "nosuch" }},
		{"adapter", func(a *Account) { a.Adapter = "copilot" }}, // no ConfigDirEnv
		{"adapter", func(a *Account) { a.Adapter = "Claude Code" }},
		{"label", func(a *Account) { a.Label = "" }},
		{"label", func(a *Account) { a.Label = "   " }},
		{"label", func(a *Account) { a.Label = strings.Repeat("x", MaxLabel+1) }},
		{"label", func(a *Account) { a.Label = "tab\there" }},
		{"label", func(a *Account) { a.Label = "\xff" }},
		{"folder", func(a *Account) { a.Folder = "relative/dir" }},
		{"folder", func(a *Account) { a.Folder = "/home/u/../u/work" }},
		{"folder", func(a *Account) { a.Folder = "/home/u/work/" }},
		{"folder", func(a *Account) { a.Folder = longFolder(MaxFolder + 1) }},
		{"folder", func(a *Account) { a.Folder = "/home/u/new\nline" }},
		{"folder", func(a *Account) { a.Folder = "/srv/not-mine" }}, // Facts refuses it
		{"createdAt", func(a *Account) { a.CreatedAt = time.Time{} }},
	}
	for _, b := range bad {
		a := ok
		b.ch(&a)
		err := Validate(a, f)
		var e *Error
		if !errors.As(err, &e) || e.Code != CodeInvalidAccount || e.Field != b.field {
			t.Errorf("%+v: got %v, want invalid_account on %s", a, err, b.field)
		}
	}

	// Add holds what it is given to the same checks, and mints the id.
	s := openAt(t, t.TempDir(), Options{Now: clockAt(t0)})
	if _, _, err := s.Add(f, "copilot", "Work", "/home/u/work", 0); code(err) != CodeInvalidAccount {
		t.Errorf("an adapter without ConfigDirEnv added: %v", err)
	}
	if _, _, err := s.Add(f, "claude", "Work", "/srv/not-mine", 0); code(err) != CodeInvalidAccount || !strings.Contains(err.Error(), "does not own") {
		t.Errorf("a folder Facts refuses added: %v", err)
	}
	a, rev, err := s.Add(f, "claude", "Work", "/home/u/work", 0)
	if err != nil || rev != 1 || !idPattern.MatchString(a.ID) || !a.CreatedAt.Equal(t0) {
		t.Fatalf("Add: %+v, %d, %v", a, rev, err)
	}
}

func TestAccountsExpectedRev(t *testing.T) {
	f := newFacts()
	dir := t.TempDir()
	s := openAt(t, dir, Options{Now: clockAt(t0)})
	a, rev, err := s.Add(f, "claude", "Work", "/home/u/work", 0)
	if err != nil || rev != 1 {
		t.Fatalf("first add: %d, %v", rev, err)
	}
	path := filepath.Join(dir, Dir, AccountsFile)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	stale := map[string]func() error{
		"add":     func() error { _, _, err := s.Add(f, "codex", "Home", "/home/u/home", 0); return err },
		"rename":  func() error { _, _, err := s.Rename(a.ID, "Job", 0); return err },
		"retire":  func() error { _, _, err := s.Retire(a.ID, 2); return err },
		"remove":  func() error { _, err := s.Remove(f, a.ID, 0); return err },
		"default": func() error { _, _, err := s.SetDefault("claude", a.ID, 5); return err },
	}
	for name, w := range stale {
		if err := w(); code(err) != CodeStaleRev {
			t.Errorf("%s with a stale rev: %v", name, err)
		}
	}
	if after, _ := os.ReadFile(path); !bytes.Equal(before, after) {
		t.Errorf("a refused write changed the document:\n%s", after)
	}
	// Each write bumps the rev once and is on disk before it answers.
	if _, rev, err = s.Rename(a.ID, "Job", 1); err != nil || rev != 2 {
		t.Fatalf("rename: %d, %v", rev, err)
	}
	if _, rev, err = s.SetDefault("claude", a.ID, 2); err != nil || rev != 3 {
		t.Fatalf("default: %d, %v", rev, err)
	}
	reread := openAt(t, dir, Options{})
	d, err := reread.Accounts()
	if err != nil || d.Rev != 3 || d.Accounts[0].Label != "Job" || d.Defaults["claude"] != a.ID {
		t.Fatalf("read back: %+v, %v", d, err)
	}
}

func TestAccountFolderFixed(t *testing.T) {
	f := newFacts()
	dir := t.TempDir()
	s := openAt(t, dir, Options{Now: clockAt(t0)})
	a, rev, _ := s.Add(f, "claude", "Work", "/home/u/work", 0)
	r, rev, err := s.Rename(a.ID, "Renamed", rev)
	if err != nil || r.Folder != a.Folder {
		t.Fatalf("rename moved the folder: %+v, %v", r, err)
	}
	r, rev, err = s.Retire(a.ID, rev)
	if err != nil || r.Folder != a.Folder {
		t.Fatalf("retire moved the folder: %+v, %v", r, err)
	}
	// The folder an account holds is not added again for its adapter;
	// another folder is another account, under another id.
	if _, _, err := s.Add(f, "claude", "Again", "/home/u/work", rev); code(err) != CodeInvalidAccount {
		t.Errorf("a second account of one folder: %v", err)
	}
	b, rev, err := s.Add(f, "claude", "Other", "/home/u/other", rev)
	if err != nil || b.ID == a.ID || b.Folder != "/home/u/other" {
		t.Fatalf("another folder: %+v, %v", b, err)
	}
	// Another adapter may hold the same folder: it is that tool's login.
	if _, _, err := s.Add(f, "codex", "Work", "/home/u/work", rev); err != nil {
		t.Errorf("codex on claude's folder: %v", err)
	}
	// No write the Store offers names a folder but Add: the method set is
	// held here so a later one that does is seen.
	var names []string
	ty := reflect.TypeOf(s)
	for i := range ty.NumMethod() {
		names = append(names, ty.Method(i).Name)
	}
	want := []string{"Accounts", "Add", "Disown", "Own", "Remove", "Rename", "Reserve", "Retire", "SetDefault", "Versions"}
	if !slices.Equal(names, want) {
		t.Errorf("Store's methods are %v, want %v", names, want)
	}
}

func TestRetireKeepsRecord(t *testing.T) {
	f := newFacts()
	s := openAt(t, t.TempDir(), Options{Now: clockAt(t0)})
	a, rev, _ := s.Add(f, "claude", "Work", "/home/u/work", 0)
	if _, rev2, err := s.Retire(a.ID, rev); err != nil {
		t.Fatal(err)
	} else {
		rev = rev2
	}
	d, _ := s.Accounts()
	if len(d.Accounts) != 1 || !d.Accounts[0].Retired || d.Accounts[0].Folder != a.Folder || d.Accounts[0].Label != a.Label {
		t.Fatalf("retired record: %+v", d.Accounts)
	}
	// Out of every choice: no default, no launch's reservation.
	if _, _, err := s.SetDefault("claude", a.ID, rev); code(err) != CodeInvalidAccount {
		t.Errorf("a retired default: %v", err)
	}
	if _, _, err := s.Reserve(a.ID); code(err) != CodeInvalidAccount {
		t.Errorf("a retired account reserved: %v", err)
	}
	// Retiring twice keeps it retired.
	if r, _, err := s.Retire(a.ID, rev); err != nil || !r.Retired {
		t.Errorf("retire again: %+v, %v", r, err)
	}
}

func TestRemoveRefusedWhileBound(t *testing.T) {
	f := newFacts()
	dir := t.TempDir()
	s := openAt(t, dir, Options{Now: clockAt(t0)})
	a, rev, _ := s.Add(f, "claude", "Work", "/home/u/work", 0)
	for i := range 12 {
		f.bound[a.ID] = append(f.bound[a.ID], "session s-"+string(rune('a'+i)))
	}
	_, err := s.Remove(f, a.ID, rev)
	var e *Error
	if !errors.As(err, &e) || e.Code != CodeInUse || len(e.Names) != MaxBound || e.Names[0] != "session s-a" {
		t.Fatalf("remove while bound: %v", err)
	}
	if d, _ := s.Accounts(); len(d.Accounts) != 1 || d.Rev != rev || len(d.Removed) != 0 {
		t.Fatalf("a refused remove changed the document: %+v", d)
	}
	delete(f.bound, a.ID)
	if rev, err = s.Remove(f, a.ID, rev); err != nil {
		t.Fatal(err)
	}
	d, _ := openAt(t, dir, Options{}).Accounts()
	if len(d.Accounts) != 0 || !slices.Equal(d.Removed, []string{a.ID}) || d.Rev != rev {
		t.Fatalf("removed: %+v", d)
	}
	if _, err := s.Remove(f, a.ID, rev); code(err) != CodeInvalidAccount {
		t.Errorf("remove a removed account: %v", err)
	}
}

func TestDefaultsReadDefaultWhenRetired(t *testing.T) {
	f := newFacts()
	s := openAt(t, t.TempDir(), Options{Now: clockAt(t0)})
	d, _ := s.Accounts()
	if DefaultOf(d, "claude") != DefaultID {
		t.Fatal("no default set: not default")
	}
	a, rev, _ := s.Add(f, "claude", "Work", "/home/u/work", 0)
	b, rev, _ := s.Add(f, "codex", "Home", "/home/u/home", rev)
	if _, _, err := s.SetDefault("codex", a.ID, rev); code(err) != CodeInvalidAccount {
		t.Errorf("claude's account as codex's default: %v", err)
	}
	_, rev, _ = s.SetDefault("claude", a.ID, rev)
	defs, rev, _ := s.SetDefault("codex", b.ID, rev)
	if defs["claude"] != a.ID || defs["codex"] != b.ID {
		t.Fatalf("defaults: %v", defs)
	}
	_, rev, _ = s.Retire(a.ID, rev)
	_, _ = s.Remove(f, b.ID, rev)
	d, _ = s.Accounts()
	if DefaultOf(d, "claude") != DefaultID || DefaultOf(d, "codex") != DefaultID {
		t.Errorf("a retired and a removed default read as %q and %q", DefaultOf(d, "claude"), DefaultOf(d, "codex"))
	}
	// A document whose default names an account that is not there, or
	// one of another adapter, reads as default too.
	d.Defaults = map[string]string{"claude": "a-zzzzzzzzzzzz", "codex": a.ID}
	if DefaultOf(d, "claude") != DefaultID || DefaultOf(d, "codex") != DefaultID {
		t.Error("a dangling default is not default")
	}
	// "default" clears the adapter's default.
	d, _ = s.Accounts()
	defs, _, err := s.SetDefault("claude", DefaultID, d.Rev)
	if err != nil || len(defs) != 0 {
		t.Errorf("default cleared: %v, %v", defs, err)
	}
}

func TestAccountsDocumentBounded(t *testing.T) {
	f := newFacts()
	dir := t.TempDir()
	s := openAt(t, dir, Options{Now: clockAt(t0)})
	// Folders of the most bytes fill the document's bound before the
	// count: the add that would pass it is refused, and every document
	// written loads back.
	rev := int64(0)
	n := 0
	for ; ; n++ {
		folder := longFolder(MaxFolder-4) + "/" + string(rune('a'+n/26)) + string(rune('a'+n%26))
		_, r, err := s.Add(f, "claude", strings.Repeat("L", MaxLabel), folder, rev)
		if err != nil {
			if code(err) != CodeInvalidAccount || !strings.Contains(err.Error(), "bytes") {
				t.Fatalf("add %d: %v", n, err)
			}
			break
		}
		rev = r
	}
	if n == 0 || n >= MaxAccounts {
		t.Fatalf("%d accounts of the longest folders fit", n)
	}
	if fi, err := os.Stat(filepath.Join(dir, Dir, AccountsFile)); err != nil || fi.Size() > MaxAccountsDoc || fi.Mode().Perm() != 0o600 {
		t.Fatalf("the document: %v, %v", fi, err)
	}
	if fi, err := os.Stat(filepath.Join(dir, Dir)); err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("the folder: %v, %v", fi, err)
	}
	openAt(t, dir, Options{})

	// The count: at most MaxAccounts, retired ones included.
	s2 := openAt(t, t.TempDir(), Options{Now: clockAt(t0)})
	rev = 0
	for i := range MaxAccounts {
		a, r, err := s2.Add(f, "codex", "x", "/h/"+string(rune('a'+i/26))+string(rune('a'+i%26)), rev)
		if err != nil {
			t.Fatalf("add %d: %v", i, err)
		}
		if _, r, err = s2.Retire(a.ID, r); err != nil {
			t.Fatal(err)
		}
		rev = r
	}
	if _, _, err := s2.Add(f, "codex", "x", "/h/last", rev); code(err) != CodeInvalidAccount {
		t.Errorf("account %d added: %v", MaxAccounts+1, err)
	}

	// Read back bounded and strict: a document past the bound, one with a
	// field Accounts does not have (a token, say), one whose account
	// breaks its shape, and one that is not the document at all (null, {},
	// a list, a number, a rev below 1 or no accounts list: this package
	// writes none of them) each cannot be read at Open, and is left as it
	// is, never read as empty for the next write to replace.
	for name, body := range map[string][]byte{
		"null":          []byte(`null`),
		"empty object":  []byte(`{}`),
		"array":         []byte(`[]`),
		"number":        []byte(`7`),
		"rev 0":         []byte(`{"rev":0,"accounts":[]}`),
		"rev 0, null":   []byte(`{"rev":0,"accounts":null}`),
		"accounts null": []byte(`{"rev":1,"accounts":null}`),
		"no accounts":   []byte(`{"rev":1}`),
		"no rev":        []byte(`{"accounts":[]}`),
		"too large":     append([]byte(`{"rev":1,"accounts":[]}`), bytes.Repeat([]byte(" "), MaxAccountsDoc)...),
		"unknown field": []byte(`{"rev":1,"accounts":[{"id":"a-abcdefgh2345","adapter":"claude","label":"W","folder":"/w","createdAt":"2030-01-02T03:04:05Z","token":"invented"}]}`),
		"email":         []byte(`{"rev":1,"accounts":[],"email":"someone@example.invalid"}`),
		"bad id":        []byte(`{"rev":1,"accounts":[{"id":"default","adapter":"claude","label":"W","folder":"/w","createdAt":"2030-01-02T03:04:05Z"}]}`),
		"twice":         []byte(`{"rev":1,"accounts":[],"removed":["a-abcdefgh2345","a-abcdefgh2345"]}`),
	} {
		d := t.TempDir()
		if err := os.MkdirAll(filepath.Join(d, Dir), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, Dir, AccountsFile), body, 0o600); err != nil {
			t.Fatal(err)
		}
		data, _ := store.Open(d)
		if _, err := Open(data, Options{}); err == nil || !strings.Contains(err.Error(), "cannot be read") {
			t.Errorf("%s: opened: %v", name, err)
		} else if name == "too large" && !errors.Is(err, store.ErrTooLarge) {
			t.Errorf("%s: %v", name, err)
		}
		if b, _ := os.ReadFile(filepath.Join(d, Dir, AccountsFile)); !bytes.Equal(b, body) {
			t.Errorf("%s: the file changed", name)
		}
	}
	// A count past its bound in a document read back cannot be read either.
	var many []string
	for i := range MaxRemoved + 1 {
		many = append(many, `"a-`+strings.Repeat(string(base32[i%32]), 6)+strings.Repeat(string(base32[i/32]), 6)+`"`)
	}
	d := t.TempDir()
	if err := os.MkdirAll(filepath.Join(d, Dir), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d, Dir, AccountsFile), []byte(`{"rev":1,"accounts":[],"removed":[`+strings.Join(many, ",")+`]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	data, _ := store.Open(d)
	if _, err := Open(data, Options{}); err == nil || !strings.Contains(err.Error(), "removed ids") {
		t.Errorf("%d removed ids: %v", MaxRemoved+1, err)
	}
}

// TestNotOwnerWritesNothing: a Store opened without Own (a server without
// capacity/owner.lock), and one that owned and then Disowned (a server
// letting the lock go), reads the accounts and writes nothing: every write,
// every Reserve of a registered account and every Admit is refused
// accounts_not_owner in words, and nothing is made on disk. Own lets the
// same Store write.
// Folders reads the accounts document's folders from the file alone: none
// for no document; every account's, retired ones included, from the
// document the Store writes and from one a later version writes (a field
// this one does not know); an error for a file that is not JSON, past its
// bound, a link, or naming a folder that is not absolute.
func TestFolders(t *testing.T) {
	dir := t.TempDir()
	if got, err := Folders(dir); err != nil || got != nil {
		t.Fatalf("no document: %q, %v", got, err)
	}
	s := openAt(t, dir, Options{Now: clockAt(t0)})
	f := newFacts()
	a, rev, err := s.Add(f, "claude", "Work", "/home/u/work", 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, rev, err = s.Add(f, "codex", "Home", "/home/u/home", rev); err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.Retire(a.ID, rev); err != nil {
		t.Fatal(err)
	}
	want := []string{"/home/u/work", "/home/u/home"}
	if got, err := Folders(dir); err != nil || !slices.Equal(got, want) {
		t.Fatalf("the Store's document: %q, %v", got, err)
	}
	path := filepath.Join(dir, Dir, AccountsFile)
	write := func(b string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(b), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(`{"rev":9,"bindings":{"s":"a"},"accounts":[{"id":"a-x","folder":"/home/u/work","pinned":true}]}`)
	if got, err := Folders(dir); err != nil || !slices.Equal(got, want[:1]) {
		t.Fatalf("a later version's document: %q, %v", got, err)
	}
	for name, b := range map[string]string{"not JSON": "{oops", "a folder not absolute": `{"accounts":[{"folder":"rel/work"}]}`, "accounts not a list": `{"accounts":{}}`,
		"past its bound": `{"accounts":[],"x":"` + strings.Repeat("x", MaxAccountsDoc) + `"}`} {
		write(b)
		if got, err := Folders(dir); err == nil {
			t.Errorf("%s: %q", name, got)
		}
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, "elsewhere.json"), path); err != nil {
		t.Fatal(err)
	}
	if got, err := Folders(dir); err == nil {
		t.Errorf("a link: %q", got)
	}
}

func TestNotOwnerWritesNothing(t *testing.T) {
	f := newFacts()
	dir := t.TempDir()
	owner := openAt(t, dir, Options{Now: clockAt(t0)})
	a, rev, err := owner.Add(f, "claude", "Work", "/home/u/work", 0)
	if err != nil {
		t.Fatal(err)
	}
	owner.Disown()
	path := filepath.Join(dir, Dir, AccountsFile)
	before, _ := os.ReadFile(path)
	s := openNotOwned(t, dir, Options{Now: clockAt(t0)})
	if d, err := s.Accounts(); err != nil || d.Rev != rev || len(d.Accounts) != 1 {
		t.Fatalf("read without owning: %+v, %v", d, err)
	}
	for who, s := range map[string]*Store{"never owning": s, "disowned": owner} {
		writes := map[string]func() error{
			"add":     func() error { _, _, err := s.Add(f, "codex", "Home", "/home/u/home", rev); return err },
			"rename":  func() error { _, _, err := s.Rename(a.ID, "Job", rev); return err },
			"retire":  func() error { _, _, err := s.Retire(a.ID, rev); return err },
			"remove":  func() error { _, err := s.Remove(f, a.ID, rev); return err },
			"default": func() error { _, _, err := s.SetDefault("claude", a.ID, rev); return err },
			"reserve": func() error { _, _, err := s.Reserve(a.ID); return err },
			"admit":   func() error { _, err := s.Versions().Admit("claude", "2.1.0", false, true); return err },
		}
		for name, w := range writes {
			if err := w(); code(err) != CodeNotOwner || !strings.Contains(err.Error(), "does not own") {
				t.Errorf("%s, %s: %v", who, name, err)
			}
		}
	}
	if _, _, err := s.Reserve(DefaultID); err != nil {
		t.Errorf("the agent's own login without owning: %v", err)
	}
	if after, _ := os.ReadFile(path); !bytes.Equal(before, after) {
		t.Errorf("a server without the lock changed the document:\n%s", after)
	}
	if _, err := os.Stat(filepath.Join(dir, Dir, VersionsFile)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a record of versions made: %v", err)
	}
	if err := s.Own(); err != nil {
		t.Fatal(err)
	}
	if err := s.Own(); err != nil { // a second call does nothing
		t.Fatal(err)
	}
	if _, _, err := s.Rename(a.ID, "Job", rev); err != nil {
		t.Errorf("rename once owned: %v", err)
	}
}

// TestAccountKeepsNoCredential holds Account and Accounts to the fields
// of a reference: nothing else can be stored, since the document is
// decoded strictly and written from these types alone.
func TestAccountKeepsNoCredential(t *testing.T) {
	fields := func(v any) []string {
		var out []string
		ty := reflect.TypeOf(v)
		for i := range ty.NumField() {
			out = append(out, strings.Split(ty.Field(i).Tag.Get("json"), ",")[0])
		}
		return out
	}
	if got, want := fields(Account{}), []string{"id", "adapter", "label", "folder", "retired", "createdAt"}; !slices.Equal(got, want) {
		t.Errorf("Account's fields: %v, want %v", got, want)
	}
	if got, want := fields(Accounts{}), []string{"rev", "accounts", "defaults", "removed"}; !slices.Equal(got, want) {
		t.Errorf("Accounts' fields: %v, want %v", got, want)
	}
	a := Account{ID: "a-abcdefgh2345", Adapter: "claude", Label: "Work", Folder: "/w", CreatedAt: t0}
	b, _ := json.Marshal(a)
	if string(b) != `{"id":"a-abcdefgh2345","adapter":"claude","label":"Work","folder":"/w","createdAt":"2030-01-02T03:04:05Z"}` {
		t.Errorf("an account as JSON: %s", b)
	}
}

// seqReader hands out its bytes in order, so a test chooses the ids the
// mint draws.
type seqReader struct{ b []byte }

func (r *seqReader) Read(p []byte) (int, error) {
	n := copy(p, r.b)
	r.b = r.b[n:]
	if n == 0 {
		return 0, errors.New("no more bytes")
	}
	return n, nil
}

// draw is the 12 bytes the mint turns into id (a- and 12 base32 letters).
func draw(id string) []byte {
	var b []byte
	for _, c := range strings.TrimPrefix(id, "a-") {
		b = append(b, byte(strings.IndexRune(base32, c)))
	}
	return b
}

func TestAccountIDsNeverReissued(t *testing.T) {
	f := newFacts()
	first, second := "a-aaaaaaaaaaaa", "a-bbbbbbbbbbbb"
	var seq []byte
	seq = append(seq, draw(first)...)  // the first add
	seq = append(seq, draw(first)...)  // the second add draws it again: refused
	seq = append(seq, draw(second)...) // and takes the next draw
	dir := t.TempDir()
	s := openAt(t, dir, Options{Now: clockAt(t0), Rand: &seqReader{b: seq}})
	a, rev, err := s.Add(f, "claude", "a-cccccccccccc", "/home/u/work", 0)
	if err != nil || a.ID != first {
		t.Fatalf("first: %+v, %v", a, err)
	}
	if rev, err = s.Remove(f, a.ID, rev); err != nil {
		t.Fatal(err)
	}
	b, _, err := s.Add(f, "claude", "a-cccccccccccc", "/home/u/work", rev)
	if err != nil || b.ID != second {
		t.Fatalf("after the remove: %+v, %v", b, err)
	}
	d, _ := openAt(t, dir, Options{}).Accounts()
	if !slices.Equal(d.Removed, []string{first}) {
		t.Errorf("removed: %v", d.Removed)
	}
	// The label is never the id, whatever it looks like; ids drawn from
	// crypto/rand differ.
	s2 := openAt(t, t.TempDir(), Options{Now: clockAt(t0)})
	seen := map[string]bool{}
	rev = 0
	for i := range MaxAccounts {
		a, r, err := s2.Add(f, "codex", "a-cccccccccccc", "/h/"+string(rune('a'+i/26))+string(rune('a'+i%26)), rev)
		if err != nil || seen[a.ID] || a.ID == a.Label || !idPattern.MatchString(a.ID) {
			t.Fatalf("add %d: %+v, %v", i, a, err)
		}
		seen[a.ID], rev = true, r
	}
}

func TestReservationHoldsRemove(t *testing.T) {
	f := newFacts()
	s := openAt(t, t.TempDir(), Options{Now: clockAt(t0)})
	a, rev, _ := s.Add(f, "claude", "Work", "/home/u/work", 0)
	got, release, err := s.Reserve(a.ID)
	if err != nil || got != a {
		t.Fatalf("reserve: %+v, %v", got, err)
	}
	_, again, err := s.Reserve(a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Remove(f, a.ID, rev); code(err) != CodeInUse || !strings.Contains(err.Error(), "2 launch") {
		t.Fatalf("remove while reserved: %v", err)
	}
	release()
	release() // a second call does nothing
	if _, err := s.Remove(f, a.ID, rev); code(err) != CodeInUse {
		t.Fatalf("remove while one reservation is left: %v", err)
	}
	again()
	if _, err := s.Remove(f, a.ID, rev); err != nil {
		t.Fatalf("remove once released: %v", err)
	}
	if _, _, err := s.Reserve(a.ID); code(err) != CodeInvalidAccount {
		t.Errorf("a removed account reserved: %v", err)
	}
	// The agent's own login needs no reservation.
	if d, rel, err := s.Reserve(DefaultID); err != nil || d != (Account{}) || rel == nil {
		t.Errorf("default: %+v, %v", d, err)
	}
}
