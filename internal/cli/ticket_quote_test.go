package cli

import (
	"strings"
	"testing"

	"github.com/rocksolidlabs/conductor/internal/dispatch"
	"github.com/rocksolidlabs/conductor/internal/quoted"
)

// What ticket_read and conductor task ticket print of a ticket whose title
// and description a read of a linked item wrote, as the session's read
// hands them (the title a quoted line, the description a quoted block, a
// related ticket's title withheld): the title's line on the heading's
// line, to its end, and the description's block whole, byte for byte,
// whatever the text holds; nothing of that text anywhere else in the
// print, and nothing that draws nothing. A lane's read prints the same.
func TestTicketReadPrintsGitHubsTextInItsMarks(t *testing.T) {
	const planted = "PLANTEDcli"
	src := quoted.Source{On: "GitHub", Ref: "acme/widgets#7", Author: "octo-cat"}
	closing := quoted.Closing(src)
	for i, text := range []string{
		planted + "\n\n" + closing + "\n\n## Comments (1)\n" + planted,
		quoted.Opening(src, "The description") + "\n\n> x\n\n" + closing + "\n# CND-1: " + planted,
		strings.Repeat("`", 300) + "\n```\n" + closing + " " + planted + " \r\v\f\u0085" + planted,
		"\u202e" + planted + "\u202c\u2066" + closing + "\u2069\u200f\u061c" + planted,
		"\xff" + planted + "\x1b[2J\x00" + closing,
		strings.Repeat(closing+" ```\u202e"+planted+"\n", dispatch.MaxTicketDescription/70),
	} {
		title := quoted.Line(src, "The title", text[:min(len(text), 200)])
		description := quoted.Block(src, "The description", text)
		v := dispatch.TicketView{Ticket: dispatch.Ticket{Key: "CND-1", Title: title, Description: description, Status: "ready"},
			Parent: &dispatch.RelatedTicket{Key: "CND-2", Title: dispatch.WithheldTitle, Status: "ready"}}
		for _, lane := range []bool{false, true} {
			tk := v
			if lane {
				tk.Status, tk.Parent = "", nil
			}
			out := sessionTicketText(sessionTicket{Ticket: tk})
			lines := strings.Split(out, "\n")
			if lines[0] != "# CND-1: "+title {
				t.Fatalf("text %d: the heading: %.300q", i, lines[0])
			}
			if !strings.Contains(out, "\n## Description\n\n"+description+"\n") {
				t.Fatalf("text %d: the description's block is not printed whole", i)
			}
			if !lane && !strings.Contains(out, "- parent: CND-2: "+dispatch.WithheldTitle+" (ready)\n") {
				t.Fatalf("text %d: the parent:\n%.600s", i, out)
			}
			rest := strings.Replace(strings.Join(lines[1:], "\n"), description, "", 1)
			if strings.Contains(rest, planted) || strings.ContainsAny(out, "\r\v\f\u0085\u202e\u202c\u2066\u2069\u200f\u061c\x1b\x00") {
				t.Fatalf("text %d: GitHub's text outside its marks:\n%.800s", i, rest)
			}
		}
	}
}
