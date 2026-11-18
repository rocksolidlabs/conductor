package crew

import (
	"fmt"
	"strings"

	"github.com/rocksolidlabs/conductor/internal/quoted"
)

// writeSubject writes task.md's "## Subject" for a run a registered
// provider launched with one: the provider's label, the subject's
// reference and link, and the earlier run, each a line of its own quoted
// as written outside Conductor (internal/quoted), so none of them can
// make a heading or an instruction; the start commit, a commit id, in
// Conductor's own words. Nothing of it is cut. Nothing is written for a
// run with no subject.
func writeSubject(b *strings.Builder, f TaskFile) {
	s := f.Subject
	if s == nil {
		return
	}
	src := f.subjectFrom
	fmt.Fprintf(b, "## Subject\n\nThis run was launched by %s for a subject it names; what it says of it is below, in its own words. The run follows it as launched.\n\n", quoted.Flat(src.On))
	fmt.Fprintf(b, "%s\n\n", quoted.Line(src, "Its label", f.subjectLabel))
	fmt.Fprintf(b, "%s\n\n", quoted.Line(src, "The subject reference", s.Ref))
	if s.URL != "" {
		fmt.Fprintf(b, "%s\n\n", quoted.Line(src, "The subject link", s.URL))
	}
	if s.StartCommit != "" {
		fmt.Fprintf(b, "The launch asked the run to start from the commit %s.\n\n", s.StartCommit)
	}
	if s.Prior != "" {
		fmt.Fprintf(b, "This run follows an earlier run of the same provider, whose records you may read.\n\n%s\n\n", quoted.Line(src, "The earlier run", s.Prior))
	}
}
