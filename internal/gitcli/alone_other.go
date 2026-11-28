//go:build !unix

package gitcli

import (
	"context"
	"errors"
	"io"
)

// ErrNoGroup is Alone's git where the system offers no process group to
// end whole: nothing is run.
var ErrNoGroup = errors.New("git: this system cannot end git and what it starts as one")

// execAlone runs nothing here (ErrNoGroup): a git whose signing program
// could outlive its bound is not started.
func execAlone(context.Context, string, []string, map[string]string, io.Writer, io.Writer) error {
	return ErrNoGroup
}
