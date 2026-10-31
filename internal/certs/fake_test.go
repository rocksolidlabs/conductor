package certs

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"sync"
	"testing"
	"time"
)

// fakeCA signs certificates for the tests.
type fakeCA struct {
	key  *ecdsa.PrivateKey
	cert *x509.Certificate
	pem  []byte
}

func newFakeCA(t *testing.T) *fakeCA {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "fake ca"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(10 * 365 * 24 * time.Hour), IsCA: true, KeyUsage: x509.KeyUsageCertSign, BasicConstraintsValid: true}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := x509.ParseCertificate(der)
	return &fakeCA{key: key, cert: cert, pem: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})}
}

// issue signs a certificate for ids valid from notBefore for lifetime.
func (ca *fakeCA) issue(t *testing.T, ids []string, notBefore time.Time, lifetime time.Duration) *Issued {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	serial, _ := rand.Int(rand.Reader, big.NewInt(1<<62))
	tmpl := &x509.Certificate{SerialNumber: serial, NotBefore: notBefore, NotAfter: notBefore.Add(lifetime), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	for _, id := range ids {
		if ip := net.ParseIP(id); ip != nil {
			tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
		} else {
			tmpl.DNSNames = append(tmpl.DNSNames, id)
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, _ := x509.MarshalECPrivateKey(key)
	return &Issued{
		CertPEM:   append(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), ca.pem...),
		KeyPEM:    pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}),
		IssuerPEM: ca.pem,
	}
}

// fakeIssuer issues from fakeCA, presents a challenge on the way when told
// to, fails when told to, holds an order at a gate when given one, and
// counts.
type fakeIssuer struct {
	t        *testing.T
	ca       *fakeCA
	m        *Manager
	now      func() time.Time
	lifetime time.Duration

	mu       sync.Mutex
	issued   int
	fail     error
	alpnFor  string // present an ALPN challenge for this identifier during the order
	window   *[2]time.Time
	lastIDs  []string
	observed func()        // runs mid-order, after the challenge is presented
	hold     chan struct{} // an order waits here until it is closed (or its context ends)
}

func (f *fakeIssuer) Issue(ctx context.Context, ids []string, _ string) (*Issued, error) {
	f.mu.Lock()
	fail, alpnFor, observed, hold := f.fail, f.alpnFor, f.observed, f.hold
	f.lastIDs = append([]string(nil), ids...)
	f.mu.Unlock()
	if hold != nil {
		select {
		case <-hold:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if fail != nil {
		return nil, fail
	}
	if alpnFor != "" && f.m != nil {
		cert := f.ca.issue(f.t, []string{alpnFor}, f.now(), time.Hour)
		c, _ := tls.X509KeyPair(cert.CertPEM, cert.KeyPEM)
		f.m.presentALPN(alpnFor, &c)
		if observed != nil {
			observed()
		}
		f.m.cleanALPN(alpnFor)
	}
	f.mu.Lock()
	f.issued++
	f.mu.Unlock()
	return f.ca.issue(f.t, ids, f.now().Add(-time.Minute), f.lifetime), nil
}

func (f *fakeIssuer) RenewalWindow(context.Context, *x509.Certificate) (time.Time, time.Time, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.window == nil {
		return time.Time{}, time.Time{}, false
	}
	return f.window[0], f.window[1], true
}

func (f *fakeIssuer) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.issued
}

// ordering is the identifiers of the order begun last; an order held at
// the gate has begun.
func (f *fakeIssuer) ordering() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastIDs
}

func (f *fakeIssuer) set(fn func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn()
}

// clock is a fake time the tests move.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

var errFakeOrder = errors.New("the CA said no")

// newManagerForTest wires a manager to a fake issuer on a fake clock.
func newManagerForTest(t *testing.T, o Options, lifetime time.Duration) (*Manager, *fakeIssuer, *clock) {
	t.Helper()
	ca := newFakeCA(t)
	cl := &clock{t: time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)}
	fi := &fakeIssuer{t: t, ca: ca, now: cl.now, lifetime: lifetime}
	o.Issuer = fi
	o.Now = cl.now
	if o.Dir == "" {
		o.Dir = t.TempDir()
	}
	if o.Backoff == 0 {
		o.Backoff, o.BackoffMax = time.Hour, 24*time.Hour
	}
	m, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	fi.m = m
	return m, fi, cl
}

// runManager runs m on a goroutine until the test ends.
func runManager(t *testing.T, m *Manager) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { m.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
}

// waitFor polls cond until it holds. Its bound is one only a hang reaches:
// what a test waits for is a step it drove (a poke, a gate opened, the
// fake clock advanced), not a moment on the wall clock (#281).
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(time.Minute)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("never: %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
