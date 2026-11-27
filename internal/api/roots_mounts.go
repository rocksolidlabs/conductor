package api

import (
	"strconv"
	"strings"
)

// parseMountinfo is the mount points mountinfo lists (its fifth field), the
// kernel's escapes (\040 for a space, \011, \012, \134) undone.
func parseMountinfo(text string) []string {
	var out []string
	for _, line := range strings.Split(text, "\n") {
		f := strings.Fields(line)
		if len(f) < 5 {
			continue
		}
		out = append(out, unescapeMount(f[4]))
	}
	return out
}

// unescapeMount undoes mountinfo's octal escapes.
func unescapeMount(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) {
			if n, err := strconv.ParseUint(s[i+1:i+4], 8, 8); err == nil {
				b.WriteByte(byte(n))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}
