//go:build unix

package proc

import (
	"context"
	"errors"
	"syscall"
	"time"
)

// groupGonePoll is how often GroupGone looks at the group again.
const groupGonePoll = 10 * time.Millisecond

// GroupGone proves the process group pgid ended, within ctx: nil once no
// process that can run is left in it. A signal 0 to the group that finds
// none (ESRCH) says so; so does a group whose members have all exited and
// wait to be reaped (zombies: where nothing reaps orphans, a server that is
// process 1 with no init, they stay, and a zombie runs nothing), read from
// the group's processes (groupMembers, on Linux and macOS). What is still
// running there (what the group's leader left) is sent SIGKILL once. The
// group's number is not reused while a member is in it, so the kill reaches
// that group alone. A move between ways asks it before it starts the other
// way: two writers to one conversation are never left.
func GroupGone(ctx context.Context, pgid int) error {
	if pgid <= 0 {
		return nil
	}
	killed := false
	for {
		err := syscall.Kill(-pgid, 0)
		switch {
		case errors.Is(err, syscall.ESRCH):
			return nil
		case err != nil && !errors.Is(err, syscall.EPERM):
			return err
		}
		if live, lerr := groupMembers(pgid, -1); lerr == nil && len(live) == 0 {
			// Only zombies are left.
			return nil
		}
		if !killed {
			_ = syscall.Kill(-pgid, syscall.SIGKILL)
			killed = true
		}
		select {
		case <-ctx.Done():
			return errors.New("proc: the process group did not end")
		case <-time.After(groupGonePoll):
		}
	}
}
