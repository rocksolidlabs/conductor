// Package certs keeps a TLS listener's certificate: obtained from a CA
// through ACME (go-acme/lego) for the server's public address or its
// domains, stored in the data directory, renewed on its own, and the
// challenges answered on the listener itself (tls-alpn-01) or the plain
// one (http-01); or read from files of the person's own. Before the first
// issuance it serves nothing, so a handshake fails plainly rather than
// trusting a certificate nobody signed.
package certs

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"log/slog"
	"math/rand/v2"
	"net"
	"slices"
	"sync"
	"time"
)

// Modes of the manager (what Status reports).
const (
	ModeACME  = "acme"
	ModeFiles = "files"
)

// ACME challenges (config.ACME.Challenge).
const (
	ChallengeTLSALPN = "tls-alpn-01"
	ChallengeHTTP    = "http-01"
	ChallengeDNS     = "dns-01"
)

// acmeTLSProto is the ALPN protocol of RFC 8737.
const acmeTLSProto = "acme-tls/1"

// Status is the certificate part of GET /api/reach.
type Status struct {
	Mode string `json:"mode"`
	// Identifiers the certificate is for (or wanted); none while the public
	// address is not known yet.
	Identifiers []string `json:"identifiers,omitempty"`
	Challenge   string   `json:"challenge,omitempty"`
	// NotAfter and RenewAt of the certificate served; zero without one.
	NotAfter time.Time `json:"notAfter,omitzero"`
	RenewAt  time.Time `json:"renewAt,omitzero"`
	// Ready says a certificate is served.
	Ready bool `json:"ready"`
	// LastError is the last failed order or file read, in words.
	LastError string `json:"lastError,omitempty"`
	// NextTry is when the next order is attempted after a failure.
	NextTry time.Time `json:"nextTry,omitzero"`
}

// Issued is what an Issuer returns: PEM, the certificate with its chain.
type Issued struct {
	CertPEM   []byte
	KeyPEM    []byte
	IssuerPEM []byte
}

// Issuer obtains certificates; lego through a CA, or a fake in tests. It
// answers challenges through the manager's present/clean hooks.
type Issuer interface {
	Issue(ctx context.Context, identifiers []string, profile string) (*Issued, error)
	// RenewalWindow is the CA's suggested renewal window (ARI); ok false
	// when it has none.
	RenewalWindow(ctx context.Context, leaf *x509.Certificate) (start, end time.Time, ok bool)
}

// Options configure a Manager.
type Options struct {
	// Dir is where the account and the certificates are kept (dataDir/tls).
	Dir string
	// Identifiers are the DNS names or IP addresses the certificate is for;
	// empty means they come later from SetIdentifiers (the public address).
	Identifiers []string
	Profile     string
	Challenge   string
	// Issuer obtains the certificate; nil builds lego from ACME.
	Issuer Issuer
	ACME   *ACMEOptions
	// CertFile and KeyFile, when set, serve a pair of the person's own
	// instead of ACME, re-read when they change (FileCheck, hourly).
	CertFile, KeyFile string
	FileCheck         time.Duration

	Now func() time.Time
	Log *slog.Logger
	// Backoff after a failed order, doubling to BackoffMax: 1 h and 24 h.
	Backoff, BackoffMax time.Duration
}

// Manager keeps the certificate and answers tls.Config.GetCertificate.
type Manager struct {
	o      Options
	log    *slog.Logger
	now    func() time.Time
	issuer Issuer
	store  *store

	mu       sync.Mutex
	wanted   []string
	cur      *tls.Certificate
	leaf     *x509.Certificate
	curFor   []string
	renewAt  time.Time
	failures int
	nextTry  time.Time
	lastErr  string
	alpn     map[string]*tls.Certificate
	http     map[string]string
	fileTime time.Time
	wake     chan struct{}
	// afterRead, when set, runs in files mode once the pair has been read
	// and before what was read is recorded: a test's seam, nil otherwise.
	afterRead func()
}

var (
	errNoCertificate = errors.New("tls: no certificate yet (the first order has not completed)")
	errNoChallenge   = errors.New("tls: no acme-tls/1 challenge is open for that name")
)

// New makes a Manager. In ACME mode it loads what Dir holds for the
// identifiers; in files mode it reads the pair. Run keeps it current.
func New(o Options) (*Manager, error) {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	if o.Backoff <= 0 {
		o.Backoff = time.Hour
	}
	if o.BackoffMax <= 0 {
		o.BackoffMax = 24 * time.Hour
	}
	if o.FileCheck <= 0 {
		o.FileCheck = time.Hour
	}
	if o.Challenge == "" {
		o.Challenge = ChallengeTLSALPN
	}
	m := &Manager{o: o, log: o.Log, now: o.Now, alpn: map[string]*tls.Certificate{}, http: map[string]string{}, wake: make(chan struct{}, 1)}
	if o.CertFile != "" {
		if err := m.loadFiles(m.filesTime()); err != nil {
			return nil, err
		}
		return m, nil
	}
	if o.Dir == "" {
		return nil, errors.New("certs: a directory is needed")
	}
	m.store = &store{dir: o.Dir}
	if err := m.store.init(); err != nil {
		return nil, err
	}
	if o.Issuer != nil {
		m.issuer = o.Issuer
	} else {
		if o.ACME == nil {
			return nil, errors.New("certs: ACME options or an issuer are needed")
		}
		m.issuer = newLegoIssuer(m, *o.ACME, m.store)
	}
	if len(o.Identifiers) > 0 {
		m.SetIdentifiers(o.Identifiers)
	}
	return m, nil
}

// SetIdentifiers names what the certificate must be for; nil holds orders
// (the public address is unknown). A certificate kept for these identifiers
// is served at once; otherwise the current one is dropped (it names
// something else) and an order starts.
func (m *Manager) SetIdentifiers(ids []string) {
	ids = normalize(ids)
	m.mu.Lock()
	if slices.Equal(ids, m.wanted) {
		m.mu.Unlock()
		return
	}
	m.wanted = ids
	m.failures, m.nextTry, m.lastErr = 0, time.Time{}, ""
	if !slices.Equal(ids, m.curFor) {
		m.cur, m.leaf, m.curFor, m.renewAt = nil, nil, nil, time.Time{}
		if len(ids) > 0 && m.store != nil {
			if issued, ok := m.store.load(ids); ok {
				if err := m.adoptLocked(ids, issued); err != nil {
					m.log.Warn("tls: the kept certificate could not be used", "identifiers", ids, "err", err.Error())
				}
			}
		}
	}
	m.mu.Unlock()
	m.poke()
}

// Ready reports whether a certificate is served.
func (m *Manager) Ready() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cur != nil
}

// Status is what the manager knows now.
func (m *Manager) Status() Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	st := Status{Mode: ModeACME, Identifiers: append([]string(nil), m.wanted...), Challenge: m.o.Challenge, Ready: m.cur != nil, LastError: m.lastErr, NextTry: m.nextTry, RenewAt: m.renewAt}
	if m.o.CertFile != "" {
		st.Mode, st.Challenge, st.Identifiers = ModeFiles, "", nil
		if m.leaf != nil {
			st.Identifiers = append(m.leaf.DNSNames, ipStrings(m.leaf)...)
		}
	}
	if m.leaf != nil {
		st.NotAfter = m.leaf.NotAfter
	}
	return st
}

// GetCertificate is tls.Config.GetCertificate: the challenge certificate on
// the acme-tls/1 protocol (by SNI, or for an IP identifier, which has no
// SNI, by the address the connection came in on), the current certificate
// otherwise, and an error before the first issuance.
func (m *Manager) GetCertificate(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if slices.Contains(hello.SupportedProtos, acmeTLSProto) {
		if c, ok := m.alpn[hello.ServerName]; ok && hello.ServerName != "" {
			return c, nil
		}
		if hello.ServerName == "" && hello.Conn != nil {
			if a, ok := hello.Conn.LocalAddr().(*net.TCPAddr); ok {
				if c, ok := m.alpn[a.IP.String()]; ok {
					return c, nil
				}
			}
		}
		if len(m.alpn) == 1 {
			for _, c := range m.alpn {
				return c, nil
			}
		}
		return nil, errNoChallenge
	}
	if m.cur == nil {
		return nil, errNoCertificate
	}
	return m.cur, nil
}

// HTTP01 answers GET /.well-known/acme-challenge/{token} while an order is open.
func (m *Manager) HTTP01(token string) (keyAuth string, ok bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	keyAuth, ok = m.http[token]
	return keyAuth, ok
}

// Run keeps the certificate current until ctx ends: orders when there is
// none for the identifiers or the renewal time has come, waits otherwise,
// backs off after a failure; in files mode, re-reads the pair when it
// changes.
func (m *Manager) Run(ctx context.Context) {
	for {
		wait := m.step(ctx)
		if ctx.Err() != nil {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-m.wake:
		case <-time.After(wait):
		}
	}
}

// step does what is due now and returns how long to wait before the next.
func (m *Manager) step(ctx context.Context) time.Duration {
	if m.o.CertFile != "" {
		if err := m.reloadFilesIfChanged(); err != nil {
			m.log.Warn("tls: certificate files", "err", err.Error())
		}
		return m.o.FileCheck
	}
	now := m.now()
	m.mu.Lock()
	ids := append([]string(nil), m.wanted...)
	due := len(ids) > 0 && (m.cur == nil || !now.Before(m.renewAt)) && !now.Before(m.nextTry)
	var wait time.Duration = 24 * time.Hour
	if len(ids) > 0 {
		for _, t := range []time.Time{m.renewAt, m.nextTry} {
			if !t.IsZero() && t.After(now) {
				wait = min(wait, t.Sub(now))
			}
		}
	}
	m.mu.Unlock()
	if !due {
		return max(wait, time.Second)
	}
	m.issue(ctx, ids)
	return time.Second
}

// issue orders a certificate for ids and adopts it. When the identifiers
// changed while the order ran, what it brings is for identifiers no longer
// wanted: a certificate is kept for them, to be served at once if they come
// back, but never served for the new ones, and a failure is not counted
// against the new ones. SetIdentifiers has woken Run, so the next step
// orders for the identifiers wanted now.
func (m *Manager) issue(ctx context.Context, ids []string) {
	m.log.Info("tls: ordering a certificate", "identifiers", ids, "challenge", m.o.Challenge, "profile", m.o.Profile)
	issued, err := m.issuer.Issue(ctx, ids, m.o.Profile)
	m.mu.Lock()
	defer m.mu.Unlock()
	if !slices.Equal(ids, m.wanted) {
		if err == nil {
			if _, _, err = parseIssued(issued, m.now()); err == nil {
				err = m.store.save(ids, issued)
			}
		}
		if err != nil {
			m.log.Info("tls: an order for identifiers no longer wanted came to nothing", "identifiers", ids, "wanted", m.wanted, "err", err.Error())
			return
		}
		m.log.Info("tls: a certificate for identifiers no longer wanted is kept, not served", "identifiers", ids, "wanted", m.wanted)
		return
	}
	if err == nil {
		err = m.adoptLocked(ids, issued)
		if err == nil {
			if saveErr := m.store.save(ids, issued); saveErr != nil {
				m.log.Warn("tls: the certificate could not be kept", "err", saveErr.Error())
			}
		}
	}
	if err != nil {
		m.failures++
		backoff := min(m.o.Backoff<<(m.failures-1), m.o.BackoffMax)
		if backoff <= 0 {
			backoff = m.o.BackoffMax
		}
		m.nextTry = m.now().Add(backoff)
		m.lastErr = err.Error()
		m.log.Warn("tls: the order failed", "identifiers", ids, "err", err.Error(), "nextTry", m.nextTry.Format(time.RFC3339))
		return
	}
	m.failures, m.nextTry, m.lastErr = 0, time.Time{}, ""
	m.log.Info("tls: certificate issued", "identifiers", ids, "notAfter", m.leaf.NotAfter.Format(time.RFC3339), "renewAt", m.renewAt.Format(time.RFC3339))
}

// parseIssued reads issued as a certificate that can be served at now: a
// key pair that matches and a leaf that has not expired.
func parseIssued(issued *Issued, now time.Time) (tls.Certificate, *x509.Certificate, error) {
	cert, err := tls.X509KeyPair(issued.CertPEM, issued.KeyPEM)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	if !now.Before(leaf.NotAfter) {
		return tls.Certificate{}, nil, errors.New("the certificate has expired")
	}
	cert.Leaf = leaf
	return cert, leaf, nil
}

// adoptLocked makes issued the certificate served for ids (m.mu held).
func (m *Manager) adoptLocked(ids []string, issued *Issued) error {
	now := m.now()
	cert, leaf, err := parseIssued(issued, now)
	if err != nil {
		return err
	}
	m.cur, m.leaf, m.curFor = &cert, leaf, append([]string(nil), ids...)
	m.renewAt = renewAt(leaf, now)
	if m.issuer != nil {
		if start, end, ok := m.issuer.RenewalWindow(context.Background(), leaf); ok && start.Before(m.renewAt) {
			at := start
			if w := end.Sub(start); w > 0 {
				at = start.Add(time.Duration(rand.Int64N(int64(w))))
			}
			if at.Before(now) {
				at = now
			}
			m.renewAt = at
		}
	}
	return nil
}

// presentALPN and cleanALPN are the tls-alpn-01 provider's hooks.
func (m *Manager) presentALPN(identifier string, cert *tls.Certificate) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.alpn[identifier] = cert
}

func (m *Manager) cleanALPN(identifier string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.alpn, identifier)
}

// presentHTTP and cleanHTTP are the http-01 provider's hooks.
func (m *Manager) presentHTTP(token, keyAuth string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.http[token] = keyAuth
}

func (m *Manager) cleanHTTP(token string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.http, token)
}

// poke wakes Run.
func (m *Manager) poke() {
	select {
	case m.wake <- struct{}{}:
	default:
	}
}

func normalize(ids []string) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if id != "" {
			out = append(out, id)
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

func ipStrings(leaf *x509.Certificate) []string {
	var out []string
	for _, ip := range leaf.IPAddresses {
		out = append(out, ip.String())
	}
	return out
}
