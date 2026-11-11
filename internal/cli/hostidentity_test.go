package cli

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/internal/config"
	"github.com/rocksolidlabs/conductor/internal/export"
	"github.com/rocksolidlabs/conductor/internal/hostagent"
)

// conductor host and its identity provider (docs/plans/identity-uplink.md):
// started before the host registers, handed the --token, its directory
// under the host's state directory, marked so no export takes it.

// hostProvider is an identity provider that is a Starter and an
// UplinkIdentity: each Start hands its Host over (began), writes a file in
// its DataDir, and fails as fail says, with the token in its words.
type hostProvider struct {
	began chan ext.Host
	ctxs  chan context.Context
	fail  string // "", "error" or "panic"
	mark  chan error
	dirs  chan string
}

func (hostProvider) Name() string { return "cloud" }
func (hostProvider) Authenticate(context.Context, string) (ext.Principal, bool, error) {
	return ext.Principal{}, false, nil
}
func (hostProvider) OwnsHostToken(c string) bool               { return strings.HasPrefix(c, "code-") }
func (hostProvider) UplinkToken(string, string) (string, bool) { return "cred-uplink", true }
func (p hostProvider) Start(ctx context.Context, h ext.Host) error {
	dir, err := h.DataDir()
	if err == nil {
		_, merr := os.Stat(filepath.Join(filepath.Dir(dir), config.ProviderStateMark))
		p.mark <- merr
		err = os.WriteFile(filepath.Join(dir, "lease.json"), []byte(`{"lease":"x"}`), 0o600)
	}
	p.dirs <- dir
	p.began <- h
	p.ctxs <- ctx
	switch p.fail {
	case "error":
		return errors.New("cannot redeem " + h.HostToken())
	case "panic":
		panic("cannot redeem " + h.HostToken())
	}
	return err
}

func newHostProvider(fail string) hostProvider {
	return hostProvider{began: make(chan ext.Host, 8), ctxs: make(chan context.Context, 8), fail: fail, mark: make(chan error, 8), dirs: make(chan string, 8)}
}

func withHostProvider(t *testing.T, p hostProvider) {
	t.Helper()
	old := extensions
	t.Cleanup(func() { extensions = old; runHostAgent = hostagent.Run })
	extensions = func() ext.Providers {
		return ext.Providers{Identity: p, All: []ext.Entry{{Kind: ext.KindIdentity, Name: "cloud", Value: p}}}
	}
}

// TestHostStartsTheIdentityProvider: Start is called before the host
// registers, with a Host of host mode, the --token and a directory under
// the state directory, marked first; its context ends once the host has.
// Two hosts over one state directory both start; a failed Start's words,
// which hold the token, are never logged; a state directory that cannot
// be made starts nothing.
func TestHostStartsTheIdentityProvider(t *testing.T) {
	clearConductorEnv(t)
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	p := newHostProvider("")
	withHostProvider(t, p)
	var mu sync.Mutex
	both := make(chan struct{})
	n := 0
	runHostAgent = func(ctx context.Context, o hostagent.Options) (hostagent.Result, error) {
		if o.Credential == nil {
			t.Error("the host has no uplink credential")
			return hostagent.Result{}, nil
		}
		// What the first registration presents waits for the first try of
		// Start, which has then begun.
		if tok, _ := o.Credential.Present(ctx); tok != "cred-uplink" {
			t.Errorf("the first registration presents %q", tok)
		}
		mu.Lock()
		if n++; n == 2 {
			close(both)
		}
		mu.Unlock()
		<-both // two hosts at once over one state directory
		return hostagent.Result{}, nil
	}
	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() {
			if code, stderr, err := runHostWith(t, "--server", "http://127.0.0.1:1", "--token", "code-xyz", "--no-local", "--", "sh"); code != 0 || err != nil {
				t.Errorf("exit %d, %v, %s", code, err, stderr)
			}
		})
	}
	wg.Wait()
	for range 2 {
		h, ctx := <-p.began, <-p.ctxs
		if err := <-p.mark; err != nil {
			t.Errorf("the mark was not there before Start: %v", err)
		}
		dir := <-p.dirs
		if h.Mode() != ext.ModeHost || dir != filepath.Join(state, "conductor", "ext", "cloud") {
			t.Errorf("Host: mode %q, DataDir %q", h.Mode(), dir)
		}
		<-ctx.Done() // ended with its host
		if h.HostToken() != "" {
			t.Error("a Host whose try ended still hands the token over")
		}
	}

	for _, fail := range []string{"error", "panic"} {
		p := newHostProvider(fail)
		withHostProvider(t, p)
		runHostAgent = func(context.Context, hostagent.Options) (hostagent.Result, error) {
			<-p.began
			<-p.began // the second try: the first was logged
			return hostagent.Result{}, nil
		}
		_, stderr, _ := runHostWith(t, "--server", "http://127.0.0.1:1", "--token", "code-secret-9", "--no-local", "--", "sh")
		if strings.Contains(stderr, "code-secret-9") || !strings.Contains(stderr, "failure="+fail) {
			t.Fatalf("%s: the host's log:\n%s", fail, stderr)
		}
	}

	notDir := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(notDir, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_STATE_HOME", notDir)
	q := newHostProvider("")
	withHostProvider(t, q)
	runHostAgent = func(context.Context, hostagent.Options) (hostagent.Result, error) { return hostagent.Result{}, nil }
	runHostWith(t, "--server", "http://127.0.0.1:1", "--token", "code-xyz", "--no-local", "--", "sh")
	if len(q.began) != 0 {
		t.Fatal("a provider started without its marked state directory")
	}
}

// TestHostRegistersWithTheUplinkToken: the registration carries what the
// provider gives for the switchyard, never the configured code.
func TestHostRegistersWithTheUplinkToken(t *testing.T) {
	clearConductorEnv(t)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	withHostProvider(t, newHostProvider(""))
	seen := make(chan string, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- r.Header.Get("Authorization")
		http.Error(w, `{"error":{"code":"unauthorized","message":"no"}}`, http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)
	runHostWith(t, "--server", srv.URL, "--token", "code-xyz", "--no-local", "--", "sh")
	if got := <-seen; got != "Bearer cred-uplink" {
		t.Fatalf("the registration presented %q", got)
	}
}

// TestHostProviderStateNotExported: a state directory inside a data
// directory (XDG_STATE_HOME set to it) is no part of its export.
func TestHostProviderStateNotExported(t *testing.T) {
	clearConductorEnv(t)
	data := t.TempDir()
	t.Setenv("XDG_STATE_HOME", data)
	p := newHostProvider("")
	withHostProvider(t, p)
	runHostAgent = func(context.Context, hostagent.Options) (hostagent.Result, error) {
		<-p.began
		return hostagent.Result{}, nil
	}
	runHostWith(t, "--server", "http://127.0.0.1:1", "--token", "code-xyz", "--no-local", "--", "sh")
	if _, err := os.Stat(filepath.Join(data, "conductor", "ext", "cloud", "lease.json")); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	// The directory holds no database: nothing is snapshotted.
	none := func(context.Context, string, string) error { return errors.New("no database here") }
	m, err := export.Write(t.Context(), &buf, export.Source{Dir: data, Snapshot: none})
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range m.Files {
		if strings.HasPrefix(f.Path, "conductor/ext") {
			t.Fatalf("the archive holds %s", f.Path)
		}
	}
	left := false
	for _, l := range m.Left {
		left = left || (l.Path == "conductor/ext/" && l.Why == export.WhyProvider)
	}
	if !left || bytes.Contains(buf.Bytes(), []byte("lease")) {
		t.Fatalf("the provider's state is not left out: %+v", m.Left)
	}
}
