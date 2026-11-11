package tracker

import (
	"strings"
	"unicode"
)

// MaxRepro is the bound on the steps to reproduce Repro gives.
const MaxRepro = 8 << 10

// reproHeadings are the headings, lower case and trimmed of punctuation,
// that open an item's steps to reproduce (GitHub's issue forms make such a
// heading of a form field's label).
var reproHeadings = map[string]bool{
	"steps to reproduce": true,
	"reproduction steps": true,
	"repro steps":        true,
	"how to reproduce":   true,
	"to reproduce":       true,
	"reproduction":       true,
	"repro":              true,
}

// reproCut is what ends steps cut at MaxRepro.
const reproCut = "\n…"

// Repro finds the steps to reproduce in an item's Markdown: the first ATX
// heading of level 1 to 4, outside a fenced code block, whose text is one
// of reproHeadings, and its text up to the next heading of the same or a
// higher level, trimmed, at most MaxRepro bytes (cut at a line end, the
// cut said with "…"). None found, or found empty, is "", false.
func Repro(body string) (string, bool) {
	lines := strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n")
	start, level := -1, 0
	fence := ""
	var out []string
	for i, line := range lines {
		if f := fenceOf(line); f != "" {
			switch {
			case fence == "":
				fence = f
			case strings.HasPrefix(strings.TrimSpace(line), fence) && strings.TrimLeft(strings.TrimSpace(line), fence[:1]) == "":
				fence = ""
			}
			if start >= 0 {
				out = append(out, line)
			}
			continue
		}
		if fence == "" {
			if l, text, ok := atxHeading(line); ok {
				if start >= 0 && l <= level {
					break
				}
				if start < 0 && l <= 4 && reproHeadings[headingKey(text)] {
					start, level = i, l
					continue
				}
			}
		}
		if start >= 0 {
			out = append(out, line)
		}
	}
	if start < 0 {
		return "", false
	}
	steps := strings.TrimSpace(strings.Join(out, "\n"))
	if steps == "" {
		return "", false
	}
	if len(steps) > MaxRepro {
		room := MaxRepro - len(reproCut)
		cut := strings.LastIndexByte(steps[:room+1], '\n')
		if cut <= 0 {
			cut = len(cutBytes(steps, room))
		}
		steps = strings.TrimRight(steps[:cut], " \t\n") + reproCut
	}
	return steps, true
}

// fenceOf gives the fence a line opens or closes (``` or ~~~, three or
// more), or "".
func fenceOf(line string) string {
	t := strings.TrimLeft(line, " ")
	if len(line)-len(t) > 3 {
		return ""
	}
	for _, c := range []string{"`", "~"} {
		n := len(t) - len(strings.TrimLeft(t, c))
		if n >= 3 {
			return strings.Repeat(c, n)
		}
	}
	return ""
}

// atxHeading reads an ATX heading: at most three spaces, one to six #, a
// space or the line's end, the text, an optional closing run of #.
func atxHeading(line string) (int, string, bool) {
	t := strings.TrimLeft(line, " ")
	if len(line)-len(t) > 3 {
		return 0, "", false
	}
	n := len(t) - len(strings.TrimLeft(t, "#"))
	if n < 1 || n > 6 {
		return 0, "", false
	}
	rest := t[n:]
	if rest != "" && rest[0] != ' ' && rest[0] != '\t' {
		return 0, "", false
	}
	rest = strings.TrimSpace(rest)
	if trimmed := strings.TrimRight(rest, "#"); trimmed != rest && (trimmed == "" || strings.HasSuffix(trimmed, " ") || strings.HasSuffix(trimmed, "\t")) {
		rest = strings.TrimSpace(trimmed)
	}
	return n, rest, true
}

// headingKey is a heading's text lower case, trimmed of punctuation and
// symbols, its white space made single spaces.
func headingKey(text string) string {
	text = strings.TrimFunc(strings.ToLower(text), func(r rune) bool {
		return unicode.IsPunct(r) || unicode.IsSymbol(r) || unicode.IsSpace(r)
	})
	return strings.Join(strings.Fields(text), " ")
}
