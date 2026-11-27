//go:build !linux

package api

// systemMounts are none here: the folders below a folder are judged by
// their names' identities alone.
func systemMounts() []string { return nil }
