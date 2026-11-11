//go:build unix

package agents

import (
	"bufio"
	"errors"
	"io"
	"strings"
	"testing"
)

// chunked gives its input in reads of the sizes it is given, in turn, and
// then of the last size: a pipe's input as it arrives.
type chunked struct {
	s     string
	sizes []int
}

func (c *chunked) Read(p []byte) (int, error) {
	if c.s == "" {
		return 0, io.EOF
	}
	n := c.sizes[0]
	if len(c.sizes) > 1 {
		c.sizes = c.sizes[1:]
	}
	n = min(n, len(p), len(c.s))
	copy(p, c.s[:n])
	c.s = c.s[n:]
	return n, nil
}

// TestUsageBoundEdges holds each bound of the readers at its edge, on the
// raw bytes it bounds, with input that arrives in several chunks: a line
// of usageMaxLine bytes passes and one more fails, a '\r', a space and a
// blank line's bytes counting as the line's, only its final '\n' not; all
// of the output at usageMaxOutput passes and one byte more fails; and the
// field bounds at their size and one byte more.
func TestUsageBoundEdges(t *testing.T) {
	max := usageMaxLine
	x := func(n int) string { return strings.Repeat("x", n) }
	cases := []struct {
		name  string
		in    string
		lines int
		err   error
	}{
		{"line at the bound", x(max) + "\n", 1, nil},
		{"line past the bound", x(max+1) + "\n", 0, errLineTooLong},
		{"line at the bound, last, no newline", x(max), 1, nil},
		{"line past the bound, last, no newline", x(max + 1), 0, errLineTooLong},
		{"CR counts: at the bound", x(max-1) + "\r\n", 1, nil},
		{"CR counts: past the bound", x(max) + "\r\n", 0, errLineTooLong},
		{"carriage returns, then a line", strings.Repeat("\r", 2*max) + `{"type":"result"}` + "\n", 0, errLineTooLong},
		{"spaces count", strings.Repeat(" ", max) + "x\n", 0, errLineTooLong},
		{"a blank line at the bound", strings.Repeat(" ", max) + "\nok\n", 1, nil},
	}
	// The output: lines at the bound until usageMaxOutput bytes in all,
	// exactly, then one byte more.
	full := strings.Repeat(x(max-1)+"\n", usageMaxOutput/max)
	cases = append(cases,
		struct {
			name  string
			in    string
			lines int
			err   error
		}{"output at the bound", full, usageMaxOutput / max, nil},
		struct {
			name  string
			in    string
			lines int
			err   error
		}{"output past the bound", full + "\n", usageMaxOutput / max, errOutputTooLarge},
	)
	for _, c := range cases {
		for _, sizes := range [][]int{{7, 4093, 65536}, {max - 1, 1, 1, 65536}, {max, 1, 65536}, {1 << 20, 3, 65536}} {
			b := &boundedLines{r: bufio.NewReaderSize(&chunked{s: c.in, sizes: sizes}, 64<<10)}
			n := 0
			var err error
			for {
				var line []byte
				if line, err = b.next(); err != nil {
					break
				}
				if len(line) == 0 {
					t.Fatalf("%s: an empty line returned", c.name)
				}
				n++
			}
			if errors.Is(err, io.EOF) {
				err = nil
			}
			if n != c.lines || !errors.Is(err, c.err) || (c.err == nil && err != nil) {
				t.Errorf("%s, chunks %v: %d lines, %v; want %d, %v", c.name, sizes[:min(len(sizes), 2)], n, err, c.lines, c.err)
			}
		}
	}

	// The field bounds, each at its size and one byte more.
	if _, err := shownName(x(usageMaxLimit), usageMaxLimit, "f"); err != nil {
		t.Errorf("a limit id of %d bytes: %v", usageMaxLimit, err)
	}
	if _, err := shownName(x(usageMaxLimit+1), usageMaxLimit, "f"); err == nil {
		t.Errorf("a limit id of %d bytes taken", usageMaxLimit+1)
	}
	at, past := "1"+strings.Repeat("0", usageMaxFigure-1), "1"+strings.Repeat("0", usageMaxFigure)
	if v, err := figure(&at, "f"); err != nil || *v != at {
		t.Errorf("a figure of %d bytes: %v", usageMaxFigure, err)
	}
	if _, err := figure(&past, "f"); err == nil {
		t.Errorf("a figure of %d bytes taken", usageMaxFigure+1)
	}
	if got := clip(x(usageMaxWords)+"é", usageMaxWords); len(got) != usageMaxWords {
		t.Errorf("words clipped to %d bytes", len(got))
	}
	if got := clip(x(usageMaxWords-1)+"é", usageMaxWords); len(got) != usageMaxWords-1 {
		t.Errorf("words clipped inside a character: %d bytes", len(got))
	}
}

// TestUsagePrintBounds: a version's print (stdout and stderr together) and
// Claude Code's auth status are read up to their bound, counted on every
// byte printed, and one byte more fails the read.
func TestUsagePrintBounds(t *testing.T) {
	version := claudeUsageQualified + " (Claude Code)"
	// helperVersion prints USAGE_VERSION and a newline.
	pad := func(n int) string { return version + strings.Repeat(" ", n-len(version)-1) }
	if rep := claudeRead(t, newStub(t, "claude", "USAGE_VERSION="+pad(versionMaxOutput)), &fakeAdmit{}); rep.State != UsageOK {
		t.Errorf("a version print of %d bytes: %+v", versionMaxOutput, rep)
	}
	if rep := claudeRead(t, newStub(t, "claude", "USAGE_VERSION="+pad(versionMaxOutput+1)), &fakeAdmit{}); rep.State != UsageUnqualified || !strings.Contains(rep.Words, "more than 64 KiB") {
		t.Errorf("a version print of %d bytes: %+v", versionMaxOutput+1, rep)
	}
	auth := `{"loggedIn": true, "authMethod": "claude.ai", "apiProvider": "firstParty", "subscriptionType": "max"}`
	padAuth := func(n int) string {
		return auth + strings.Repeat("\r\n", (n-len(auth))/2) + strings.Repeat(" ", (n-len(auth))%2)
	}
	if rep := claudeRead(t, newStub(t, "claude", "USAGE_AUTH="+authFile(t, padAuth(claudeAuthMax))), &fakeAdmit{}); rep.State != UsageOK {
		t.Errorf("an auth status of %d bytes: %+v", claudeAuthMax, rep)
	}
	if rep := claudeRead(t, newStub(t, "claude", "USAGE_AUTH="+authFile(t, padAuth(claudeAuthMax+1))), &fakeAdmit{}); rep.State != UsageError || !strings.Contains(rep.Words, "more than 64 KiB") {
		t.Errorf("an auth status of %d bytes: %+v", claudeAuthMax+1, rep)
	}
}
