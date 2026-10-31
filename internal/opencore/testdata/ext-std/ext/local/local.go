// Package local is a fixture of internal/opencore's test: ext's own
// packages and the standard library, which ext/ may import.
package local

import (
	"io"

	_ "github.com/rocksolidlabs/conductor/ext"
)

var _ = io.EOF
