package proc

import (
	"bytes"
	"os"
	"strconv"
)

// groupMembers is the processes of the process group pgid that have not
// exited, self left out: every process's stat read from /proc (a listing
// that is not cheap, so it is made rarely: stillMembers follows the ones
// it found). A process gone meanwhile is passed over; /proc that cannot be
// listed is an error.
func groupMembers(pgid, self int) ([]int, error) {
	d, err := os.Open("/proc")
	if err != nil {
		return nil, err
	}
	defer d.Close()
	names, err := d.Readdirnames(-1)
	if err != nil {
		return nil, err
	}
	var pids []int
	for _, name := range names {
		pid, err := strconv.Atoi(name)
		if err != nil || pid == self {
			continue
		}
		if inGroup(pid, pgid) {
			pids = append(pids, pid)
		}
	}
	return pids, nil
}

// stillMembers is those of pids that are still processes of the group
// pgid that have not exited.
func stillMembers(pgid int, pids []int) []int {
	var out []int
	for _, pid := range pids {
		if inGroup(pid, pgid) {
			out = append(out, pid)
		}
	}
	return out
}

// inGroup reports whether pid is a process of the group pgid that has not
// exited. A pid used again since is another process, in the group only as
// a member's own child.
func inGroup(pid, pgid int) bool {
	state, g, ok := stat(pid)
	return ok && state != 'Z' && state != 'X' && g == pgid
}

// exited reports whether pid, a child not reaped yet, has exited: its
// state a zombie's. One whose stat cannot be read is not known to have.
func exited(pid int) bool {
	state, _, ok := stat(pid)
	return ok && (state == 'Z' || state == 'X')
}

// stat reads pid's state and process group from its stat: the first and
// the third fields after its command's name (which may hold spaces and
// parentheses, so they are read from its last parenthesis).
func stat(pid int) (state byte, pgid int, ok bool) {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return 0, 0, false
	}
	i := bytes.LastIndexByte(b, ')')
	if i < 0 {
		return 0, 0, false
	}
	f := bytes.Fields(b[i+1:])
	if len(f) < 3 {
		return 0, 0, false
	}
	g, err := strconv.Atoi(string(f[2]))
	if err != nil {
		return 0, 0, false
	}
	return f[0][0], g, true
}
