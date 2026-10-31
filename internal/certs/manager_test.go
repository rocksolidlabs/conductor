package certs

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestRenewalIsAThirdOfTheLifetimeForShortLivedCertificates(t *testing.T) {
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	leaf := &x509.Certificate{NotBefore: now, NotAfter: now.Add(6 * 24 * time.Hour)}
	if at := renewAt(leaf, now); !at.Equal(now.Add(4 * 24 * time.Hour)) {
		t.Fatalf("six days: renew at %s", at)
	}
	leaf = &x509.Certificate{NotBefore: now, NotAfter: now.Add(90 * 24 * time.Hour)}
	if at := renewAt(leaf, now); !at.Equal(now.Add(60 * 24 * time.Hour)) {
		t.Fatalf("ninety days: renew at %s", at)
	}
}

func TestRenewalIsThirtyDaysBeforeExpiryForLongCertificates(t *testing.T) {
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	leaf := &x509.Certificate{NotBefore: now, NotAfter: now.Add(365 * 24 * time.Hour)}
	if at := renewAt(leaf, now); !at.Equal(now.Add(335 * 24 * time.Hour)) {
		t.Fatalf("a year: renew at %s", at)
	}
	if at := renewAt(leaf, now.Add(360*24*time.Hour)); !at.Equal(now.Add(360 * 24 * time.Hour)) {
		t.Fatalf("past the renewal time: renew now, got %s", at)
	}
}

func TestManagerIssuesOnceAndRenewsAtTheRenewalTime(t *testing.T) {
	m, fi, cl := newManagerForTest(t, Options{Identifiers: []string{"203.0.113.9"}, Profile: "shortlived"}, 6*24*time.Hour)
	if m.Ready() {
		t.Fatal("ready before any issuance")
	}
	runManager(t, m)
	waitFor(t, "the first issuance", m.Ready)
	st := m.Status()
	if fi.count() != 1 || !st.Ready || st.Mode != ModeACME || st.Identifiers[0] != "203.0.113.9" || st.LastError != "" {
		t.Fatalf("status %+v issued=%d", st, fi.count())
	}
	if want := cl.now().Add(-time.Minute).Add(4 * 24 * time.Hour); !st.RenewAt.Equal(want) {
		t.Fatalf("renewAt %s, want %s", st.RenewAt, want)
	}
	first := m.Status().NotAfter
	cl.advance(3 * 24 * time.Hour)
	m.poke()
	time.Sleep(50 * time.Millisecond)
	if fi.count() != 1 {
		t.Fatal("renewed before the renewal time")
	}
	cl.advance(1*24*time.Hour + time.Minute)
	m.poke()
	waitFor(t, "the renewal", func() bool { return fi.count() == 2 })
	waitFor(t, "the new certificate", func() bool { return m.Status().NotAfter.After(first) })
	if _, ok := m.store.load([]string{"203.0.113.9"}); !ok {
		t.Fatal("the certificate was not kept")
	}
}

func TestManagerReissuesWhenTheIdentifierChanges(t *testing.T) {
	m, fi, _ := newManagerForTest(t, Options{}, 6*24*time.Hour)
	runManager(t, m)
	time.Sleep(50 * time.Millisecond)
	if fi.count() != 0 || m.Ready() {
		t.Fatal("ordered without identifiers")
	}
	m.SetIdentifiers([]string{"203.0.113.9"})
	waitFor(t, "the first issuance", m.Ready)
	// The new address's order is held while the test looks: ungated, it
	// can finish first on a loaded machine, and the new certificate it
	// serves fails the look as if the old one were kept (#281).
	hold := make(chan struct{})
	fi.set(func() { fi.hold = hold })
	m.SetIdentifiers([]string{"198.51.100.4"})
	if m.Ready() {
		t.Fatal("the old address's certificate is still served for the new one")
	}
	close(hold)
	waitFor(t, "the second issuance", func() bool { return fi.count() == 2 && m.Ready() })
	if ids := m.Status().Identifiers; len(ids) != 1 || ids[0] != "198.51.100.4" {
		t.Fatalf("identifiers %v", ids)
	}
	// Back to the first address: its certificate is kept and served at once.
	m.SetIdentifiers([]string{"203.0.113.9"})
	if !m.Ready() || fi.count() != 2 {
		t.Fatalf("the kept certificate was not served: ready=%v issued=%d", m.Ready(), fi.count())
	}
	m.SetIdentifiers(nil)
	if m.Ready() {
		t.Fatal("still ready with no identifiers")
	}
}

// An order for the old address that completes after the address changed is
// never served for the new one (#443). It is kept for the old address, and
// the new address's order follows at once, not at the old certificate's
// renewal time. The old address's order is held at a gate while the address
// changes, and the new address's while the test looks.
func TestManagerDoesNotServeAnOrderForTheOldAddress(t *testing.T) {
	m, fi, _ := newManagerForTest(t, Options{Identifiers: []string{"203.0.113.9"}}, 6*24*time.Hour)
	holdOld, holdNew := make(chan struct{}), make(chan struct{})
	fi.set(func() { fi.hold = holdOld })
	runManager(t, m)
	waitFor(t, "the old address's order", func() bool { return slices.Equal(fi.ordering(), []string{"203.0.113.9"}) })
	fi.set(func() { fi.hold = holdNew })
	m.SetIdentifiers([]string{"198.51.100.4"})
	close(holdOld)
	waitFor(t, "the new address's order", func() bool {
		if c, err := m.GetCertificate(&tls.ClientHelloInfo{ServerName: "198.51.100.4"}); err == nil {
			t.Fatalf("a certificate for %v is served for the new address", c.Leaf.IPAddresses)
		}
		return slices.Equal(fi.ordering(), []string{"198.51.100.4"})
	})
	if st := m.Status(); st.Ready || !slices.Equal(st.Identifiers, []string{"198.51.100.4"}) || st.LastError != "" || !st.NextTry.IsZero() || fi.count() != 1 {
		t.Fatalf("while the new address's order runs: status %+v issued=%d", st, fi.count())
	}
	close(holdNew)
	waitFor(t, "the new address's certificate", m.Ready)
	cur, err := m.GetCertificate(&tls.ClientHelloInfo{ServerName: "198.51.100.4"})
	if err != nil || len(cur.Leaf.IPAddresses) != 1 || cur.Leaf.IPAddresses[0].String() != "198.51.100.4" {
		t.Fatalf("served for the new address: %v %v", err, cur)
	}
	// Back to the old address: the certificate its order brought was kept,
	// and is served at once.
	m.SetIdentifiers([]string{"203.0.113.9"})
	cur, err = m.GetCertificate(&tls.ClientHelloInfo{ServerName: "203.0.113.9"})
	if err != nil || len(cur.Leaf.IPAddresses) != 1 || cur.Leaf.IPAddresses[0].String() != "203.0.113.9" || fi.count() != 2 {
		t.Fatalf("the old address's certificate was not kept: %v issued=%d", err, fi.count())
	}
}

// A failed order for the old address that ends after the address changed
// leaves the new address's failures, backoff and error alone (#443). The new
// address's order follows at once, and its own first failure backs off by
// the first step, an hour, not the second.
func TestManagerDoesNotCountAFailedOrderForTheOldAddress(t *testing.T) {
	m, fi, cl := newManagerForTest(t, Options{Identifiers: []string{"203.0.113.9"}, Backoff: time.Hour, BackoffMax: 4 * time.Hour}, 6*24*time.Hour)
	holdOld, holdNew := make(chan struct{}), make(chan struct{})
	fi.set(func() { fi.hold, fi.fail = holdOld, errFakeOrder })
	runManager(t, m)
	waitFor(t, "the old address's order", func() bool { return slices.Equal(fi.ordering(), []string{"203.0.113.9"}) })
	fi.set(func() { fi.hold = holdNew })
	m.SetIdentifiers([]string{"198.51.100.4"})
	close(holdOld)
	waitFor(t, "the new address's order", func() bool {
		if st := m.Status(); st.LastError != "" || !st.NextTry.IsZero() {
			t.Fatalf("the old address's failed order was counted against the new one: %+v", st)
		}
		return slices.Equal(fi.ordering(), []string{"198.51.100.4"})
	})
	close(holdNew)
	waitFor(t, "the new address's own failure", func() bool { return m.Status().LastError != "" })
	if st := m.Status(); st.Ready || !strings.Contains(st.LastError, "the CA said no") || !st.NextTry.Equal(cl.now().Add(time.Hour)) {
		t.Fatalf("the new address's first failure: %+v", st)
	}
}

func TestManagerBacksOffAfterAFailedOrder(t *testing.T) {
	m, fi, cl := newManagerForTest(t, Options{Identifiers: []string{"home.example.net"}, Backoff: time.Hour, BackoffMax: 4 * time.Hour}, 90*24*time.Hour)
	fi.set(func() { fi.fail = errFakeOrder })
	runManager(t, m)
	waitFor(t, "the failure", func() bool { return m.Status().LastError != "" })
	st := m.Status()
	if st.Ready || !strings.Contains(st.LastError, "the CA said no") || !st.NextTry.Equal(cl.now().Add(time.Hour)) {
		t.Fatalf("status %+v", st)
	}
	cl.advance(61 * time.Minute)
	m.poke()
	waitFor(t, "the second failure", func() bool { return m.Status().NextTry.After(cl.now().Add(time.Hour)) })
	if st := m.Status(); !st.NextTry.Equal(cl.now().Add(2 * time.Hour)) {
		t.Fatalf("second backoff: next try %s, now %s", st.NextTry, cl.now())
	}
	fi.set(func() { fi.fail = nil })
	cl.advance(3 * time.Hour)
	m.poke()
	waitFor(t, "the issuance", m.Ready)
	if st := m.Status(); st.LastError != "" || !st.NextTry.IsZero() {
		t.Fatalf("status after success %+v", st)
	}
}

func TestGetCertificateServesTheChallengeOnTheACMEALPN(t *testing.T) {
	m, fi, _ := newManagerForTest(t, Options{Identifiers: []string{"203.0.113.9"}}, 6*24*time.Hour)
	var seen *tls.Certificate
	var seenErr error
	fi.set(func() {
		fi.alpnFor = "203.0.113.9"
		fi.observed = func() {
			// A validator's hello: ALPN acme-tls/1, no SNI (an IP identifier), on the listener's address.
			conn := fakeConn{local: &net.TCPAddr{IP: net.ParseIP("203.0.113.9"), Port: 443}}
			seen, seenErr = m.GetCertificate(&tls.ClientHelloInfo{SupportedProtos: []string{acmeTLSProto}, Conn: conn})
			if _, err := m.GetCertificate(&tls.ClientHelloInfo{ServerName: "203.0.113.9", SupportedProtos: []string{acmeTLSProto}}); err != nil {
				t.Errorf("by SNI: %v", err)
			}
			if _, err := m.GetCertificate(&tls.ClientHelloInfo{ServerName: "other.example", SupportedProtos: []string{acmeTLSProto}}); err != nil {
				t.Errorf("a lone open challenge answers any name: %v", err)
			}
		}
	})
	runManager(t, m)
	waitFor(t, "the issuance", m.Ready)
	if seenErr != nil || seen == nil {
		t.Fatalf("challenge certificate: %v", seenErr)
	}
	leaf, _ := x509.ParseCertificate(seen.Certificate[0])
	if len(leaf.IPAddresses) != 1 || leaf.IPAddresses[0].String() != "203.0.113.9" {
		t.Fatalf("challenge certificate names %v", leaf.IPAddresses)
	}
	if _, err := m.GetCertificate(&tls.ClientHelloInfo{SupportedProtos: []string{acmeTLSProto}}); !errors.Is(err, errNoChallenge) {
		t.Fatalf("after the order: %v", err)
	}
	cur, err := m.GetCertificate(&tls.ClientHelloInfo{ServerName: "203.0.113.9", SupportedProtos: []string{"http/1.1"}})
	if err != nil || cur.Leaf == nil || cur.Leaf.IPAddresses[0].String() != "203.0.113.9" {
		t.Fatalf("the real certificate: %v %v", err, cur)
	}
}

type fakeConn struct {
	net.Conn
	local net.Addr
}

func (c fakeConn) LocalAddr() net.Addr { return c.local }

func TestGetCertificateServesNothingBeforeTheFirstIssuance(t *testing.T) {
	m, fi, _ := newManagerForTest(t, Options{Identifiers: []string{"203.0.113.9"}}, 6*24*time.Hour)
	fi.set(func() { fi.fail = errFakeOrder })
	runManager(t, m)
	waitFor(t, "the failure", func() bool { return m.Status().LastError != "" })
	if c, err := m.GetCertificate(&tls.ClientHelloInfo{ServerName: "203.0.113.9"}); !errors.Is(err, errNoCertificate) || c != nil {
		t.Fatalf("got %v, %v: nothing self-signed may be served", c, err)
	}
}

func TestManagerHonoursTheCAsRenewalWindow(t *testing.T) {
	m, fi, cl := newManagerForTest(t, Options{Identifiers: []string{"home.example.net"}}, 90*24*time.Hour)
	start := cl.now().Add(24 * time.Hour)
	fi.set(func() { fi.window = &[2]time.Time{start, start.Add(time.Hour)} })
	runManager(t, m)
	waitFor(t, "the issuance", m.Ready)
	if at := m.Status().RenewAt; at.Before(start) || at.After(start.Add(time.Hour)) {
		t.Fatalf("renewAt %s is outside the CA's window from %s", at, start)
	}
}

func TestStoreKeepsKeysPrivate(t *testing.T) {
	dir := t.TempDir()
	m, _, _ := newManagerForTest(t, Options{Dir: dir, Identifiers: []string{"203.0.113.9"}}, 6*24*time.Hour)
	runManager(t, m)
	waitFor(t, "the issuance", m.Ready)
	var keys []string
	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			keys = append(keys, path)
		}
		return nil
	})
	if len(keys) == 0 {
		t.Fatal("nothing stored")
	}
	for _, p := range keys {
		fi, _ := os.Stat(p)
		if fi.Mode().Perm() != 0o600 {
			t.Errorf("%s has mode %o", p, fi.Mode().Perm())
		}
	}
	if _, ok := m.store.load([]string{"203.0.113.9"}); !ok {
		t.Fatal("not loadable")
	}
	// A second manager over the same directory serves the kept certificate without an order.
	m2, fi2, _ := newManagerForTest(t, Options{Dir: dir, Identifiers: []string{"203.0.113.9"}}, 6*24*time.Hour)
	if !m2.Ready() || fi2.count() != 0 {
		t.Fatalf("the kept certificate was not served: ready=%v issued=%d", m2.Ready(), fi2.count())
	}
}

// filePair is a certificate pair on disk for the files mode, each file
// published with the mtime it is given so a check sees it as changed
// whatever the file system's timestamp granularity, and never sees the
// wall-clock mtime a write leaves before its Chtimes.
type filePair struct {
	t                 *testing.T
	ca                *fakeCA
	certPath, keyPath string
}

func newFilePair(t *testing.T) *filePair {
	dir := t.TempDir()
	return &filePair{t: t, ca: newFakeCA(t), certPath: filepath.Join(dir, "cert.pem"), keyPath: filepath.Join(dir, "key.pem")}
}

func (p *filePair) issue(id string) *Issued {
	return p.ca.issue(p.t, []string{id}, time.Now().Add(-time.Minute), time.Hour)
}

// put publishes one file with its mtime already set: written and stamped
// beside it, then renamed over it. A check that runs between a write and
// its Chtimes would otherwise see, and could record, the write's wall-clock
// mtime, which is later than the mtimes the test sets once setup has taken
// a few seconds, and no later check would see the test's changes (#281).
// It reports with t.Error, so the manager's goroutine can call it from the
// afterRead seam.
func (p *filePair) put(path string, data []byte, mtime time.Time) {
	tmp := path + ".new"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		p.t.Error(err)
	}
	if err := os.Chtimes(tmp, mtime, mtime); err != nil {
		p.t.Error(err)
	}
	if err := os.Rename(tmp, path); err != nil {
		p.t.Error(err)
	}
}

func (p *filePair) write(issued *Issued, mtime time.Time) {
	p.put(p.certPath, issued.CertPEM, mtime)
	p.put(p.keyPath, issued.KeyPEM, mtime)
}

func (p *filePair) manager() *Manager {
	p.t.Helper()
	// An hour between the manager's own checks: the test pokes it for each.
	m, err := New(Options{CertFile: p.certPath, KeyFile: p.keyPath, FileCheck: time.Hour})
	if err != nil {
		p.t.Fatal(err)
	}
	return m
}

func TestFilesReloadWhenTheyChange(t *testing.T) {
	p := newFilePair(t)
	at := time.Now()
	p.write(p.issue("one.example"), at)
	m := p.manager()
	if st := m.Status(); !st.Ready || st.Mode != ModeFiles || st.Identifiers[0] != "one.example" {
		t.Fatalf("status %+v", st)
	}
	runManager(t, m)
	// Each change is followed by a poke: a check starts after the change,
	// whether the manager was waiting or in a check of its own.
	at = at.Add(2 * time.Second)
	p.write(p.issue("two.example"), at)
	m.poke()
	waitFor(t, "the reload", func() bool { return m.Status().Identifiers[0] == "two.example" })
	// A broken pair keeps the one served and names the error.
	at = at.Add(2 * time.Second)
	p.put(p.keyPath, []byte("not a key"), at)
	m.poke()
	waitFor(t, "the error", func() bool { return m.Status().LastError != "" })
	if st := m.Status(); !st.Ready || st.Identifiers[0] != "two.example" {
		t.Fatalf("status %+v", st)
	}
	if _, err := New(Options{CertFile: p.certPath, KeyFile: p.keyPath}); err == nil {
		t.Fatal("a broken pair must refuse to start")
	}
}

// In production nothing pokes a files-mode manager: Run checks the pair
// again because step, which checks it, returns FileCheck and Run waits
// that long. Called here directly, so nothing waits on the clock.
func TestFilesCheckedEveryFileCheck(t *testing.T) {
	p := newFilePair(t)
	at := time.Now()
	p.write(p.issue("one.example"), at)
	for _, c := range []struct{ set, want time.Duration }{{0, time.Hour}, {7 * time.Minute, 7 * time.Minute}} {
		m, err := New(Options{CertFile: p.certPath, KeyFile: p.keyPath, FileCheck: c.set})
		if err != nil {
			t.Fatal(err)
		}
		at = at.Add(2 * time.Second)
		id := fmt.Sprintf("check-%d.example", c.want/time.Minute)
		p.write(p.issue(id), at)
		if got := m.step(context.Background()); got != c.want {
			t.Errorf("FileCheck %s: step waits %s before the next check, want %s", c.set, got, c.want)
		}
		if st := m.Status(); st.Identifiers[0] != id || st.LastError != "" {
			t.Errorf("FileCheck %s: step did not check the pair: %+v", c.set, st)
		}
	}
}

// A pair that changes while a check reads it is read again at the next
// check (#281). A pair that loads records the files' time taken before the
// read: a time taken after it is the change's, and no later check sees a
// change. A pair that fails records no time, so it is read again even when
// the write that completes it leaves the same mtime. The seam changes the
// files between the read and the record, once completing a pair that fails
// (a certificate written before its key, as a renewal tool writes them)
// and once replacing a pair that loads.
func TestFilesChangedWhileReadAreReadAgain(t *testing.T) {
	p := newFilePair(t)
	at := time.Now()
	p.write(p.issue("one.example"), at)
	m := p.manager()
	// The checks are called here, one at a time, not by Run: each runs to
	// its end before the test looks.
	var change func()
	m.afterRead = func() {
		if change != nil {
			change()
			change = nil
		}
	}
	check := func(wantErr bool) {
		t.Helper()
		if err := m.reloadFilesIfChanged(); (err != nil) != wantErr {
			t.Fatalf("check: %v", err)
		}
	}

	// The certificate is new and its key not yet: the read fails, and the
	// key arrives, in the certificate's timestamp tick, before the failure
	// is recorded.
	two := p.issue("two.example")
	at = at.Add(2 * time.Second)
	p.put(p.certPath, two.CertPEM, at)
	change = func() { p.put(p.keyPath, two.KeyPEM, at) }
	check(true)
	if st := m.Status(); st.Identifiers[0] != "one.example" || st.LastError == "" {
		t.Fatalf("the half-written pair: %+v", st)
	}
	check(false)
	if st := m.Status(); st.Identifiers[0] != "two.example" || st.LastError != "" {
		t.Fatalf("the whole pair was not read again: %+v", st)
	}

	// A pair that loads, replaced before what was read is recorded.
	three, four := p.issue("three.example"), p.issue("four.example")
	at = at.Add(2 * time.Second)
	p.write(three, at)
	fourAt := at.Add(2 * time.Second)
	change = func() { p.write(four, fourAt) }
	check(false)
	if st := m.Status(); st.Identifiers[0] != "three.example" {
		t.Fatalf("the pair that was read: %+v", st)
	}
	check(false)
	if st := m.Status(); st.Identifiers[0] != "four.example" {
		t.Fatalf("the pair that replaced it was not read again: %+v", st)
	}
	// Nothing changed since: the next check reads nothing.
	change = func() { t.Error("a check read an unchanged pair") }
	check(false)
}

func TestNewDNSProviderNeedsItsSettings(t *testing.T) {
	for name, env := range map[string]map[string]string{"cloudflare": {}, "exec": {}, "httpreq": {}, "route53": {"X": "1"}} {
		if _, err := newDNSProvider(name, env); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
	if _, err := newDNSProvider("exec", map[string]string{"EXEC_PATH": "/bin/true"}); err != nil {
		t.Fatal(err)
	}
	if _, err := newDNSProvider("httpreq", map[string]string{"HTTPREQ_ENDPOINT": "https://dns.example/api"}); err != nil {
		t.Fatal(err)
	}
	if _, err := newDNSProvider("cloudflare", map[string]string{"CLOUDFLARE_DNS_API_TOKEN": "tok"}); err != nil {
		t.Fatal(err)
	}
}
