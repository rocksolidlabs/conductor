package proto

import (
	"strings"
	"testing"
)

// A permission_answer is read strictly, within its bounds: a question of 1
// to 32 printable bytes, a call of at most 128, a digest of 64 lowercase hex
// digits, a choice of the closed set, a message of at most 500 bytes with a
// deny alone.
func TestPermissionAnswerFrameBounds(t *testing.T) {
	sha := strings.Repeat("ab", 32)
	frame := func(question, call, digest, choice, message string) string {
		s := `{"t":"permission_answer","reqId":"r1","question":"` + question + `","call":"` + call + `","sha256":"` + digest + `","choice":"` + choice + `"`
		if message != "" {
			s += `,"message":"` + message + `"`
		}
		return s + `}`
	}
	q32, call128, msg500 := strings.Repeat("q", 32), strings.Repeat("c", 128), strings.Repeat("m", 500)
	ok := []string{
		frame("q", "toolu_1", sha, PermissionAllowOnce, ""),
		frame(q32, call128, sha, PermissionDeny, msg500),
		frame("q", "", sha, PermissionDeny, ""),
		frame("q", "c", sha, PermissionDeny, "Use the other file, please."),
	}
	for _, s := range ok {
		if _, err := DecodePermissionAnswer([]byte(s)); err != nil {
			t.Errorf("%s: %v", s, err)
		}
	}
	bad := map[string]string{
		"no question":           frame("", "c", sha, PermissionDeny, ""),
		"a question past 32":    frame(q32+"q", "c", sha, PermissionDeny, ""),
		"a question with space": frame("a b", "c", sha, PermissionDeny, ""),
		"a call past 128":       frame("q", call128+"c", sha, PermissionDeny, ""),
		"a call with a control": frame("q", `c\u0001`, sha, PermissionDeny, ""),
		"a digest of 63":        frame("q", "c", sha[1:], PermissionDeny, ""),
		"a digest of 65":        frame("q", "c", sha+"a", PermissionDeny, ""),
		"an uppercase digest":   frame("q", "c", strings.ToUpper(sha), PermissionDeny, ""),
		"a digest not hex":      frame("q", "c", strings.Repeat("zz", 32), PermissionDeny, ""),
		"allow always":          frame("q", "c", sha, "allow_always", ""),
		"no choice":             frame("q", "c", sha, "", ""),
		"a message past 500":    frame("q", "c", sha, PermissionDeny, msg500+"m"),
		"a message with allow":  frame("q", "c", sha, PermissionAllowOnce, "go"),
		"a message's control":   frame("q", "c", sha, PermissionDeny, `a\u001bb`),
		"no reqId":              `{"t":"permission_answer","question":"q","call":"c","sha256":"` + sha + `","choice":"deny"}`,
		"a field unknown":       `{"t":"permission_answer","reqId":"r","question":"q","call":"c","sha256":"` + sha + `","choice":"deny","always":true}`,
		"another type":          `{"t":"answer","reqId":"r","question":"q","call":"c","sha256":"` + sha + `","choice":"deny"}`,
		"more after it":         frame("q", "c", sha, PermissionDeny, "") + `{}`,
	}
	for name, s := range bad {
		if _, err := DecodePermissionAnswer([]byte(s)); err == nil {
			t.Errorf("%s was taken: %s", name, s)
		}
	}
	// The largest answer fits a control frame.
	if n := len(frame(q32, call128, sha, PermissionDeny, msg500)); n > MaxControl {
		t.Errorf("the largest permission_answer is %d bytes, past MaxControl", n)
	}
}
