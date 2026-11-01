// Package plant is a fixture of internal/opencore's test: one planted
// import of a private module of the organization, beside imports the rule
// lets through.
package plant

import (
	"fmt"

	_ "github.com/rocksolidlabs/conductor-premium/register"
	_ "github.com/rocksolidlabs/conductor/ext"
	_ "github.com/rocksolidlabs/conductor/internal/session"
	_ "github.com/rocksolidlabsx/elsewhere"
)

var _ = fmt.Sprint
