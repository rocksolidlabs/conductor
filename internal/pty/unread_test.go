package pty

import (
	"context"
	"io"
	"os"
	"testing"
	"time"
)

// Unread is the kernel's count of the output not read yet: what the
// program printed, until it is read, then none (#902: a line to a sign-in
// screen waits until the screen it is judged on holds all of it).
func TestUnreadCountsTheOutputNotReadYet(t *testing.T) {
	p, err := Start(Spec{Argv: []string{"/bin/sh", "-c", "printf abcdef; exec sleep 30"}, Env: BuildEnv(os.Environ(), nil, nil, nil)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.Stop(context.Background(), time.Second) })
	deadline := time.Now().Add(30 * time.Second)
	for {
		n, err := p.Unread()
		if err != nil {
			t.Fatal(err)
		}
		if n == 6 {
			break
		}
		if n > 6 || time.Now().After(deadline) {
			t.Fatalf("unread %d, want 6", n)
		}
		time.Sleep(5 * time.Millisecond)
	}
	b := make([]byte, 6)
	if _, err := io.ReadFull(p, b); err != nil || string(b) != "abcdef" {
		t.Fatalf("read %q %v", b, err)
	}
	if n, err := p.Unread(); n != 0 || err != nil {
		t.Fatalf("unread after the read: %d %v", n, err)
	}
}
