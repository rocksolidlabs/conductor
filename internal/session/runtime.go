package session

import (
	"errors"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/internal/pty"
)

// RuntimeProcess is t, a terminal a Runtime started (ext.Runtime's
// StartTerminal), as the Process a Local drives: its output, input, size,
// end and stop are t's, and its exit is t's again as a pty.ExitStatus, its
// words an error (Local reads only the code). The server and the host hand
// the Terminal to their Local and keep no other handle on it, so the Local
// stays the only writer to the session's process.
func RuntimeProcess(t ext.Terminal) Process { return runtimeProcess{t} }

type runtimeProcess struct{ ext.Terminal }

// Exit is the terminal's exit status, its Err as an error ("" for none).
func (p runtimeProcess) Exit() pty.ExitStatus {
	st := p.Terminal.Exit()
	out := pty.ExitStatus{Code: st.Code, Signal: st.Signal}
	if st.Err != "" {
		out.Err = errors.New(st.Err)
	}
	return out
}
