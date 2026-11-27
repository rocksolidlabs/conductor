//go:build !linux && !darwin

package api

import "os"

// birthOf is 0: no birth time is read on this system.
func birthOf(string, os.FileInfo) int64 { return 0 }
