package certs

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
	"time"
)

// loadFiles reads the configured pair. A pair that loads records at, the
// files' time taken before the read: a time taken after it can be a change
// the read never saw (a key written after its certificate, a pair replaced
// mid-read), and no later check would see the files as changed (#281). A
// pair that fails records no time, so every check reads it again until it
// loads, whatever the file system's timestamp granularity made of the
// writes that follow.
func (m *Manager) loadFiles(at time.Time) error {
	cert, err := tls.LoadX509KeyPair(m.o.CertFile, m.o.KeyFile)
	if err == nil {
		var leaf *x509.Certificate
		if leaf, err = x509.ParseCertificate(cert.Certificate[0]); err == nil {
			cert.Leaf = leaf
		} else {
			err = fmt.Errorf("%s: %w", m.o.CertFile, err)
		}
	}
	if m.afterRead != nil {
		m.afterRead()
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err != nil {
		err = fmt.Errorf("tls: %w", err)
		m.lastErr = err.Error()
		return err
	}
	m.cur, m.leaf, m.lastErr = &cert, cert.Leaf, ""
	m.fileTime = at
	return nil
}

// filesTime is the later modification time of the two files.
func (m *Manager) filesTime() time.Time {
	var t time.Time
	for _, p := range []string{m.o.CertFile, m.o.KeyFile} {
		if fi, err := os.Stat(p); err == nil && fi.ModTime().After(t) {
			t = fi.ModTime()
		}
	}
	return t
}

// reloadFilesIfChanged re-reads the pair when either file is newer than
// the pair served; a pair that fails to load keeps the one served and
// records the error.
func (m *Manager) reloadFilesIfChanged() error {
	m.mu.Lock()
	last := m.fileTime
	m.mu.Unlock()
	at := m.filesTime()
	if !at.After(last) {
		return nil
	}
	if err := m.loadFiles(at); err != nil {
		return err
	}
	m.log.Info("tls: certificate files re-read", "certFile", m.o.CertFile)
	return nil
}
