package proc

import (
	"slices"

	"golang.org/x/sys/unix"
)

// sZomb is the state of a process that has exited and is not reaped yet
// (SZOMB, the kernel's sys/proc.h).
const sZomb = 5

// groupMembers is the processes of the process group pgid that have not
// exited, self left out: the kernel's list of the group's processes
// (sysctl kern.proc.pgrp).
func groupMembers(pgid, self int) ([]int, error) {
	procs, err := unix.SysctlKinfoProcSlice("kern.proc.pgrp", pgid)
	if err != nil {
		return nil, err
	}
	var pids []int
	for _, p := range procs {
		if pid := int(p.Proc.P_pid); pid != self && p.Proc.P_stat != sZomb {
			pids = append(pids, pid)
		}
	}
	return pids, nil
}

// stillMembers is those of pids that are still processes of the group
// pgid that have not exited: the group listed again, which is cheap here.
func stillMembers(pgid int, pids []int) []int {
	now, err := groupMembers(pgid, 0)
	if err != nil {
		return nil
	}
	var out []int
	for _, pid := range pids {
		if slices.Contains(now, pid) {
			out = append(out, pid)
		}
	}
	return out
}

// exited reports whether pid, a child not reaped yet, has exited: its
// state a zombie's (sysctl kern.proc.pid). One that cannot be read is not
// known to have.
func exited(pid int) bool {
	p, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	return err == nil && p.Proc.P_stat == sZomb
}
