//go:build darwin

package pty

// keepsTTY says the parent keeps a descriptor of its own on the terminal's
// slave side until the process is reaped. On macOS the last close of the
// slave side waits a bounded time (about 0.6 s, measured on macOS 27) for
// the master to read what is queued and then discards it: a process that
// prints and exits at once, with its reader that much late (a loaded
// machine), left an empty terminal and an end of file at the first read.
// With the parent's descriptor open the child's exit is not the last
// close: the session leader's exit waits for the reader to drain the
// output, however late it is, and the parent closes its descriptor once
// the process is reaped, with nothing left queued.
const keepsTTY = true
