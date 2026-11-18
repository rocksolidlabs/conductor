package ext_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/ext"
)

var (
	sha40 = strings.Repeat("0a", 20)
	sha64 = strings.Repeat("0a", 32)
)

// providerLaunch is a provider's launch with every field of launch-a set.
func providerLaunch() ext.LaunchRequest {
	return ext.LaunchRequest{
		Crew: "nightly", Label: "nightly", By: ext.Principal{Subject: "u-1", Name: "Ada"},
		OpID: "op-1", Origin: ext.Origin{Ref: "example:42", Label: "Nightly checks"},
		Subject:   ext.Subject{Ref: "example:42", URL: "https://example.com/42", Prior: "nightly-0123abcd"},
		StopAfter: time.Hour,
	}
}

func TestLaunchRequestBounds(t *testing.T) {
	long := func(n int) string { return strings.Repeat("a", n) }
	edit := func(f func(*ext.LaunchRequest)) error { r := providerLaunch(); f(&r); return r.Valid() }
	cases := []struct {
		name string
		err  error
		ok   bool
	}{
		{"a provider's launch", providerLaunch().Valid(), true},
		{"origin alone with its op id", edit(func(r *ext.LaunchRequest) { r.Subject, r.StopAfter = ext.Subject{}, 0 }), true},

		// Without an origin, nothing of a provider's launch.
		{"no origin and nothing else", edit(func(r *ext.LaunchRequest) {
			r.Origin, r.OpID, r.Subject, r.StopAfter = ext.Origin{}, "", ext.Subject{}, 0
		}), true},
		{"an op id without an origin", edit(func(r *ext.LaunchRequest) { r.Origin, r.Subject, r.StopAfter = ext.Origin{}, ext.Subject{}, 0 }), false},
		{"a subject without an origin", edit(func(r *ext.LaunchRequest) { r.Origin, r.OpID, r.StopAfter = ext.Origin{}, "", 0 }), false},
		{"a limit without an origin", edit(func(r *ext.LaunchRequest) { r.Origin, r.OpID, r.Subject = ext.Origin{}, "", ext.Subject{} }), false},
		{"an origin without an op id", edit(func(r *ext.LaunchRequest) { r.OpID = "" }), false},

		// The op id.
		{"op id at its bound", edit(func(r *ext.LaunchRequest) { r.OpID = long(ext.MaxOpID) }), true},
		{"op id past its bound", edit(func(r *ext.LaunchRequest) { r.OpID = long(ext.MaxOpID + 1) }), false},
		{"op id of every character it may hold", edit(func(r *ext.LaunchRequest) { r.OpID = "aZ09._~-" }), true},
		{"op id with a slash", edit(func(r *ext.LaunchRequest) { r.OpID = "a/b" }), false},

		// The origin.
		{"origin ref at its bound", edit(func(r *ext.LaunchRequest) { r.Origin.Ref = long(ext.MaxOriginRef) }), true},
		{"origin ref past its bound", edit(func(r *ext.LaunchRequest) { r.Origin.Ref = long(ext.MaxOriginRef + 1) }), false},
		{"origin ref empty", edit(func(r *ext.LaunchRequest) { r.Origin.Ref = "" }), false},
		{"origin ref with a space", edit(func(r *ext.LaunchRequest) { r.Origin.Ref = "a b" }), false},
		{"origin ref not ASCII", edit(func(r *ext.LaunchRequest) { r.Origin.Ref = "é" }), false},
		{"origin label at its bound in characters", edit(func(r *ext.LaunchRequest) { r.Origin.Label = strings.Repeat("é", ext.MaxRunLabel) }), true},
		{"origin label past its bound", edit(func(r *ext.LaunchRequest) { r.Origin.Label = long(ext.MaxRunLabel + 1) }), false},
		{"origin label empty", edit(func(r *ext.LaunchRequest) { r.Origin.Label = "" }), false},
		{"origin label of two lines", edit(func(r *ext.LaunchRequest) { r.Origin.Label = "a\n# b" }), false},
		{"origin label with a bidirectional control", edit(func(r *ext.LaunchRequest) { r.Origin.Label = "a\u202eb" }), true},

		// The subject.
		{"subject ref at its bound", edit(func(r *ext.LaunchRequest) { r.Subject.Ref = "example:" + long(ext.MaxSubjectRef-8) }), true},
		{"subject ref past its bound", edit(func(r *ext.LaunchRequest) { r.Subject.Ref = "example:" + long(ext.MaxSubjectRef-7) }), false},
		{"subject without a ref", edit(func(r *ext.LaunchRequest) { r.Subject.Ref = "" }), false},
		{"subject ref of two lines", edit(func(r *ext.LaunchRequest) { r.Subject.Ref = "example:a\nb" }), false},
		{"subject ref without a kind", edit(func(r *ext.LaunchRequest) { r.Subject.Ref = "42" }), false},
		{"subject url at its bound", edit(func(r *ext.LaunchRequest) {
			r.Subject.URL = "https://example.com/" + long(ext.MaxSubjectURL-len("https://example.com/"))
		}), true},
		{"subject url past its bound", edit(func(r *ext.LaunchRequest) {
			r.Subject.URL = "https://example.com/" + long(ext.MaxSubjectURL-len("https://example.com/")+1)
		}), false},
		{"subject url with a port and a path", edit(func(r *ext.LaunchRequest) { r.Subject.URL = "https://example.com:8443/a/b%20c" }), true},
		{"subject url with userinfo", edit(func(r *ext.LaunchRequest) { r.Subject.URL = "https://u:p@example.com/42" }), false},
		{"subject url with a user alone", edit(func(r *ext.LaunchRequest) { r.Subject.URL = "https://u@example.com/42" }), false},
		{"subject url with a query", edit(func(r *ext.LaunchRequest) { r.Subject.URL = "https://example.com/42?token=x" }), false},
		{"subject url with an empty query", edit(func(r *ext.LaunchRequest) { r.Subject.URL = "https://example.com/42?" }), false},
		{"subject url with a fragment", edit(func(r *ext.LaunchRequest) { r.Subject.URL = "https://example.com/42#x" }), false},
		{"subject url over http", edit(func(r *ext.LaunchRequest) { r.Subject.URL = "http://example.com/42" }), false},
		{"subject url of javascript", edit(func(r *ext.LaunchRequest) { r.Subject.URL = "javascript:alert(1)" }), false},
		{"subject url of data", edit(func(r *ext.LaunchRequest) { r.Subject.URL = "data:text/html,x" }), false},
		{"subject url in capitals", edit(func(r *ext.LaunchRequest) { r.Subject.URL = "HTTPS://example.com/42" }), false},
		{"subject url without a host", edit(func(r *ext.LaunchRequest) { r.Subject.URL = "https:///42" }), false},
		{"subject url with a space", edit(func(r *ext.LaunchRequest) { r.Subject.URL = "https://example.com/a b" }), false},
		{"subject prior at its bound", edit(func(r *ext.LaunchRequest) { r.Subject.Prior = long(ext.MaxCrewID) + "-0123abcd" }), true},
		{"subject prior past its bound", edit(func(r *ext.LaunchRequest) { r.Subject.Prior = long(ext.MaxCrewID+1) + "-0123abcd" }), false},
		{"subject prior of 73 bytes", edit(func(r *ext.LaunchRequest) { r.Subject.Prior = long(ext.MaxRunID-9) + "-0123abcd" }), true},
		{"subject prior of 74 bytes", edit(func(r *ext.LaunchRequest) { r.Subject.Prior = long(ext.MaxRunID-8) + "-0123abcd" }), false},
		{"subject prior not a run's id", edit(func(r *ext.LaunchRequest) { r.Subject.Prior = "nightly" }), false},
		{"subject prior in capitals", edit(func(r *ext.LaunchRequest) { r.Subject.Prior = "nightly-0123ABCD" }), false},
		{"subject prior without a crew", edit(func(r *ext.LaunchRequest) { r.Subject.Prior = "-0123abcd" }), false},
		{"subject a link without a ref", edit(func(r *ext.LaunchRequest) { r.Subject = ext.Subject{URL: "https://example.com/42"} }), false},

		// The time limit.
		{"limit at its least", edit(func(r *ext.LaunchRequest) { r.StopAfter = ext.MinStopAfter }), true},
		{"limit at its most", edit(func(r *ext.LaunchRequest) { r.StopAfter = ext.MaxStopAfter }), true},
		{"limit of 9 minutes", edit(func(r *ext.LaunchRequest) { r.StopAfter = 9 * time.Minute }), false},
		{"limit of 1,441 minutes", edit(func(r *ext.LaunchRequest) { r.StopAfter = 1441 * time.Minute }), false},
		{"limit not of whole minutes", edit(func(r *ext.LaunchRequest) { r.StopAfter = time.Hour + time.Second }), false},
		{"limit below 0", edit(func(r *ext.LaunchRequest) { r.StopAfter = -time.Hour }), false},

		// The project, the digest and the start commit.
		{"held to a resolved launch", edit(func(r *ext.LaunchRequest) {
			r.Project, r.ResolvedDigest, r.StartCommit = "CND", strings.Repeat("f", ext.DigestLen), sha40
		}), true},
		{"a project alone", edit(func(r *ext.LaunchRequest) { r.Project = "CND" }), true},
		{"a start commit alone, of 64 hex", edit(func(r *ext.LaunchRequest) { r.StartCommit = sha64 }), true},
		{"a digest without a project", edit(func(r *ext.LaunchRequest) { r.ResolvedDigest = strings.Repeat("f", ext.DigestLen) }), false},
		{"a digest in capitals", edit(func(r *ext.LaunchRequest) { r.Project, r.ResolvedDigest = "CND", strings.Repeat("F", ext.DigestLen) }), false},
		{"a digest short", edit(func(r *ext.LaunchRequest) { r.Project, r.ResolvedDigest = "CND", strings.Repeat("f", ext.DigestLen-1) }), false},
		{"a project's key in lower case", edit(func(r *ext.LaunchRequest) { r.Project = "cnd" }), false},
		{"a project's key past its bound", edit(func(r *ext.LaunchRequest) { r.Project = "C123456" }), false},
		{"a start commit of HEAD~1", edit(func(r *ext.LaunchRequest) { r.StartCommit = "HEAD~1" }), false},
		{"a start commit short", edit(func(r *ext.LaunchRequest) { r.StartCommit = sha40[:7] }), false},
		{"a start commit of a branch", edit(func(r *ext.LaunchRequest) { r.StartCommit = "main" }), false},
		{"a project without an origin", edit(func(r *ext.LaunchRequest) {
			r.Origin, r.OpID, r.Subject, r.StopAfter, r.Project = ext.Origin{}, "", ext.Subject{}, 0, "CND"
		}), false},
		{"a start commit without an origin", edit(func(r *ext.LaunchRequest) {
			r.Origin, r.OpID, r.Subject, r.StopAfter, r.StartCommit = ext.Origin{}, "", ext.Subject{}, 0, sha40
		}), false},
	}
	for _, c := range cases {
		if (c.err == nil) != c.ok {
			t.Errorf("%s: Valid() = %v, want ok %v", c.name, c.err, c.ok)
		}
		if c.err != nil && !strings.HasPrefix(c.err.Error(), "ext: launch request: ") {
			t.Errorf("%s: the refusal %q does not name its record", c.name, c.err)
		}
	}
}

func TestSubjectRefs(t *testing.T) {
	change := "change:git.example.com/acme/widgets#12@" + sha40
	for _, ref := range []string{
		"example:42",
		"ticket:CND-12",
		change,
		"change:git.example.com/acme/widgets#12@" + sha64,
		"change:a/A.b_c-d/x#" + strings.Repeat("9", 18) + "@" + sha40,
		"change:" + strings.Repeat("a", 253) + "/" + strings.Repeat("o", 100) + "/" + strings.Repeat("n", 100) + "#1@" + sha40,
		"job" + strings.TrimPrefix(change, "change") + "/lint",
		"job" + strings.TrimPrefix(change, "change") + "/" + strings.Repeat("c", 64),
		"job:git.example.com/acme/widgets#12@" + sha64 + "/build.linux_amd64-1",
	} {
		if err := ext.SubjectRef(ref); err != nil {
			t.Errorf("SubjectRef(%q) = %v", ref, err)
		}
	}
	for _, ref := range []string{
		"", "example", ":42", "example:", "Example:42", "1x:42", "example 2:x",
		"example:" + strings.Repeat("a", ext.MaxSubjectRef), "example:a\tb", "example:\u0085",
		"change:git.example.com/acme/widgets#12",                                // no commit
		"change:git.example.com/acme/widgets@" + sha40,                          // no number
		"change:git.example.com/acme#12@" + sha40,                               // no name
		"change:git.example.com/acme/widgets/x#12@" + sha40,                     // a part too many
		"change:Git.example.com/acme/widgets#12@" + sha40,                       // a host in capitals
		"change:" + strings.Repeat("a", 254) + "/acme/widgets#12@" + sha40,      // a host past its bound
		"change:git.example.com/" + strings.Repeat("o", 101) + "/w#12@" + sha40, // an owner past its bound
		"change:git.example.com/acme/wid gets#12@" + sha40,
		"change:git.example.com/acme/widgets#0@" + sha40,
		"change:git.example.com/acme/widgets#012@" + sha40,
		"change:git.example.com/acme/widgets#" + strings.Repeat("9", 19) + "@" + sha40,
		"change:git.example.com/acme/widgets#12@" + strings.ToUpper(sha40),
		"change:git.example.com/acme/widgets#12@" + sha40[:39],
		"change:git.example.com/acme/widgets#12@HEAD",
		"job:git.example.com/acme/widgets#12@" + sha40,          // no check
		"job:git.example.com/acme/widgets#12@" + sha40 + "/",    // an empty check
		"job:git.example.com/acme/widgets#12@" + sha40 + "/a b", // a check with a space
		"job:git.example.com/acme/widgets#12@" + sha40 + "/" + strings.Repeat("c", 65),
	} {
		if err := ext.SubjectRef(ref); err == nil {
			t.Errorf("SubjectRef(%q) took it", ref)
		}
	}
}

func TestStopRequestBounds(t *testing.T) {
	by := ext.Principal{Subject: "u-1", Name: "Ada"}
	for _, c := range []struct {
		name string
		r    ext.StopRequest
		ok   bool
	}{
		{"a stop", ext.StopRequest{Run: "nightly-0123abcd", By: by, Why: "superseded by a newer change"}, true},
		{"a stop without a why", ext.StopRequest{Run: "nightly-0123abcd", By: by}, true},
		{"why at its bound", ext.StopRequest{Run: "nightly-0123abcd", By: by, Why: strings.Repeat("a", ext.MaxStopWhy)}, true},
		{"why past its bound", ext.StopRequest{Run: "nightly-0123abcd", By: by, Why: strings.Repeat("a", ext.MaxStopWhy+1)}, false},
		{"why of two lines", ext.StopRequest{Run: "nightly-0123abcd", By: by, Why: "a\nb"}, false},
		{"why with an escape", ext.StopRequest{Run: "nightly-0123abcd", By: by, Why: "a\x1b[2Jb"}, false},
		{"run not a run's id", ext.StopRequest{Run: "nightly", By: by}, false},
		{"run empty", ext.StopRequest{By: by}, false},
		{"by no one", ext.StopRequest{Run: "nightly-0123abcd"}, false},
	} {
		if err := c.r.Valid(); (err == nil) != c.ok {
			t.Errorf("%s: Valid() = %v, want ok %v", c.name, err, c.ok)
		}
	}
}

func TestRunStatusValid(t *testing.T) {
	good := ext.RunStatus{ID: "nightly-0123abcd", Crew: "nightly", State: ext.RunRunning, Origin: ext.RunOrigin{OpID: "op-1", Ref: "example:42", Label: "Nightly"}}
	edit := func(f func(*ext.RunStatus)) error { s := good; f(&s); return s.Valid() }
	for _, c := range []struct {
		name string
		err  error
		ok   bool
	}{
		{"a run", good.Valid(), true},
		{"every state", errors.Join(
			edit(func(s *ext.RunStatus) { s.State = ext.RunNeedsInput }),
			edit(func(s *ext.RunStatus) { s.State = ext.RunStopped }),
			edit(func(s *ext.RunStatus) { s.State = ext.RunFinished; s.Outcome = ext.OutcomeSucceeded }),
			edit(func(s *ext.RunStatus) { s.State = ext.RunInterrupted; s.Outcome = ext.OutcomeCancelled }),
		), true},
		{"every field of its origin", edit(func(s *ext.RunStatus) {
			s.Origin.Subject = ext.Subject{Ref: "example:42", URL: "https://example.com/42", Prior: "nightly-89abcdef"}
			s.Origin.Project, s.Origin.StartCommit, s.Origin.ResolvedDigest = "CND", sha64, strings.Repeat("f", ext.DigestLen)
			s.Origin.Deadline = time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)
		}), true},
		{"another state", edit(func(s *ext.RunStatus) { s.State = "paused" }), false},
		{"another outcome", edit(func(s *ext.RunStatus) { s.Outcome = "passed" }), false},
		{"needs input below 0", edit(func(s *ext.RunStatus) { s.NeedsInput = -1 }), false},
		{"id not a run's", edit(func(s *ext.RunStatus) { s.ID = "nightly" }), false},
		{"crew not a crew id", edit(func(s *ext.RunStatus) { s.Crew = "Nightly" }), false},
		{"origin without an op id", edit(func(s *ext.RunStatus) { s.Origin.OpID = "" }), false},
		{"origin without a label", edit(func(s *ext.RunStatus) { s.Origin.Label = "" }), false},
		{"origin subject without a ref", edit(func(s *ext.RunStatus) { s.Origin.Subject = ext.Subject{Prior: "nightly-89abcdef"} }), false},
		{"origin project not a key", edit(func(s *ext.RunStatus) { s.Origin.Project = "cnd" }), false},
		{"origin start commit short", edit(func(s *ext.RunStatus) { s.Origin.StartCommit = sha40[:12] }), false},
		{"origin digest without a project", edit(func(s *ext.RunStatus) { s.Origin.ResolvedDigest = strings.Repeat("f", ext.DigestLen) }), false},
		{"origin deadline not in UTC", edit(func(s *ext.RunStatus) {
			s.Origin.Deadline = time.Date(2026, 11, 18, 9, 0, 0, 0, time.FixedZone("x", 3600))
		}), false},
	} {
		if (c.err == nil) != c.ok {
			t.Errorf("%s: Valid() = %v, want ok %v", c.name, c.err, c.ok)
		}
	}
}

func TestRefusalIsARefusalWithItsCode(t *testing.T) {
	r := &ext.Refusal{Code: ext.RefusalNotManaged, Why: "the crew has no tasks"}
	if got, want := r.Error(), "ext: refused: not_managed: the crew has no tasks"; got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
	if got, want := (&ext.Refusal{Code: "x"}).Error(), "ext: refused: x"; got != want {
		t.Errorf("Error() with no why = %q, want %q", got, want)
	}
	wrapped := fmt.Errorf("launching: %w", r)
	switch {
	case !errors.Is(r, ext.ErrRefused) || !errors.Is(wrapped, ext.ErrRefused):
		t.Error("a refusal with a code is not ext.ErrRefused")
	case errors.Is(r, ext.ErrNotFound) || errors.Is(r, ext.ErrUnsupported):
		t.Error("a refusal is another error too")
	case ext.RefusalCode(r) != ext.RefusalNotManaged || ext.RefusalCode(wrapped) != ext.RefusalNotManaged:
		t.Errorf("RefusalCode = %q through a wrap, want %q", ext.RefusalCode(wrapped), ext.RefusalNotManaged)
	case ext.RefusalCode(fmt.Errorf("%w: no", ext.ErrRefused)) != "":
		t.Error("a plain refusal has a code")
	case ext.RefusalCode(nil) != "" || ext.RefusalCode(ext.ErrNotFound) != "":
		t.Error("an error that is no refusal has a code")
	case ext.RefusalCode(errors.Join(ext.ErrNotFound, r)) != ext.RefusalNotManaged:
		t.Error("a refusal joined with another error lost its code")
	}
	for _, code := range []string{
		ext.RefusalInvalidRequest, ext.RefusalNotManaged, ext.RefusalInvalidSubject, ext.RefusalPriorUnknown,
		ext.RefusalProjectUnknown, ext.RefusalProjectArchived, ext.RefusalLaunchChanged, ext.RefusalStartCommitMissing,
		ext.RefusalResolvedTooLarge, ext.RefusalInvalidCrew, ext.RefusalInvalidCwd, "crew_changed", strings.Repeat("a", ext.MaxRefusalCode),
	} {
		if !ext.ValidRefusalCode(code) {
			t.Errorf("ValidRefusalCode(%q) = false", code)
		}
	}
	for _, code := range []string{"", "Not_managed", "not-managed", "not managed", strings.Repeat("a", ext.MaxRefusalCode+1)} {
		if ext.ValidRefusalCode(code) {
			t.Errorf("ValidRefusalCode(%q) = true", code)
		}
	}
}
