//go:build linux

package pty

import "golang.org/x/sys/unix"

// fionread asks a terminal how many bytes wait to be read (TIOCINQ, which
// is FIONREAD on Linux).
const fionread = unix.TIOCINQ
