package session

import (
	"context"
	"errors"
	"time"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/internal/pty"
)

// PipesChild is p, a child a Runtime started with pipes (ext.PipeStarter's
// StartPipes), as the Child of a session's protocol way: its end and exit
// are p's, its exit again as a pty.ExitStatus (its words an error), and its
// Stop closes the child's input first, which cancels a permission prompt
// it waits on, then stops it as a terminal's Stop does. The driver reads
// p's Stdout and writes its Stdin; the session holds no other handle.
func PipesChild(p ext.Pipes) Child { return pipesChild{p} }

type pipesChild struct{ p ext.Pipes }

func (c pipesChild) Done() <-chan struct{} { return c.p.Done() }

// Exit is the child's exit status, its Err as an error ("" for none).
func (c pipesChild) Exit() pty.ExitStatus {
	st := c.p.Exit()
	out := pty.ExitStatus{Code: st.Code, Signal: st.Signal}
	if st.Err != "" {
		out.Err = errors.New(st.Err)
	}
	return out
}

// Stop closes the child's input, then ends it within grace (Pipes.Stop).
func (c pipesChild) Stop(ctx context.Context, grace time.Duration) error {
	_ = c.p.Stdin().Close()
	return c.p.Stop(ctx, grace)
}
