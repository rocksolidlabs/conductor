package tracker

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

// fakeAdapter is a second system whose accounts and comments carry UUIDs.
type fakeAdapter struct {
	name     string
	account  Account
	comments []struct {
		c    Comment
		body string
	}
}

func (f *fakeAdapter) System() string { return f.name }
func (f *fakeAdapter) Parse(string, string) (Ref, error) {
	return Ref{}, ErrUnsupported
}
func (f *fakeAdapter) Whoami(context.Context, Credential) (Account, error) { return f.account, nil }
func (f *fakeAdapter) Read(context.Context, Credential, Ref, Validators) (Item, bool, error) {
	return Item{}, false, ErrUnsupported
}
func (f *fakeAdapter) Image(context.Context, Credential, ImageRef, int64) (io.ReadCloser, string, error) {
	return nil, "", ErrUnsupported
}
func (f *fakeAdapter) Checks(context.Context, Credential, Ref, string) (CheckSet, error) {
	return CheckSet{}, ErrUnsupported
}
func (f *fakeAdapter) CheckLog(context.Context, Credential, Ref, Check, int) (LogTail, error) {
	return LogTail{}, ErrUnsupported
}
func (f *fakeAdapter) Resolve(context.Context, Credential, string) (Target, error) {
	return Target{}, ErrUnsupported
}
func (f *fakeAdapter) Comment(context.Context, Credential, Target, string) (Comment, error) {
	return Comment{}, ErrUnsupported
}
func (f *fakeAdapter) FindComment(_ context.Context, _ Credential, _ Target, _ time.Time, m Match) (Comment, Found, error) {
	if err := m.Check(); err != nil {
		return Comment{}, FoundUnknown, err
	}
	for _, c := range f.comments {
		if m.Matches(c.body, c.c.AuthorID) {
			return c.c, FoundYes, nil
		}
	}
	return Comment{}, FoundNo, nil
}
func (f *fakeAdapter) Close(context.Context, Credential, Target, string) error { return ErrUnsupported }
func (f *fakeAdapter) Marker(id string) string                                 { return "[conductor:" + id + "]" }

func TestSystemNames(t *testing.T) {
	ok := Adapters{"github": &fakeAdapter{name: "github"}, "linear-2": &fakeAdapter{name: "linear-2"}}
	got, err := ok.Systems()
	if err != nil || strings.Join(got, ",") != "github,linear-2" {
		t.Fatalf("Systems = %v, %v", got, err)
	}
	for _, name := range []string{"native", "dispatch", "GitHub", "abcdefghijklmnopq", "", "1st", "-x", "a_b", "a b"} {
		if _, err := (Adapters{name: &fakeAdapter{name: name}}).Systems(); err == nil {
			t.Errorf("Systems admitted %q", name)
		}
	}
	if _, err := (Adapters{"abcdefghijklmnop": &fakeAdapter{name: "abcdefghijklmnop"}}).Systems(); err != nil {
		t.Errorf("16 bytes refused: %v", err)
	}
	if _, err := (Adapters{"github": &fakeAdapter{name: "gitlab"}}).Systems(); err == nil {
		t.Error("a key that is not its adapter's System admitted")
	}
	if _, err := (Adapters{"github": nil}).Systems(); err == nil {
		t.Error("a nil adapter admitted")
	}
}

func TestRemoteIDOpaque(t *testing.T) {
	me := RemoteID("6f1c2e9a-4b7d-4a52-9c3e-0f1d2a3b4c5d")
	other := RemoteID("0a1b2c3d-4e5f-4a6b-8c7d-9e0f1a2b3c4d")
	f := &fakeAdapter{name: "linear", account: Account{Login: "mona", ID: me}}
	body := "Done.\n\n" + f.Marker("pub1")
	mine := Comment{ID: "c0ffee00-0000-4000-8000-000000000001", URL: "https://linear.example/c/1", AuthorID: me, Author: "mona"}
	theirs := Comment{ID: "c0ffee00-0000-4000-8000-000000000002", AuthorID: other, Author: "hubot"}
	f.comments = append(f.comments, struct {
		c    Comment
		body string
	}{theirs, body}, struct {
		c    Comment
		body string
	}{mine, body + "\r\n  "})

	acct, _ := f.Whoami(context.Background(), Credential{})
	if acct.ID != me || acct.Check() != nil {
		t.Fatalf("account %+v", acct)
	}
	m := Match{Marker: f.Marker("pub1"), BodySha256: BodyDigest(body), AuthorID: acct.ID}
	got, found, err := f.FindComment(context.Background(), Credential{}, Target{}, time.Time{}, m)
	if err != nil || found != FoundYes || got != mine || got.Check() != nil {
		t.Fatalf("found %+v %v %v", got, found, err)
	}
	m.AuthorID = ""
	if got, _, _ := f.FindComment(context.Background(), Credential{}, Target{}, time.Time{}, m); got != theirs {
		t.Fatalf("any author: %+v", got)
	}
	for _, bad := range []RemoteID{"", RemoteID(strings.Repeat("a", 65)), "has space", "tab\t", "é"} {
		if CheckRemoteID(bad) == nil {
			t.Errorf("CheckRemoteID admitted %q", bad)
		}
		if (Comment{ID: bad, AuthorID: me}).Check() == nil || (Comment{ID: me, AuthorID: bad}).Check() == nil || (Account{ID: bad}).Check() == nil {
			t.Errorf("a record admitted %q", bad)
		}
	}
	if CheckRemoteID(RemoteID(strings.Repeat("a", 64))) != nil {
		t.Error("64 bytes refused")
	}
	if (Match{Marker: "m", BodySha256: BodyDigest("x"), AuthorID: RemoteID(strings.Repeat("a", 65))}).Check() == nil {
		t.Error("a match with a long author id admitted")
	}
}

func TestMatch(t *testing.T) {
	marker := "<!-- conductor-publication:p1 -->"
	body := "text\n\n" + marker
	m := Match{Marker: marker, BodySha256: BodyDigest(body)}
	if !m.Matches(body, "1") || !m.Matches(strings.ReplaceAll(body, "\n", "\r\n")+" \n", "1") {
		t.Fatal("the normalised body did not match")
	}
	if m.Matches("other "+body, "1") || m.Matches("text\n\n<!-- conductor-publication:p2 -->", "1") {
		t.Fatal("other bytes beside the marker matched")
	}
	if (Match{BodySha256: m.BodySha256}).Matches(body, "1") {
		t.Fatal("an empty marker matched")
	}
	for _, bad := range []Match{
		{Marker: "", BodySha256: m.BodySha256},
		{Marker: strings.Repeat("m", MaxMarker+1), BodySha256: m.BodySha256},
		{Marker: marker, BodySha256: "xyz"},
		{Marker: marker, BodySha256: strings.ToUpper(m.BodySha256)},
	} {
		if bad.Check() == nil {
			t.Errorf("Check admitted %+v", bad)
		}
	}
}

func TestCheckSetPassed(t *testing.T) {
	ok := Check{Status: "completed", Conclusion: "success"}
	if !(CheckSet{Checks: []Check{ok, {Status: "completed", Conclusion: "neutral"}, {Status: "completed", Conclusion: "skipped"}}}).Passed() {
		t.Fatal("a whole passing set")
	}
	for name, s := range map[string]CheckSet{
		"empty":      {},
		"incomplete": {Checks: []Check{ok}, Incomplete: []string{"cut"}},
		"failed":     {Checks: []Check{ok, {Status: "completed", Conclusion: "failure"}}},
		"pending":    {Checks: []Check{ok, {Status: "in_progress"}}},
		"stale":      {Checks: []Check{ok, {Status: "completed", Conclusion: "stale"}}},
		"unknown":    {Checks: []Check{ok, {Status: "completed", Conclusion: "something_new"}}},
		"none said":  {Checks: []Check{ok, {Status: "completed"}}},
	} {
		if s.Passed() {
			t.Errorf("%s passed", name)
		}
	}
	var s CheckSet
	for i := 0; i < MaxIncomplete+3; i++ {
		s.AddIncomplete(strings.Repeat("é", 100))
	}
	if len(s.Incomplete) != MaxIncomplete {
		t.Fatalf("%d reasons", len(s.Incomplete))
	}
	for _, w := range s.Incomplete {
		if len(w) > MaxIncompleteLen || !strings.HasPrefix(w, "é") && !strings.HasPrefix(w, "more") {
			t.Fatalf("reason %q", w)
		}
	}
}

func TestErrorsWrap(t *testing.T) {
	err := error(&RequestError{System: "github", Method: "GET", Path: "/repos/o/r/issues/1", Status: 429, Err: &ErrRateLimited{Reset: time.Unix(1700000000, 0)}})
	var rl *ErrRateLimited
	if !errors.As(err, &rl) || rl.Reset.Unix() != 1700000000 {
		t.Fatalf("As: %v", err)
	}
	if !strings.Contains(err.Error(), "GET /repos/o/r/issues/1: 429: rate limited until") {
		t.Fatalf("text %q", err.Error())
	}
	ctxErr := &RequestError{System: "github", Method: "GET", Path: "/x", Err: ErrUnavailable, Cause: context.Canceled}
	if !errors.Is(ctxErr, context.Canceled) || !errors.Is(ctxErr, ErrUnavailable) {
		t.Fatal("the context's error")
	}
	p := Proven(&RequestError{Err: ErrForbidden})
	if !NothingWritten(p) || !errors.Is(p, ErrForbidden) || errors.Is(p, ErrWriteUnknown) {
		t.Fatal("Proven")
	}
	u := Unknown(&RequestError{Err: ErrUnavailable})
	if NothingWritten(u) || !errors.Is(u, ErrWriteUnknown) {
		t.Fatal("Unknown")
	}
	// The outcome is said in the error's words, not only through errors.Is.
	if !strings.HasSuffix(p.Error(), ": nothing was written") || !strings.HasSuffix(u.Error(), ": not known whether the write was made") {
		t.Fatalf("words %q, %q", p.Error(), u.Error())
	}
	// An error already sorted keeps its outcome.
	if Unknown(p) != p || Proven(u) != u {
		t.Fatal("a sorted error sorted again")
	}
}

// An account is one only in its form: a login of letters, digits, '-' and
// '_' within MaxLogin, at most MaxScopes scopes of lower-case letters,
// digits, '_' and ':' within MaxScope; at every bound it passes. CheckFor
// refuses one any field of which holds the token it was asked with,
// whatever its form, and its error quotes nothing of it.
func TestAccountForm(t *testing.T) {
	scopes := func(n int, s string) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = s
		}
		return out
	}
	for _, a := range []Account{
		{Login: "mona-example", ID: "9000001", Scopes: []string{"repo", "read:org", "admin:repo_hook"}},
		{Login: "Mona_Ex-1", ID: "1"},
		{Login: strings.Repeat("a", MaxLogin), ID: "1", Scopes: scopes(MaxScopes, strings.Repeat("b", MaxScope))},
	} {
		if err := a.Check(); err != nil {
			t.Errorf("%.40q: %v", a.Login, err)
		}
	}
	for name, a := range map[string]Account{
		"no login":             {ID: "1"},
		"a long login":         {Login: strings.Repeat("a", MaxLogin+1), ID: "1"},
		"a login with a space": {Login: "mona example", ID: "1"},
		"a login with a quote": {Login: `mona"`, ID: "1"},
		"a login with a dot":   {Login: "mona.example", ID: "1"},
		"a non-ASCII login":    {Login: "moná", ID: "1"},
		"an upper-case scope":  {Login: "mona", ID: "1", Scopes: []string{"Repo"}},
		"an empty scope":       {Login: "mona", ID: "1", Scopes: []string{""}},
		"a long scope":         {Login: "mona", ID: "1", Scopes: []string{strings.Repeat("a", MaxScope+1)}},
		"a scope with a space": {Login: "mona", ID: "1", Scopes: []string{"read org"}},
		"too many scopes":      {Login: "mona", ID: "1", Scopes: scopes(MaxScopes+1, "repo")},
		"no id":                {Login: "mona"},
	} {
		if a.Check() == nil {
			t.Errorf("%s: passed", name)
		}
	}
	// Made-up tokens short enough for a login's form, and a lower-case one
	// for a scope's.
	const short, lower = "PLANTEDshort1", "planted_short_2"
	cred := func(tok string) Credential {
		c, err := Token("github.com", tok, false)
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	if err := (Account{Login: "mona", ID: "1", Scopes: []string{"repo"}}).CheckFor(cred(short)); err != nil {
		t.Errorf("an account that holds no token: %v", err)
	}
	for name, c := range map[string]struct {
		a   Account
		tok string
	}{
		"the login":        {Account{Login: short, ID: "1"}, short},
		"inside the login": {Account{Login: "x" + short + "y", ID: "1"}, short},
		"the id":           {Account{Login: "mona", ID: RemoteID(short)}, short},
		"a scope":          {Account{Login: "mona", ID: "1", Scopes: []string{"repo", lower}}, lower},
	} {
		if err := c.a.Check(); err != nil {
			t.Fatalf("%s: the account is in its form: %v", name, err)
		}
		if err := c.a.CheckFor(cred(c.tok)); err == nil || strings.Contains(err.Error(), c.tok) {
			t.Errorf("%s: %v", name, err)
		}
	}
}
