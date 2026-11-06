//go:build !darwin

package pty

// keepsTTY is false here: Linux keeps what a process printed for the
// master after the slave side's last close, so the parent closes its
// descriptor at once, as creack/pty does (see keep_darwin.go).
const keepsTTY = false
