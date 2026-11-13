package ext_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/ext"
)

// TestSignInStatusValid checks each state with what it may carry, each
// bound at its edge and past it, and that a refusal never quotes the
// field it refuses.
func TestSignInStatusValid(t *testing.T) {
	long := func(n int) string { return strings.Repeat("a", n) }
	page := "https://id.example.com/device"
	code := func(c, u string) ext.SignInStatus { return ext.SignInStatus{State: ext.SignInCode, Code: c, URL: u} }
	ends := func(z *time.Location) ext.SignInStatus {
		return ext.SignInStatus{State: ext.SignInCode, Code: "ABCD", URL: page, Expires: time.Date(2026, 11, 12, 9, 0, 0, 0, z)}
	}
	cases := []struct {
		name string
		s    ext.SignInStatus
		ok   bool
	}{
		{"signed out", ext.SignInStatus{State: ext.SignedOut}, true},
		{"signed out with a why", ext.SignInStatus{State: ext.SignedOut, Why: "the workspace lapsed"}, true},
		{"starting", ext.SignInStatus{State: ext.SignInStarting}, true},
		{"a code", code("WDJB-MJHT", page), true},
		{"a code with its end", ext.SignInStatus{State: ext.SignInCode, Code: "WDJBMJHT", URL: page, Expires: time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC)}, true},
		{"signed in", ext.SignInStatus{State: ext.SignedIn, Who: "Ana · workspace Acme"}, true},
		{"who at its bound", ext.SignInStatus{State: ext.SignedIn, Who: long(ext.MaxSignInWho)}, true},
		{"why at its bound", ext.SignInStatus{State: ext.SignedOut, Why: long(ext.MaxSignInWhy)}, true},
		{"code at its bound", code(long(ext.MaxSignInCode), page), true},
		{"url at its bound", code("ABCD", "https://e.example/"+long(ext.MaxSignInURL-len("https://e.example/"))), true},

		{"no state", ext.SignInStatus{}, false},
		{"an unknown state", ext.SignInStatus{State: "pending"}, false},
		{"signed in with no who", ext.SignInStatus{State: ext.SignedIn}, false},
		{"who past its bound", ext.SignInStatus{State: ext.SignedIn, Who: long(ext.MaxSignInWho + 1)}, false},
		{"who of two lines", ext.SignInStatus{State: ext.SignedIn, Who: "Ana\nAcme"}, false},
		{"who not UTF-8", ext.SignInStatus{State: ext.SignedIn, Who: "Ana\xff"}, false},
		{"who outside signed-in", ext.SignInStatus{State: ext.SignedOut, Who: "Ana"}, false},
		{"why past its bound", ext.SignInStatus{State: ext.SignedOut, Why: long(ext.MaxSignInWhy + 1)}, false},
		{"why with an escape", ext.SignInStatus{State: ext.SignedOut, Why: "a\x1b[2Jb"}, false},
		{"a code outside code", ext.SignInStatus{State: ext.SignedOut, Code: "ABCD"}, false},
		{"a url outside code", ext.SignInStatus{State: ext.SignedIn, Who: "Ana", URL: page}, false},
		{"an end outside code", ext.SignInStatus{State: ext.SignInStarting, Expires: time.Now()}, false},
		{"code with no code", code("", page), false},
		{"code with no url", code("ABCD", ""), false},
		{"code past its bound", code(long(ext.MaxSignInCode+1), page), false},
		{"code with a space", code("ABCD EFGH", page), false},
		{"code not ASCII", code("ÄBCD", page), false},
		{"url past its bound", code("ABCD", "https://e.example/"+long(ext.MaxSignInURL)), false},
		{"url over http", code("ABCD", "http://id.example.com/device"), false},
		{"url of a script", code("ABCD", "javascript:alert(1)"), false},
		{"url with no host", code("ABCD", "https:///device"), false},
		{"url opaque", code("ABCD", "https:id.example.com"), false},
		{"url with user information", code("ABCD", "https://ana:pw@id.example.com/"), false},
		{"url with a space", code("ABCD", "https://id.example.com/a b"), false},
		{"url with a quote", code("ABCD", `https://id.example.com/"x`), false},
		{"url with a bracket", code("ABCD", "https://id.example.com/<x>"), false},
		{"url relative", code("ABCD", "/device"), false},
		{"an end past the year 9999", ext.SignInStatus{State: ext.SignInCode, Code: "ABCD", URL: page, Expires: time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)}, false},
		{"an end in a zone of +14 h", ends(time.FixedZone("", 14*3600)), true},
		{"an end in a zone of +24 h", ends(time.FixedZone("", 24*3600)), false},
		{"an end in a zone of -24 h", ends(time.FixedZone("", -24*3600)), false},
		{"an end in 9999 that is 10000 in UTC", ext.SignInStatus{State: ext.SignInCode, Code: "ABCD", URL: page, Expires: time.Date(9999, 12, 31, 23, 0, 0, 0, time.FixedZone("", -2*3600))}, false},
		{"url with a port", code("ABCD", "https://id.example.com:8443/device"), true},
		{"url with a percent-escape", code("ABCD", "https://id.example.com/a%2Fb?c=%20"), true},
		{"url with a port alone", code("ABCD", "https://:443/x"), false},
		{"url with a bare dot", code("ABCD", "https://./"), false},
		{"url with dots alone", code("ABCD", "https://../"), false},
		{"url with a port past 65535", code("ABCD", "https://id.example.com:99999/d"), false},
		{"url with port 0", code("ABCD", "https://id.example.com:0/d"), false},
		{"url with a malformed escape", code("ABCD", "https://id.example.com/d?x=%zz"), false},
		{"url with a cut escape", code("ABCD", "https://id.example.com/d%2"), false},
		{"url with an apostrophe", code("ABCD", "https://id.example.com/a'b"), false},
		{"url with a backtick", code("ABCD", "https://id.example.com/a`b"), false},
		// Format characters and the separators are taken: the server shows
		// who and why without them.
		{"who with a right-to-left override", ext.SignInStatus{State: ext.SignedIn, Who: "Ana " + string(rune(0x202e)) + "gnp.exe"}, true},
		{"who with a joined emoji", ext.SignInStatus{State: ext.SignedIn, Who: "Ana " + string([]rune{0x1f469, 0x200d, 0x1f4bb})}, true},
		{"who with a soft hyphen", ext.SignInStatus{State: ext.SignedIn, Who: "Ana" + string(rune(0xad)) + "stasia"}, true},
		{"who with a line separator", ext.SignInStatus{State: ext.SignedIn, Who: "Ana" + string(rune(0x2028)) + "Eve"}, true},
		{"why with a paragraph separator", ext.SignInStatus{State: ext.SignedOut, Why: "a" + string(rune(0x2029)) + "b"}, true},
		{"why with a C1 control", ext.SignInStatus{State: ext.SignedOut, Why: "a\u0085b"}, false},

		// The page's address: a host name, a path, a port without leading
		// zeros that is not 443, no fragment.
		{"url with a dotted address", code("ABCD", "https://999.1.1.1/device"), false},
		{"url with a zoned address", code("ABCD", "https://[fe80::1%25eth0]/device"), false},
		{"url with a bracketed address", code("ABCD", "https://[::1]/x"), false},
		{"url whose last label begins with a digit", code("ABCD", "https://a.0x7f/x"), false},
		{"url with capitals in its host", code("ABCD", "https://ID.example/x"), false},
		{"url with a trailing dot", code("ABCD", "https://id.example./x"), false},
		{"url with an escape in its host", code("ABCD", "https://ex%25ample.test/d"), false},
		{"url with no path", code("ABCD", "https://id.example"), false},
		{"url with port 443", code("ABCD", "https://id.example:443/x"), false},
		{"url with a port with a leading zero", code("ABCD", "https://id.example:0443/x"), false},
		{"url with a fragment", code("ABCD", "https://id.example/x#token=abc"), false},
		{"url with a short fragment", code("ABCD", "https://id.example/x#a"), false},
		{"url with an empty authority", code("ABCD", "https:///id.example/device"), false},
		{"url with an empty port", code("ABCD", "https://id.example:/x"), false},
		{"url with its scheme in capitals", code("ABCD", "HTTPS://id.example/x"), false},
		{"url with its scheme capitalised", code("ABCD", "Https://id.example/x"), false},
		// Taken, though a browser writes them otherwise: the page draws no link.
		{"url with dot segments", code("ABCD", "https://id.example/a/../b"), true},
		{"url with escaped dot segments", code("ABCD", "https://id.example/%2e%2e/b"), true},
		{"url with a query", code("ABCD", "https://id.example/device?user_code=ABCD-EFGH"), true},
		{"url with port 8443", code("ABCD", "https://id.example:8443/x"), true},
	}
	for _, c := range cases {
		err := c.s.Valid()
		if (err == nil) != c.ok {
			t.Errorf("%s: Valid() = %v, want ok %v", c.name, err, c.ok)
			continue
		}
		if err == nil {
			continue
		}
		for _, v := range []string{c.s.Who, c.s.Code, c.s.URL, c.s.Why} {
			if len(v) > 3 && strings.Contains(err.Error(), v) {
				t.Errorf("%s: the refusal %q quotes what it refuses", c.name, err)
			}
		}
	}
}

// A status is carried by JSON as it is, and a state's absent fields are
// left out, the end of a code among them.
func TestSignInStatusJSON(t *testing.T) {
	for _, c := range []struct {
		s    ext.SignInStatus
		want string
	}{
		{ext.SignInStatus{State: ext.SignedOut}, `{"state":"signed-out"}`},
		{ext.SignInStatus{State: ext.SignedIn, Who: "Ana"}, `{"state":"signed-in","who":"Ana"}`},
		{ext.SignInStatus{State: ext.SignInCode, Code: "ABCD", URL: "https://e.example/d"}, `{"state":"code","code":"ABCD","url":"https://e.example/d"}`},
	} {
		got, err := json.Marshal(c.s)
		if err != nil || string(got) != c.want {
			t.Errorf("%+v encodes as %s, %v; want %s", c.s, got, err, c.want)
		}
	}
}
