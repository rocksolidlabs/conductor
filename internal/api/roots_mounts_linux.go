//go:build linux

package api

import (
	"os"
)

// systemMounts are the mount points of the server's view of the file
// system (/proc/self/mountinfo), each as a path; none when it cannot be
// read.
func systemMounts() []string {
	b, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return nil
	}
	return parseMountinfo(string(b))
}
