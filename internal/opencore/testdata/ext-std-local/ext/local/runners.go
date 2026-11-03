// Package local is a fixture of internal/opencore's test: the two runners
// the native Runtime is made of, which ext/local alone may import beside
// the standard library and ext's own packages.
package local

import (
	"io"

	_ "github.com/rocksolidlabs/conductor/internal/proc"
	_ "github.com/rocksolidlabs/conductor/internal/pty"
)

var _ = io.EOF
