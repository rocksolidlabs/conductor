package export_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/rocksolidlabs/conductor/internal/catalog"
	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
	"github.com/rocksolidlabs/conductor/internal/dispatch"
	"github.com/rocksolidlabs/conductor/internal/export"
	"github.com/rocksolidlabs/conductor/internal/store"
)

// A data directory with the catalog, a crew, the runs' records, a
// finished managed run, a Dispatch project with a ticket, a comment and
// two attachments, a switchyard's links and the hook assets, exported
// while its ledger and Dispatch are open, then restored into an empty
// directory: every file the same byte for byte, every row of the two
// databases the same, and the restored directory opened by the ledger
// and by Dispatch as their own, the ticket's comment and each
// attachment's bytes read back.
func TestRoundTrip(t *testing.T) {
	f := newFixture(t)
	put(t, f.dir, "catalog.json", `{"agents":[{"id":"mine","name":"Mine","command":["/bin/cat"],"allowArgs":false,"env":{"K":"v"}}]}`+"\n")
	put(t, f.dir, "crews/team.json", `{"id":"team","name":"Team"}`+"\n")
	put(t, f.dir, "links/0123456789abcdef.json", `{"hash":"ab"}`+"\n")
	put(t, f.dir, "hooks/claude-settings.json", `{"hooks":{}}`+"\n")
	const id = "team-00000001"
	f.run(t, id, ledger.RunRunning)
	f.finish(t, id)
	f.record(t, id)
	key, atts := f.ticket(t, [2]string{"a.txt", "alpha"}, [2]string{"b.txt", "beta"})

	b, m := archive(t, f.source())
	names, files := entries(t, b)
	if names[len(names)-1] != export.ManifestName {
		t.Fatalf("the manifest is not last: %v", names)
	}
	var onDisk export.Manifest
	if err := json.Unmarshal(files[export.ManifestName], &onDisk); err != nil {
		t.Fatal(err)
	}
	if onDisk.Format != export.Format || len(onDisk.Files) != len(names)-1 || len(m.Files) != len(onDisk.Files) {
		t.Fatalf("the manifest: %+v", onDisk)
	}
	for _, want := range []string{"catalog.json", "crews/team.json", "runs/" + id + ".json", export.LedgerPath, export.DispatchPath, "links/0123456789abcdef.json", "hooks/claude-settings.json",
		"dispatch/" + dispatch.AttachmentPath(atts[0].SHA256), "dispatch/" + dispatch.AttachmentPath(atts[1].SHA256)} {
		if _, ok := files[export.DataDir+want]; !ok {
			t.Fatalf("the archive lacks %s: %v", want, names)
		}
	}

	dst := filepath.Join(t.TempDir(), "restored")
	rm, err := restore(b, dst)
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if len(rm.Files) != len(m.Files) {
		t.Fatalf("restored %d files of %d", len(rm.Files), len(m.Files))
	}
	for _, file := range m.Files {
		got, err := os.ReadFile(filepath.Join(dst, filepath.FromSlash(file.Path)))
		if err != nil {
			t.Fatal(err)
		}
		if file.Path == export.LedgerPath || file.Path == export.DispatchPath {
			continue
		}
		want, err := os.ReadFile(filepath.Join(f.dir, filepath.FromSlash(file.Path)))
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != string(want) {
			t.Fatalf("%s differs", file.Path)
		}
		if runtime.GOOS != "windows" {
			if fi, err := os.Stat(filepath.Join(dst, filepath.FromSlash(file.Path))); err != nil || fi.Mode().Perm() != 0o600 {
				t.Fatalf("%s: mode %v %v", file.Path, fi.Mode(), err)
			}
		}
	}
	for _, db := range []string{export.LedgerPath, export.DispatchPath} {
		if !slices.Equal(dump(t, filepath.Join(dst, filepath.FromSlash(db))), dump(t, filepath.Join(f.dir, filepath.FromSlash(db)))) {
			t.Fatalf("%s: the rows differ", db)
		}
	}
	for _, left := range []string{export.LockFile, "runs/" + ledger.Lock} {
		if _, err := os.Lstat(filepath.Join(dst, filepath.FromSlash(left))); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s in the restored directory: %v", left, err)
		}
	}
	entriesOf, _ := os.ReadDir(dst)
	for _, e := range entriesOf {
		if strings.HasPrefix(e.Name(), ".restore-") {
			t.Fatalf("the staging directory %s was left", e.Name())
		}
	}

	// Opened as their own: the ledger writable, with the finished run; Dispatch
	// writable, with the ticket, its comment and each attachment's bytes.
	l, err := ledger.Open(filepath.Join(dst, "runs"))
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if ro, why := l.ReadOnly(); ro {
		t.Fatalf("the restored ledger is read-only: %s", why)
	}
	row, ok, err := l.Run(context.Background(), id)
	if err != nil || !ok || row.State != ledger.RunFinished || row.Revision != 2 {
		t.Fatalf("the restored run: %+v %v %v", row, ok, err)
	}
	ds, err := dispatch.Open(filepath.Join(dst, "dispatch"))
	if err != nil {
		t.Fatal(err)
	}
	defer ds.Close()
	if ro, why := ds.ReadOnly(); ro {
		t.Fatalf("the restored Dispatch is read-only: %s", why)
	}
	tk, err := ds.Ticket(context.Background(), key)
	if err != nil || tk.Title != "Export it" {
		t.Fatalf("the restored ticket: %+v %v", tk, err)
	}
	comments, err := ds.Comments(context.Background(), key, 0, 10)
	if err != nil || len(comments) != 1 || comments[0].Body != "and restore it" {
		t.Fatalf("the restored comments: %+v %v", comments, err)
	}
	for i, want := range []string{"alpha", "beta"} {
		_, file, err := ds.OpenAttachment(context.Background(), atts[i].ID)
		if err != nil {
			t.Fatalf("attachment %d: %v", i, err)
		}
		got, err := io.ReadAll(file)
		file.Close()
		if err != nil || string(got) != want {
			t.Fatalf("attachment %d: %q %v", i, got, err)
		}
	}
}

// What the export leaves out it lists with why: the workbench token and
// its lock, restore.lock, the ledger's owner lock, the databases' -wal
// and -shm, *.tmp, .probe-*, a .restore- directory, Dispatch's uploads'
// temporary directory, tls/, ext/, a link, and a name no archive holds;
// none is in the archive, which restores.
func TestExportLeavesOut(t *testing.T) {
	f := newFixture(t)
	f.ticket(t, [2]string{"a.txt", "alpha"})
	for rel, content := range map[string]string{
		"workbench-token":                "s3cret",
		"workbench-token.lock":           "",
		export.LockFile:                  "pid 1",
		"tls/account.key":                "key",
		"ext/provider/state.json":        "{}",
		"x.tmp":                          "tmp",
		"crews/team.json.123.tmp":        "tmp",
		".probe-123":                     "",
		".restore-abc/catalog.json":      "{}",
		"dispatch/attachments/tmp/up-1":  "part",
		"bad name.json":                  "{}",
		"crews/team.json":                `{"id":"team"}`,
		"dispatch/attachments/ff/orphan": "not named",
	} {
		put(t, f.dir, rel, content)
	}
	want := map[string]string{
		"workbench-token":           export.WhySecret,
		"workbench-token.lock":      export.WhyLock,
		export.LockFile:             export.WhyLock,
		"runs/" + ledger.Lock:       export.WhyLock,
		"tls/":                      export.WhySecret,
		"ext/":                      export.WhyProvider,
		"x.tmp":                     export.WhyTemporary,
		"crews/team.json.123.tmp":   export.WhyTemporary,
		".probe-123":                export.WhyTemporary,
		".restore-abc/":             export.WhyTemporary,
		"dispatch/attachments/tmp/": export.WhyTemporary,
		"bad name.json":             export.WhyName,
	}
	for _, db := range []string{export.LedgerPath, export.DispatchPath} {
		for _, suffix := range []string{"-wal", "-shm"} {
			if _, err := os.Lstat(filepath.Join(f.dir, filepath.FromSlash(db+suffix))); err == nil {
				want[db+suffix] = export.WhyTemporary
			}
		}
	}
	if runtime.GOOS != "windows" {
		if err := os.Symlink(filepath.Join(f.dir, "crews", "team.json"), filepath.Join(f.dir, "crews", "link.json")); err != nil {
			t.Fatal(err)
		}
		want["crews/link.json"] = export.WhyNotRegular
	}
	b, m := archive(t, f.source())
	left := map[string]string{}
	for _, l := range m.Left {
		left[l.Path] = l.Why
	}
	for p, why := range want {
		if left[p] != why {
			t.Errorf("%s: left %q, not %q", p, left[p], why)
		}
	}
	for p := range left {
		if _, ok := want[p]; !ok {
			t.Errorf("%s left out, unasked", p)
		}
	}
	names, _ := entries(t, b)
	for _, name := range names {
		rel := strings.TrimPrefix(name, export.DataDir)
		for p := range want {
			if rel == p || strings.HasPrefix(rel, p) && strings.HasSuffix(p, "/") {
				t.Errorf("the archive holds %s", name)
			}
		}
		if strings.HasSuffix(name, "/orphan") {
			t.Errorf("the archive holds an orphan no row names: %s", name)
		}
	}
	if !slices.Contains(names, export.DataDir+"crews/team.json") {
		t.Fatalf("the crew is not in the archive: %v", names)
	}
	if _, err := restore(b, filepath.Join(t.TempDir(), "restored")); err != nil {
		t.Fatalf("the archive does not restore: %v", err)
	}
}

// The records go in before the ledger's snapshot: a run that finishes and
// a run that starts between the two (through the seam) are a record that
// lags its ledger and a record missing, never one ahead; the restored
// server's start writes both from the ledger, so every managed run's
// record is its ledger's.
func TestExportRecordsBeforeTheLedger(t *testing.T) {
	f := newFixture(t)
	const done, started = "team-00000001", "team-00000002"
	f.run(t, done, ledger.RunRunning)
	export.SetAfterRecords(t, func() {
		f.finish(t, done)
		f.ledgerRun(t, started, ledger.RunRunning)
	})
	b, _ := archive(t, f.source())
	_, files := entries(t, b)
	var rec crew.Run
	if err := json.Unmarshal(files[export.DataDir+"runs/"+done+".json"], &rec); err != nil || rec.Managed == nil || rec.Managed.Revision != 1 {
		t.Fatalf("the archive's record of %s: %+v %v", done, rec.Managed, err)
	}
	if _, ok := files[export.DataDir+"runs/"+started+".json"]; ok {
		t.Fatalf("the archive holds a record of %s, made after the records' copy", started)
	}

	dst := filepath.Join(t.TempDir(), "restored")
	if _, err := restore(b, dst); err != nil {
		t.Fatal(err)
	}
	s := serve(t, dst)
	s.stop()
	st, err := store.Open(dst)
	if err != nil {
		t.Fatal(err)
	}
	records, err := crew.NewRecords(st)
	if err != nil {
		t.Fatal(err)
	}
	defer records.Close()
	l, err := ledger.Open(filepath.Join(dst, "runs"))
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	rows, err := l.Runs(context.Background())
	if err != nil || len(rows) != 2 {
		t.Fatalf("the restored ledger's runs: %d %v", len(rows), err)
	}
	for _, row := range rows {
		rec, ok, err := records.Get(row.ID)
		switch {
		case err != nil || !ok || rec.Managed == nil:
			t.Fatalf("%s: no record (%v %v)", row.ID, ok, err)
		case rec.Managed.Revision != row.Revision || rec.State != row.State:
			t.Fatalf("%s: the record at revision %d (%s), the ledger at %d (%s)", row.ID, rec.Managed.Revision, rec.State, row.Revision, row.State)
		}
	}
}

// keyed is a configured agent with an env value and an adapter.
func keyed(value string) catalog.Agent {
	return catalog.Agent{ID: "keyed", Name: "keyed", Command: []string{"/bin/cat"}, Adapter: "claude", Env: map[string]string{"TOKEN": value}}
}

// A saved agent's own env values are in the archive in full: one saved
// over nothing, and one saved over a configured agent with a value of its
// own; the masked ones of GET /api/catalog never stand in for them.
func TestExportKeepsCatalogEnv(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	ov := catalog.Overlay{Agents: []catalog.Agent{
		{ID: "mine", Name: "Mine", Command: []string{"/bin/cat"}, Env: map[string]string{"KEY": "mine-0123"}},
		{ID: "keyed", Name: "Keyed", Command: []string{"/bin/cat"}, Env: map[string]string{"TOKEN": "own-4567"}},
	}}
	b, err := store.Encode(ov)
	if err != nil {
		t.Fatal(err)
	}
	put(t, dir, "catalog.json", string(b))
	s := serve(t, dir, keyed("configured-89ab"))
	_, files := entries(t, s.export(t))
	var got catalog.Overlay
	if err := json.Unmarshal(files[export.DataDir+"catalog.json"], &got); err != nil {
		t.Fatal(err)
	}
	values := map[string]string{}
	for _, a := range got.Agents {
		for k, v := range a.Env {
			values[a.ID+"."+k] = v
		}
	}
	if values["mine.KEY"] != "mine-0123" || values["keyed.TOKEN"] != "own-4567" {
		t.Fatalf("the archive's env values: %v", values)
	}
	if strings.Contains(string(files[export.DataDir+"catalog.json"]), catalog.RedactedValue) {
		t.Fatal("a masked value in the archive's catalog")
	}
}

// A configured agent edited so that its saved override stores the mask
// for its env value and omits its adapter: exported through the server,
// the archive's catalog holds the configured value and adapter, so a
// directory restored from it and served with no config runs the agent as
// the source did; restored where the config holds the same agent and
// value, the start masks the value again, and the agent follows that
// config.
func TestExportResolvesInheritedEnv(t *testing.T) {
	const secret = "configured-0123456789"
	dir := filepath.Join(t.TempDir(), "data")
	ov := catalog.Overlay{Agents: []catalog.Agent{{ID: "keyed", Name: "Keyed", Command: []string{"/bin/cat"}, Env: map[string]string{"TOKEN": catalog.RedactedValue}}}}
	b, err := store.Encode(ov)
	if err != nil {
		t.Fatal(err)
	}
	put(t, dir, "catalog.json", string(b))
	s := serve(t, dir, keyed(secret))
	archived := s.export(t)
	if saved := overlayIn(t, dir); saved.Agents[0].Env["TOKEN"] != catalog.RedactedValue || saved.Agents[0].Adapter != "" {
		t.Fatalf("the source's file changed: %+v", saved.Agents[0])
	}
	s.stop()

	// No config: the agent as the source ran it.
	bare := filepath.Join(t.TempDir(), "bare")
	if _, err := restore(archived, bare); err != nil {
		t.Fatal(err)
	}
	serve(t, bare).stop()
	got := overlayIn(t, bare)
	if len(got.Agents) != 1 || got.Agents[0].Env["TOKEN"] != secret || got.Agents[0].Adapter != "claude" || got.Agents[0].Name != "Keyed" {
		t.Fatalf("restored with no config: %+v", got.Agents)
	}
	none, err := catalog.Load(catalog.File{DisableDefaults: true, Agents: []catalog.Agent{plain}})
	if err != nil {
		t.Fatal(err)
	}
	if err := none.ApplyOverlay(got); err != nil {
		t.Fatal(err)
	}
	if a, ok := none.Get("keyed"); !ok || a.Env["TOKEN"] != secret || a.Adapter != "claude" {
		t.Fatalf("the agent a server with no config runs: %+v %v", a, ok)
	}

	// The same config: masked again at start.
	same := filepath.Join(t.TempDir(), "same")
	if _, err := restore(archived, same); err != nil {
		t.Fatal(err)
	}
	serve(t, same, keyed(secret)).stop()
	if got := overlayIn(t, same); len(got.Agents) != 1 || got.Agents[0].Env["TOKEN"] != catalog.RedactedValue {
		t.Fatalf("restored where the config holds the value: %+v", got.Agents)
	}
}

// An attachment removed after Dispatch's snapshot and its file swept by a
// second store's open (through the seam): the export takes the snapshot
// again, which no longer names it, and succeeds; restored and opened,
// every live attachment opens with its bytes, the removed one is not
// found.
func TestExportAttachmentSweptAfterTheSnapshot(t *testing.T) {
	f := newFixture(t)
	key, atts := f.ticket(t, [2]string{"gone.txt", "removed after the snapshot"}, [2]string{"kept.txt", "kept"})
	gone, kept := atts[0], atts[1]
	var snapshots []int
	export.SetAfterDispatchSnapshot(t, func(n int) {
		snapshots = append(snapshots, n)
		if n != 1 {
			return
		}
		if rc, err := f.ds.RemoveAttachment(context.Background(), f.op(), key, gone.ID); err != nil || rc.Status != dispatch.StatusDone {
			t.Fatalf("remove: %+v %v", rc, err)
		}
		second, err := dispatch.Open(filepath.Join(f.dir, "dispatch"))
		if err != nil {
			t.Fatal(err)
		}
		second.Close()
		if _, err := os.Lstat(filepath.Join(f.dir, "dispatch", filepath.FromSlash(dispatch.AttachmentPath(gone.SHA256)))); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("the second store's sweep left the file: %v", err)
		}
	})
	b, _ := archive(t, f.source())
	if !slices.Equal(snapshots, []int{1, 2}) {
		t.Fatalf("the snapshots taken: %v", snapshots)
	}
	dst := filepath.Join(t.TempDir(), "restored")
	if _, err := restore(b, dst); err != nil {
		t.Fatal(err)
	}
	ds, err := dispatch.Open(filepath.Join(dst, "dispatch"))
	if err != nil {
		t.Fatal(err)
	}
	defer ds.Close()
	_, file, err := ds.OpenAttachment(context.Background(), kept.ID)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(file)
	file.Close()
	if string(got) != "kept" {
		t.Fatalf("the kept attachment: %q", got)
	}
	if _, _, err := ds.OpenAttachment(context.Background(), gone.ID); err == nil {
		t.Fatal("the removed attachment opens")
	}
}

// A live row's file deleted by hand: no snapshot stops naming it, and
// after three the export fails, naming its hash.
func TestExportAttachmentStillMissing(t *testing.T) {
	f := newFixture(t)
	_, atts := f.ticket(t, [2]string{"lost.txt", "lost by hand"})
	if err := os.Remove(filepath.Join(f.dir, "dispatch", filepath.FromSlash(dispatch.AttachmentPath(atts[0].SHA256)))); err != nil {
		t.Fatal(err)
	}
	n := 0
	export.SetAfterDispatchSnapshot(t, func(int) { n++ })
	_, err := export.Write(context.Background(), io.Discard, f.source())
	if err == nil || !strings.Contains(err.Error(), atts[0].SHA256) {
		t.Fatalf("the export: %v", err)
	}
	if n != 3 {
		t.Fatalf("%d snapshots, not 3", n)
	}
}
