package proc

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Prepare refuses what Run and Serve refuse, in bounds' order, in the
// words Result.Err (and Serve's error) carries for the same spec, and
// builds what the command would be given.
func TestPrepareRefusesAsRunDoes(t *testing.T) {
	dir := t.TempDir()
	for _, tc := range []struct {
		name string
		s    Spec
		want string // a part of the words; the first refusal's in bounds' order
	}{
		{"no command", Spec{Dir: dir, Timeout: time.Second}, "no command"},
		{"empty command, and no timeout", Spec{Argv: []string{""}, Dir: dir}, "no command"},
		{"no timeout, and a relative dir", Spec{Argv: []string{"true"}, Dir: "."}, "the timeout must be"},
		{"a long timeout", Spec{Argv: []string{"true"}, Dir: dir, Timeout: MaxTimeout + 1}, "the timeout must be"},
		{"a relative dir, and a CONDUCTOR_ name", Spec{Argv: []string{"true"}, Dir: ".", Timeout: time.Second, Env: map[string]string{"CONDUCTOR_Y": "1"}}, "must be absolute"},
		{"a CONDUCTOR_ name, and a bad Inject", Spec{Argv: []string{"true"}, Dir: dir, Timeout: time.Second, Env: map[string]string{"CONDUCTOR_Y": "1"}, Inject: map[string]string{"PATH": "x"}}, "a CONDUCTOR_ variable"},
		{"a loader", Spec{Argv: []string{"true"}, Dir: dir, Timeout: time.Second, Env: map[string]string{"LD_PRELOAD": "x"}}, "is never set"},
		{"not a name", Spec{Argv: []string{"true"}, Dir: dir, Timeout: time.Second, Env: map[string]string{"A=B": "x"}}, "is not a variable name"},
		{"an Inject not Conductor's", Spec{Argv: []string{"true"}, Dir: dir, Timeout: time.Second, Inject: map[string]string{"CONDUCTOR_NOTIFY_TOKEN": "x"}}, "is not one of Conductor's own names"},
		{"Environ beside Env", Spec{Argv: []string{"true"}, Dir: dir, Timeout: time.Second, Environ: []string{"A=1"}, Env: map[string]string{"CONDUCTOR_Y": "1"}}, "Environ beside Allow, Env or Inject"},
		{"Environ not NAME=value", Spec{Argv: []string{"true"}, Dir: dir, Timeout: time.Second, Environ: []string{"A"}}, "is not NAME=value"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env, err := Prepare(tc.s, false)
			if err == nil || env != nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Prepare: %v %q, want an error naming %q", env, err, tc.want)
			}
			if res := Run(context.Background(), tc.s); res.Err != err.Error() || res.Exit != -1 || !res.Started.IsZero() {
				t.Fatalf("Prepare said %q; Run %+v", err, res)
			}
			if tc.s.Timeout > 0 && tc.s.Timeout <= MaxTimeout {
				if _, serr := Serve(context.Background(), tc.s); serr == nil || serr.Error() != err.Error() {
					t.Fatalf("Prepare said %q; Serve %v", err, serr)
				}
			}
		})
	}
	// A service needs no timeout; a command does.
	s := Spec{Argv: []string{"true"}, Dir: dir}
	if _, err := Prepare(s, false); err == nil || !strings.Contains(err.Error(), "the timeout must be") {
		t.Fatalf("a command with no timeout: %v", err)
	}
	if env, err := Prepare(s, true); err != nil || !slices.Contains(env, "TERM=dumb") {
		t.Fatalf("a service with no timeout: %v %q", err, env)
	}
}

// What Prepare builds is the environment Run gives the command: the
// session's allowlist and Allow, Env, Inject, TERM=dumb and NO_COLOR=1;
// with Environ, exactly it, Conductor's own CONDUCTOR_RUN and
// CONDUCTOR_PORT_WEB among it (names Env would refuse), and nothing else.
func TestRunWithEnvironGivesExactlyIt(t *testing.T) {
	t.Setenv("CONDUCTOR_NOTIFY_TOKEN", "secret")
	t.Setenv("ALLOWED_BY_THE_CALLER", "yes")
	envProg, err := exec.LookPath("env")
	if err != nil {
		t.Skip("no env program")
	}
	base := Spec{Argv: []string{envProg}, Dir: t.TempDir(), Timeout: time.Minute,
		Allow: []string{"ALLOWED_BY_THE_CALLER"}, Env: map[string]string{"MY_VAR": "v"},
		Inject: map[string]string{"CONDUCTOR_RUN": "build-00000001", "CONDUCTOR_PORT_WEB": "41234"}}
	built, err := Prepare(base, false)
	if err != nil {
		t.Fatal(err)
	}
	res := Run(context.Background(), base)
	if res.Exit != 0 || res.Err != "" {
		t.Fatalf("%+v", res)
	}
	if got := lines(res.Head); !slices.Equal(sorted(got), sorted(built)) {
		t.Fatalf("Run gave\n%q\nPrepare built\n%q", got, built)
	}
	for _, want := range []string{"ALLOWED_BY_THE_CALLER=yes", "MY_VAR=v", "CONDUCTOR_RUN=build-00000001", "CONDUCTOR_PORT_WEB=41234", "TERM=dumb", "NO_COLOR=1"} {
		if !slices.Contains(built, want) {
			t.Errorf("Prepare built no %s: %q", want, built)
		}
	}
	if slices.ContainsFunc(built, func(kv string) bool { return strings.HasPrefix(kv, "CONDUCTOR_NOTIFY_TOKEN=") }) {
		t.Errorf("Prepare passed the server's token on: %q", built)
	}

	environ := []string{"CONDUCTOR_RUN=build-00000002", "CONDUCTOR_PORT_WEB=8080", "ONLY=this", "EMPTY="}
	res = Run(context.Background(), Spec{Argv: []string{envProg}, Dir: t.TempDir(), Timeout: time.Minute, Environ: environ})
	if res.Exit != 0 || res.Err != "" {
		t.Fatalf("%+v", res)
	}
	if got := lines(res.Head); !slices.Equal(sorted(got), sorted(environ)) {
		t.Fatalf("with Environ the command got\n%q\nwant exactly\n%q", got, environ)
	}
	again, err := Prepare(Spec{Argv: []string{envProg}, Dir: t.TempDir(), Timeout: time.Minute, Environ: environ}, false)
	if err != nil || !slices.Equal(again, environ) {
		t.Fatalf("Prepare of an Environ: %q %v", again, err)
	}
	again[0] = "CHANGED=1"
	if environ[0] != "CONDUCTOR_RUN=build-00000002" {
		t.Fatal("Prepare answered the caller's own slice")
	}
}

// Environ is the whole environment: beside Allow, Env or Inject it is
// refused, and so is an entry that is not NAME=value, before anything
// starts.
func TestEnvironBesideAllowIsRefused(t *testing.T) {
	dir := t.TempDir()
	base := Spec{Argv: []string{"true"}, Dir: dir, Timeout: time.Minute, Environ: []string{"A=1"}}
	for name, change := range map[string]func(*Spec){
		"Allow":  func(s *Spec) { s.Allow = []string{"HOME"} },
		"Env":    func(s *Spec) { s.Env = map[string]string{"B": "2"} },
		"Inject": func(s *Spec) { s.Inject = map[string]string{"CONDUCTOR_RUN": "r"} },
	} {
		s := base
		change(&s)
		if res := Run(context.Background(), s); res.Err != "proc: Environ beside Allow, Env or Inject" || res.Exit != -1 || !res.Started.IsZero() {
			t.Errorf("Environ beside %s: %+v", name, res)
		}
	}
	for _, kv := range []string{"NOEQUALS", "=value", "A\x00B=1", ""} {
		s := base
		s.Environ = []string{"A=1", kv}
		if res := Run(context.Background(), s); !strings.Contains(res.Err, "is not NAME=value") || res.Exit != -1 || !res.Started.IsZero() {
			t.Errorf("Environ entry %q: %+v", kv, res)
		}
	}
	// An empty Environ is an empty environment, not the default one.
	envProg, err := exec.LookPath("env")
	if err != nil {
		return
	}
	res := Run(context.Background(), Spec{Argv: []string{envProg}, Dir: dir, Timeout: time.Minute, Environ: []string{}})
	if res.Exit != 0 || len(res.Head) != 0 {
		t.Fatalf("an empty Environ: %+v", res)
	}
}

// A Started that panics leaves nothing running (the Runtime's contract,
// docs/plans/runtime.md): Run and Serve let the same panic go on, and by
// the time it is recovered the command's group is killed, its leader
// reaped (its pid no longer answers) and its child gone. Started waits for
// the pid files the command writes, so both exist when it panics.
func TestAStartedThatPanicsEndsTheCommand(t *testing.T) {
	type boom struct{ n int }
	for _, how := range []string{"Run", "Serve"} {
		t.Run(how, func(t *testing.T) {
			dir := t.TempDir()
			var leader, child int
			s := Spec{Argv: []string{"sh", "-c", "sleep 30 & echo $! > child.pid; echo $$ > leader.pid; wait"}, Dir: dir, Timeout: time.Minute,
				Started: func(p Process) {
					child = pidFile(t, filepath.Join(dir, "child.pid"))
					leader = pidFile(t, filepath.Join(dir, "leader.pid"))
					if leader != p.PID {
						t.Errorf("the leader wrote %d, Started was told %d", leader, p.PID)
					}
					panic(boom{7})
				}}
			got := func() (v any) {
				defer func() { v = recover() }()
				if how == "Run" {
					Run(context.Background(), s)
				} else {
					Serve(context.Background(), s)
				}
				return nil
			}()
			if got != (boom{7}) {
				t.Fatalf("the panic that left %s: %#v", how, got)
			}
			if err := syscall.Kill(leader, 0); !errors.Is(err, syscall.ESRCH) {
				t.Fatalf("the leader %d is still there (unreaped, or running): %v", leader, err)
			}
			if !gone(t, child, 10*time.Second) {
				syscall.Kill(child, syscall.SIGKILL)
				t.Fatalf("the child %d outlived the panic", child)
			}
		})
	}
}

func lines(b []byte) []string {
	var out []string
	for _, l := range strings.Split(string(b), "\n") {
		if l != "" {
			out = append(out, l)
		}
	}
	return out
}

func sorted(s []string) []string {
	s = slices.Clone(s)
	slices.Sort(s)
	return s
}
