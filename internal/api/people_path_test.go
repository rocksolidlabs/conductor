package api

import (
	"encoding/json"
	"math/rand/v2"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

	"github.com/rocksolidlabs/conductor/internal/crew"
)

// segmentCase is a name and its segment in testdata/person-segments.json,
// the cases the Go rule and the app's (web/app/utils/people.ts) are both
// tested against, and the embedded app's handler too (internal/web).
type segmentCase struct {
	Name    string `json:"name"`
	Segment string `json:"segment"`
}

func segmentCases(t *testing.T) []segmentCase {
	t.Helper()
	b, err := os.ReadFile("testdata/person-segments.json")
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Cases []segmentCase `json:"cases"`
	}
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	return f.Cases
}

// nameRunes are what randomName builds names of: every kind of character
// the rule treats apart (a plain one, a path word's, an escape's, one past
// ASCII, a slash past ASCII), so the names cover the class.
var nameRunes = []rune("aZ09-_. ~/\\%?#;+'&=:@!$,()[]*\u00e9\u00df\u65e5\U0001f600\u00a0\u2215\uff0f\u202e\ufeff")

// randomName is a name the people rules allow: 1 to 60 characters of
// valid UTF-8 with no control character, now and then "..", "%2F" or
// "%2E" spelled in it.
func randomName(r *rand.Rand) string {
	n := 1 + r.IntN(crew.MaxPersonName)
	var b strings.Builder
	for count := 0; count < n; {
		switch r.IntN(12) {
		case 0:
			if count+2 <= n {
				b.WriteString("..")
				count += 2
				continue
			}
		case 1:
			if count+3 <= n {
				b.WriteString([]string{"%2F", "%2E", "%25"}[r.IntN(3)])
				count += 3
				continue
			}
		}
		b.WriteRune(nameRunes[r.IntN(len(nameRunes))])
		count++
	}
	return b.String()
}

// allowedName is the people rules (crew.checkPersonName, Dispatch's
// person.name).
func allowedName(name string) bool {
	return name != "" && utf8.ValidString(name) && utf8.RuneCountInString(name) <= crew.MaxPersonName && !strings.ContainsFunc(name, unicode.IsControl)
}

// proxied is the path a proxy that normalises sends on (nginx with a
// path in proxy_pass, say): decoded, its dot steps and doubled slashes
// resolved, escaped again.
func proxied(p string) string {
	dec, err := url.PathUnescape(p)
	if err != nil {
		return p
	}
	clean := path.Clean(dec)
	if strings.HasSuffix(dec, "/") && clean != "/" {
		clean += "/"
	}
	return (&url.URL{Path: clean}).EscapedPath()
}

// checkSegment holds the rule's promises for one name the people rules
// allow: its segment reads back as it, in the one form written; it is
// one segment that no decoding, second decoding, cleaning or proxy's
// normalising changes; and it is the testdata's segment when given.
func checkSegment(t *testing.T, name, want string) {
	t.Helper()
	seg := personSegment(name)
	if want != "" && seg != want {
		t.Fatalf("%q: segment %q, want %q", name, seg, want)
	}
	for _, c := range []byte(seg) {
		if !('a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9' || strings.IndexByte("-_.~%", c) >= 0) {
			t.Fatalf("%q: segment %q holds %q", name, seg, c)
		}
	}
	text, err := url.PathUnescape(seg)
	if err != nil {
		t.Fatalf("%q: segment %q: %v", name, seg, err)
	}
	if strings.ContainsAny(text, "/\\%?#;") || text == "." || text == ".." {
		t.Fatalf("%q: segment %q unescaped is %q, which a path reads as more than a name", name, seg, text)
	}
	if again, _ := url.PathUnescape(text); again != text {
		t.Fatalf("%q: segment %q decoded twice is %q", name, seg, again)
	}
	if p := "/people/" + seg; path.Clean(p) != p || proxied(p) != p || proxied("/api/people/"+seg) != "/api/people/"+seg {
		t.Fatalf("%q: %s cleaned %s, proxied %s", name, p, path.Clean(p), proxied(p))
	}
	got, ok := personFromSegment(text)
	if !ok || got != name {
		t.Fatalf("%q: segment %q reads back as %q %v", name, seg, got, ok)
	}
}

// A person's name in a path (personSegment): the testdata's names, the
// path words among them (".", "..", "~", "~.", "~~", a slash, a percent
// sign, a space, a letter past ASCII, the longest names), and 20,000
// names drawn from the whole class the people rules allow, each holding
// the rule's promises; and no segment is read as a name unless it is the
// one that name is written as, so no address is another name's.
func TestPersonSegmentRule(t *testing.T) {
	cases := segmentCases(t)
	for _, c := range cases {
		if !allowedName(c.Name) {
			t.Fatalf("the testdata's name %q is not one the people rules allow", c.Name)
		}
		checkSegment(t, c.Name, c.Segment)
	}
	r := rand.New(rand.NewPCG(644, 2))
	for range 20000 {
		name := randomName(r)
		if !allowedName(name) {
			t.Fatalf("randomName made %q", name)
		}
		checkSegment(t, name, "")
	}
	// The other way: a segment read as a name is the one written for it.
	reads := append([]string{"", "~", "~~", "~.", "~..", "~zed", "~bmF0ZQ", "~Lg\n", "~Lg==", "~Lh", "a/b", "50% c", ".", "..", " x", "x ", "~fn4"}, "~"+strings.Repeat("A", 10))
	for range 20000 {
		reads = append(reads, randomName(r), "~"+randomName(r))
	}
	for _, text := range reads {
		if name, ok := personFromSegment(text); ok {
			if back, _ := url.PathUnescape(personSegment(name)); back != text {
				t.Fatalf("%q reads as %q, whose segment is %q", text, name, back)
			}
		}
	}
}

// Every name of the class has its page through the route: the testdata's
// names and 40 drawn ones, each in Dispatch's directory, are answered by
// their segment, asked as written and as a normalising proxy sends it on;
// no other form of a name is anyone's (the dot words and the slash as an
// earlier rule wrote them, a token written for a plain name, the plain
// form of a coded one), so none reaches another route or person.
func TestAPersonsPath(t *testing.T) {
	e := newTestEnv(t, nil)
	names := map[string]bool{}
	for _, c := range segmentCases(t) {
		names[c.Name] = true
	}
	r := rand.New(rand.NewPCG(644, 3))
	for len(names) < len(segmentCases(t))+40 {
		names[randomName(r)] = true
	}
	for name := range names {
		e.expect(t, "POST", "/api/dispatch/people", map[string]any{"opId": opID(), "name": name}, http.StatusCreated, "")
	}
	e.expect(t, "POST", "/api/dispatch/people", map[string]any{"opId": opID(), "name": "zed"}, http.StatusCreated, "")
	for name := range names {
		p := "/api/people/" + personSegment(name)
		for _, asked := range []string{p, proxied(p)} {
			out := e.expect(t, "GET", asked, nil, http.StatusOK, "")
			if out["name"] != name || out["dispatch"] != true {
				t.Fatalf("%q at %s: %v", name, asked, out)
			}
		}
	}
	for _, p := range []string{"/api/people/~..", "/api/people/~.", "/api/people/a%2Fb", "/api/people/~zed", "/api/people/~emVk", "/api/people/~~x", "/api/people/~", "/api/people/YS9i", "/api/people/x"} {
		e.expect(t, "GET", p, nil, http.StatusNotFound, "not_found")
	}
}
