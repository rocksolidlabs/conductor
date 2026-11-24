package local

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/internal/proc"
)

// The native Runtime is an ext.PipeStarter: Native's value, as the server
// and the extensions' set hold it, answers StartPipes with proc.StartPipes's
// child, given exactly its environment, its input and output pipes, its
// directory the one it was lent, and its pid; its Stop's wait after the
// force is the contract's.
func TestNativeRuntimeIsAPipeStarter(t *testing.T) {
	ps, ok := Native().(ext.PipeStarter)
	if !ok {
		t.Fatalf("local.Native() (%T) is no ext.PipeStarter", Native())
	}
	if filled, ok := Fill(ext.Providers{}).Runtime.(ext.PipeStarter); !ok {
		t.Fatalf("Fill's Runtime (%T) is no ext.PipeStarter", filled)
	}
	if ext.StopKillWait != proc.PipesKillWait {
		t.Fatalf("ext.StopKillWait %s, proc.PipesKillWait %s", ext.StopKillWait, proc.PipesKillWait)
	}
	dir := t.TempDir()
	f, err := os.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	env := append(testEnv(), "PIPES_WORD=a b;c")
	p, err := ps.StartPipes(context.Background(), ext.PipeSpec{
		Argv: []string{"/bin/sh", "-c", `env; echo end-of-env; read line; echo "got:$line"; exit 5`},
		Dir:  dir, DirFile: f, Env: env, Ref: ext.ProcessRef{Session: "s1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.Stop(context.Background(), 0) })
	if _, err := f.Stat(); err != nil {
		t.Fatalf("the folder lent was closed: %v", err)
	}
	if info := p.Process(); info.PID <= 0 || info.Group != info.PID || info.StartedAt.IsZero() {
		t.Fatalf("the process: %+v", info)
	}
	out := bufio.NewScanner(p.Stdout())
	var got []string
	for out.Scan() && out.Text() != "end-of-env" {
		got = append(got, out.Text())
	}
	for _, kv := range got {
		// The shell's own (its working directory, its level, its last
		// command) beside what it was given.
		if k, _, _ := strings.Cut(kv, "="); k != "PWD" && k != "SHLVL" && k != "_" && !slices.Contains(env, kv) {
			t.Errorf("the child has %s, which it was not given", k) // its name alone: it may be a token
		}
	}
	if !slices.Contains(got, "PIPES_WORD=a b;c") {
		t.Errorf("the child's environment lacks PIPES_WORD as it was given")
	}
	if _, err := io.WriteString(p.Stdin(), "hello\n"); err != nil {
		t.Fatal(err)
	}
	if !out.Scan() || out.Text() != "got:hello" {
		t.Fatalf("read %q (%v), want got:hello", out.Text(), out.Err())
	}
	done(t, pipesTerminal{p})
	if st := p.Exit(); st.Code != 5 || st.Signal != "" || st.Err != "" {
		t.Fatalf("the exit: %+v", st)
	}
	if err := p.Stop(context.Background(), hang); err != nil {
		t.Fatalf("a Stop once the child exited: %v", err)
	}
	ended, cancel := context.WithCancel(context.Background())
	cancel()
	// What a start that broke the rule would leave is stopped at the end.
	var late ext.Pipes
	t.Cleanup(func() {
		if late != nil {
			late.Stop(context.Background(), 0)
		}
	})
	late, err = ps.StartPipes(ended, ext.PipeSpec{Argv: []string{"sleep", "300"}, Dir: dir, Env: env})
	if late != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("under an ended context: %v, %v", late, err)
	}
}

// pipesTerminal lends Pipes' Done to the terminal tests' done.
type pipesTerminal struct{ ext.Pipes }

func (pipesTerminal) Read([]byte) (int, error)  { return 0, io.EOF }
func (pipesTerminal) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }
func (pipesTerminal) Resize(uint16, uint16) error {
	return errors.New("no terminal")
}
