package pty

import (
	"errors"
	"os"
)

// errDirChanged refuses a start whose directory's path no longer names the
// directory its caller opened (Spec.DirFile).
var errDirChanged = errors.New("the working directory changed since it was opened")

// StartDir is the directory a child another runner starts (internal/proc's
// StartPipes) changes into before it runs its command, as Start's child
// does: dir, or, with dirFile (dir as its caller opened and checked it),
// the open directory itself on Linux and dir checked to still name it
// elsewhere. The caller keeps dirFile open until the child has started.
func StartDir(dir string, dirFile *os.File) (string, error) {
	return startDir(Spec{Dir: dir, DirFile: dirFile})
}

// checkedDir is dir once it is checked to name want, the directory a
// caller opened.
func checkedDir(dir string, want os.FileInfo) (string, error) {
	got, err := os.Stat(dir)
	if err != nil || !os.SameFile(got, want) {
		return "", errDirChanged
	}
	return dir, nil
}
