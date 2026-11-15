//go:build darwin

package pty

// fionread asks a terminal how many bytes wait to be read: FIONREAD,
// _IOR('f', 127, int) in <sys/filio.h>, which golang.org/x/sys/unix does
// not name for Darwin.
const fionread = 0x4004667f
