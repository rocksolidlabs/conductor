package pty

import (
	"slices"
	"strings"
	"testing"
)

func TestBuildEnv(t *testing.T) {
	parent := []string{
		"PATH=/bin", "HOME=/home/x", "SECRET=1", "LC_ALL=C", "CONDUCTOR_WORKBENCH_TOKEN=t",
		"LD_PRELOAD=/evil.so", "EXTRA=yes", "TERM=dumb", "CONDUCTOR_BIN=/from/the/parent",
	}
	set := map[string]string{"FOO": "bar", "CONDUCTOR_X": "no", "LD_LIBRARY_PATH": "/x", "CONDUCTOR_BIN": "/from/the/catalog"}
	env := BuildEnv(parent, []string{"EXTRA", "CONDUCTOR_BIN"}, set, Inject("s1", "http://c/api/sessions/s1/attention", "tok", "/opt/conductor"))
	want := []string{"COLORTERM=truecolor", "CONDUCTOR_BIN=/opt/conductor", "CONDUCTOR_NOTIFY_TOKEN=tok", "CONDUCTOR_NOTIFY_URL=http://c/api/sessions/s1/attention", "CONDUCTOR_SESSION_ID=s1", "EXTRA=yes", "FOO=bar", "HOME=/home/x", "LC_ALL=C", "PATH=/bin", "TERM=xterm-256color"}
	if !slices.Equal(env, want) {
		t.Fatalf("got %v\nwant %v", env, want)
	}
}

// CONDUCTOR_BIN is the binary Conductor injects, or nothing when it has none:
// never what the parent or the catalog says.
func TestInjectWithoutABinary(t *testing.T) {
	inject := Inject("s1", "http://c/api/sessions/s1/attention", "tok", "")
	if _, ok := inject["CONDUCTOR_BIN"]; ok || len(inject) != 3 {
		t.Fatalf("inject %v", inject)
	}
	env := BuildEnv([]string{"CONDUCTOR_BIN=/from/the/parent"}, []string{"CONDUCTOR_BIN"}, nil, inject)
	if slices.ContainsFunc(env, func(kv string) bool { return strings.HasPrefix(kv, "CONDUCTOR_BIN=") }) {
		t.Fatalf("env %v", env)
	}
}

// A server's own session never gets the host's socket: neither from the
// server's environment, even passed through by name, nor from the catalog,
// and Inject sets none.
func TestAServerSessionGetsNoHostSocket(t *testing.T) {
	const name = "CONDUCTOR_HOST_SOCKET"
	inject := Inject("s1", "http://c/api/sessions/s1/attention", "tok", "/opt/conductor")
	if _, ok := inject[name]; ok {
		t.Fatalf("inject %v", inject)
	}
	env := BuildEnv([]string{name + "=/run/user/1/conductor-host-1/report.sock"}, []string{name}, map[string]string{name: "/tmp/x/report.sock"}, inject)
	if slices.ContainsFunc(env, func(kv string) bool { return strings.HasPrefix(kv, name+"=") }) {
		t.Fatalf("env %v", env)
	}
}
