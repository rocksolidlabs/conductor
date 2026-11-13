package ext

import (
	"context"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// SignIn is what an identity provider implements when a person can sign
// this server in from Settings. Every call answers from what the provider
// holds, never with a round trip to its service on the call's path:
// BeginSignIn asks the provider to start and returns; the provider's own
// goroutines (Start's) do the work, and SignInStatus shows it. The server
// asks each through its guard, within IdentityWait, and checks every
// status it is answered (SignInStatus.Valid) before it shows it.
type SignIn interface {
	SignInStatus() SignInStatus
	BeginSignIn(ctx context.Context) error
	SignOut(ctx context.Context) error
}

// SignInState is where a sign-in stands.
type SignInState string

const (
	SignedOut      SignInState = "signed-out"
	SignInStarting SignInState = "starting" // begun; no code to show yet
	SignInCode     SignInState = "code"     // a code to enter on the provider's page
	SignedIn       SignInState = "signed-in"
)

// SignInStatus is what a sign-in shows: the state, and what that state
// carries. Code and URL are the provider's page and the code a person
// enters there; Who is who the server is signed in as; Why is one line the
// provider says of the state (a lapse, a refusal), in any state.
type SignInStatus struct {
	State   SignInState `json:"state"`
	Who     string      `json:"who,omitempty"`    // signed-in: one line of 1 to MaxSignInWho bytes
	Code    string      `json:"code,omitempty"`   // code: 1 to MaxSignInCode of A-Z a-z 0-9 '-'
	URL     string      `json:"url,omitempty"`    // code: an https URL with a host name, at most MaxSignInURL bytes (signInURL)
	Expires time.Time   `json:"expires,omitzero"` // code: when the code ends, if the provider says
	Why     string      `json:"why,omitempty"`    // any state: one line of at most MaxSignInWhy bytes
}

const (
	MaxSignInWho  = 80  // bytes
	MaxSignInCode = 16  // bytes
	MaxSignInURL  = 200 // bytes
	MaxSignInWhy  = 120 // bytes
)

// Valid checks s: a state this repository knows; Who set in signed-in
// alone; Code and URL set in code alone, and both set there; Expires in
// code alone, a time JSON can carry; each field within its bound. Its
// refusal names the field and the rule, never the field's value.
func (s SignInStatus) Valid() error {
	const r = "sign-in status"
	switch s.State {
	case SignedOut, SignInStarting, SignInCode, SignedIn:
	default:
		return invalid(r, "state is not signed-out, starting, code or signed-in")
	}
	if err := signInLine(r, "who", s.Who, MaxSignInWho, s.State == SignedIn); err != nil {
		return err
	}
	if err := signInLine(r, "why", s.Why, MaxSignInWhy, false); err != nil {
		return err
	}
	if s.State != SignedIn && s.Who != "" {
		return invalid(r, "who is set outside signed-in")
	}
	if s.State != SignInCode {
		if s.Code != "" || s.URL != "" || !s.Expires.IsZero() {
			return invalid(r, "code, url or expires is set outside code")
		}
		return nil
	}
	if !signInCode(s.Code) {
		return invalid(r, "code is not 1 to %d of A-Z, a-z, 0-9 and '-'", MaxSignInCode)
	}
	if wrong := signInURL(s.URL); wrong != "" {
		return invalid(r, "url %s", wrong)
	}
	if _, off := s.Expires.Zone(); off <= -24*3600 || off >= 24*3600 {
		return invalid(r, "expires has a zone offset of a day or more")
	}
	if y := s.Expires.UTC().Year(); !s.Expires.IsZero() && (y < 1 || y > 9999) {
		return invalid(r, "expires is outside the years 1 to 9999 in UTC")
	}
	return nil
}

// signInLine is line, refusing too the format characters and the line and
// paragraph separators (Unicode's Cf, Zl and Zp), which would let one line
// draw as another or as two. The sign-in's own: ext's line serves other
// records unchanged.
func signInLine(record, field, s string, max int, required bool) error {
	if err := line(record, field, s, max, required); err != nil {
		return err
	}
	if strings.ContainsFunc(s, func(c rune) bool { return unicode.In(c, unicode.Cf, unicode.Zl, unicode.Zp) }) {
		return invalid(record, "%s holds a format character or a line or paragraph separator", field)
	}
	return nil
}

// signInCode reports whether c is 1 to MaxSignInCode of A-Z a-z 0-9 '-'.
func signInCode(c string) bool {
	if c == "" || len(c) > MaxSignInCode {
		return false
	}
	for i := 0; i < len(c); i++ {
		switch b := c[i]; {
		case 'a' <= b && b <= 'z', 'A' <= b && b <= 'Z', '0' <= b && b <= '9', b == '-':
		default:
			return false
		}
	}
	return true
}

// signInURL says what is wrong with u as a sign-in's page, "" for
// nothing, in words that never quote it: it is 1 to MaxSignInURL bytes of
// printable ASCII with no space, quote (" ' `), backslash or angle bracket
// and no '%' but before two hex digits, an https URL with a host name (not
// dots alone), a port of 1 to 65535 if it has one, and no user
// information. The page's own check (useSignIn's signInPage) is this rule.
func signInURL(u string) string {
	switch {
	case u == "":
		return "is empty"
	case len(u) > MaxSignInURL:
		return "is past its bound"
	case !printableASCII(u) || strings.ContainsAny(u, "\"'`\\<>"):
		return "is not printable ASCII without spaces, quotes or brackets"
	case !percentEscapes(u):
		return "has a malformed percent-escape"
	}
	p, err := url.Parse(u)
	switch {
	case err != nil:
		return "does not parse"
	case p.Scheme != "https":
		return "is not https"
	case strings.Trim(p.Hostname(), ".") == "" || p.Opaque != "":
		return "has no host name"
	case p.Port() != "" && !port(p.Port()):
		return "has a port outside 1 to 65535"
	case p.User != nil:
		return "carries user information"
	}
	return ""
}

// percentEscapes reports whether every '%' in u begins two hex digits.
func percentEscapes(u string) bool {
	hex := func(c byte) bool { return '0' <= c && c <= '9' || 'a' <= c && c <= 'f' || 'A' <= c && c <= 'F' }
	for i := 0; i < len(u); i++ {
		if u[i] == '%' && (i+2 >= len(u) || !hex(u[i+1]) || !hex(u[i+2])) {
			return false
		}
	}
	return true
}

// port reports whether p is a TCP port of 1 to 65535.
func port(p string) bool {
	n, err := strconv.Atoi(p)
	return err == nil && n >= 1 && n <= 65535
}
