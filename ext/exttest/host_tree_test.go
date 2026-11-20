package exttest

import (
	"context"
	"errors"
	"io/fs"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/fstest"

	"github.com/rocksolidlabs/conductor/ext"
)

var (
	treeCommit  = strings.Repeat("ab", 20)
	strayCommit = strings.Repeat("cd", 20)
)

// sampleTree is a small committed tree: two files, an executable, a link
// and a secret the tests deny.
func sampleTree() fstest.MapFS {
	return fstest.MapFS{
		"README":          {Data: []byte("hello\n")},
		"cmd/main.go":     {Data: []byte("package main\n")},
		"run.sh":          {Data: []byte("#!/bin/sh\n"), Mode: 0o755},
		"docs/latest":     {Data: []byte("../README"), Mode: fs.ModeSymlink | 0o777},
		"config/.env":     {Data: []byte("TOKEN=x\n")},
		"assets/huge.bin": {Data: make([]byte, ext.MaxBlob+1)},
	}
}

// The fake's reads answer as the server's Host does: the tree listed with
// denied paths counted and left out, a link's target, refusals by
// ext.ErrRefused and absences by ext.ErrNotFound; and every read recorded.
func TestFakeTreeReads(t *testing.T) {
	h := NewFakeHost(t)
	ctx := context.Background()
	run := "api-0a0b0c0d"
	h.SetTree(run, treeCommit, sampleTree())
	h.SetDenied(func(p string) bool { return strings.HasPrefix(p, "config/") })
	tr, err := h.ReadTree(ctx, ext.TreeRequest{Run: run, Commit: treeCommit, Roots: true})
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]string{}
	for _, e := range tr.Entries {
		kinds[e.Path] = e.Kind + " " + e.Mode
	}
	if tr.Denied != 1 || len(tr.Entries) != 5 || kinds["docs/latest"] != "link 120000" || kinds["run.sh"] != "file 100755" || kinds["README"] != "file 100644" || len(tr.Roots) != 1 {
		t.Fatalf("the tree: %+v", tr)
	}
	if b, err := h.ReadBlob(ctx, ext.BlobRequest{Run: run, Commit: treeCommit, Path: "docs/latest"}); err != nil || string(b) != "../README" {
		t.Fatalf("the link: %q %v", b, err)
	}
	for _, c := range []struct {
		path string
		want error
	}{
		{"config/.env", ext.ErrRefused}, {"assets/huge.bin", ext.ErrRefused}, {"../x", ext.ErrRefused},
		{"missing", ext.ErrNotFound}, {"cmd", ext.ErrNotFound},
	} {
		if _, err := h.ReadBlob(ctx, ext.BlobRequest{Run: run, Commit: treeCommit, Path: c.path}); !errors.Is(err, c.want) {
			t.Errorf("%s: %v, want %v", c.path, err, c.want)
		}
	}
	if _, err := h.ReadTree(ctx, ext.TreeRequest{Run: run, Commit: strayCommit}); !errors.Is(err, ext.ErrNotFound) {
		t.Fatalf("another commit: %v", err)
	}
	if _, err := h.ReadTree(ctx, ext.TreeRequest{Run: "web-0a0b0c0d", Commit: treeCommit}); !errors.Is(err, ext.ErrNotFound) {
		t.Fatalf("another run: %v", err)
	}
	if _, err := h.ReadTree(ctx, ext.TreeRequest{Run: run, Commit: "HEAD"}); !errors.Is(err, ext.ErrRefused) {
		t.Fatalf("a ref: %v", err)
	}
	if got, err := h.Denied(ctx, ext.DeniedRequest{Run: run, Paths: []string{"config/.env", "README"}}); err != nil || !got[0] || got[1] {
		t.Fatalf("denied: %v %v", got, err)
	}
	h.SetDenied(func(p string) bool { return p == "README" })
	if got, _ := h.Denied(ctx, ext.DeniedRequest{Run: run, Paths: []string{"config/.env", "README"}}); got[0] || !got[1] {
		t.Fatalf("denied as the list changed: %v", got)
	}
	if n := len(h.Reads()); n != 12 {
		t.Fatalf("%d reads recorded", n)
	}
	h.Close()
	if _, err := h.ReadTree(ctx, ext.TreeRequest{Run: run, Commit: treeCommit}); !errors.Is(err, context.Canceled) {
		t.Fatalf("closed: %v", err)
	}
}

// treeReader is a context provider that reads its run's repository
// through its Host as Context is asked: the tree of the query's start,
// then each file listed, or (stray) also another commit's tree and the
// denied path.
type treeReader struct {
	*FakeContext
	stray bool
	mu    sync.Mutex
	h     ext.Host
}

func (p *treeReader) Start(_ context.Context, h ext.Host) error {
	p.mu.Lock()
	p.h = h
	p.mu.Unlock()
	return nil
}

func (p *treeReader) Context(ctx context.Context, q ext.ContextQuery) ([]ext.ContextBlock, error) {
	p.mu.Lock()
	h := p.h
	p.mu.Unlock()
	tr, err := h.ReadTree(ctx, ext.TreeRequest{Run: q.Run, Commit: q.Start})
	if err != nil {
		return nil, err
	}
	for _, e := range tr.Entries {
		_, _ = h.ReadBlob(ctx, ext.BlobRequest{Run: q.Run, Commit: q.Start, Path: e.Path})
	}
	if p.stray {
		_, _ = h.ReadTree(ctx, ext.TreeRequest{Run: q.Run, Commit: strayCommit})
		_, _ = h.ReadBlob(ctx, ext.BlobRequest{Run: q.Run, Commit: q.Start, Path: "config/.env"})
	}
	return []ext.ContextBlock{{Title: "Files", Body: "listed", Pack: "p-1"}}, nil
}

// The Tree check passes a provider whose reads stay in the query's tree,
// and names each read of one that strays: a commit the query does not
// name, a path the deny list refuses.
func TestTreeCheck(t *testing.T) {
	q := SampleQuery(t.TempDir())
	q.Run, q.Start = "exttest-0a0b0c0d", treeCommit
	deny := func(p string) bool { return strings.HasPrefix(p, "config/") }
	n := NewNetwork()
	if errs := record(t, func(rt T) { Tree(rt, n, &treeReader{FakeContext: NewFakeContext("reader")}, q, sampleTree(), deny) }); len(errs) != 0 {
		t.Fatalf("a provider inside its tree: %v", errs)
	}
	errs := record(t, func(rt T) {
		Tree(rt, n, &treeReader{FakeContext: NewFakeContext("stray"), stray: true}, q, sampleTree(), deny)
	})
	if !said(errs, `exttest: Tree "stray": ReadTree read commit`, "which the query does not name") ||
		!said(errs, `exttest: Tree "stray": ReadBlob asked for "config/.env"`, "deny list refuses") {
		t.Fatalf("a stray provider: %v", errs)
	}
	if errs := record(t, func(rt T) { Tree(rt, n, NewFakeContext("no-host"), q, sampleTree(), deny) }); !said(errs, "is no ext.Starter") {
		t.Fatalf("a provider with no Host: %v", errs)
	}
}

// dialNoter is a context provider whose NoteCommit dials its service
// under the call's context, once.
type dialNoter struct {
	*FakeContext
	n     *Network
	addr  string
	dials atomic.Int32
}

func (p *dialNoter) NoteCommit(ctx context.Context, _ ext.CommitNote) {
	if p.dials.Add(1) > 1 {
		return
	}
	if conn, err := p.n.DialContext(ctx, "tcp", p.addr); err == nil {
		conn.Close()
	}
}

// The Context check calls NoteCommit of a provider that takes commit notes
// and finds one that reaches its service on the call's path; one that
// does not passes.
func TestContextCheckNotesCommits(t *testing.T) {
	n := NewNetwork()
	echo := echoServer(t)
	errs := record(t, func(rt T) {
		Context(rt, n, &dialNoter{FakeContext: NewFakeContext("noter"), n: n, addr: echo}, SampleQuery(t.TempDir()))
	})
	if !said(errs, `exttest: Context "noter": NoteCommit dialled tcp`, "article 8") {
		t.Fatalf("a dialling NoteCommit: %v", errs)
	}
	quiet := &dialNoter{FakeContext: NewFakeContext("quiet")}
	quiet.dials.Store(1)
	if errs := record(t, func(rt T) { Context(rt, n, quiet, SampleQuery(t.TempDir())) }); len(errs) != 0 {
		t.Fatalf("a quiet NoteCommit: %v", errs)
	}
	if quiet.dials.Load() < 2 {
		t.Fatalf("NoteCommit was not called: %d", quiet.dials.Load())
	}
}
