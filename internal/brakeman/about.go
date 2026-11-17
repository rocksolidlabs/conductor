package brakeman

import (
	"fmt"
	"slices"
	"strings"
)

// What a request is about, for the list's about filter: a run, a task, a
// session or a ticket, matched against the request's subjects and against
// what its brief cites (the digest's about). A request's own run and
// session (Request.RunID, SessionID) are its turn's, never what it is
// about, and never match.

// The kinds of ref the about filter takes.
var AboutKinds = []string{RefRun, RefTask, RefSession, RefTicket}

// How a request matched an about filter.
const (
	MatchSubject = "subject" // a subject names the thing or a part of it
	MatchCited   = "cited"   // the brief cites it, and no subject names it
)

// ParseAbout parses an about filter's ref: one of AboutKinds, at most
// MaxRef bytes.
func ParseAbout(s string) (Ref, error) {
	if s == "" {
		return Ref{}, &RefError{Ref: s, Why: "empty"}
	}
	r, err := ParseRef(s)
	if err != nil {
		return Ref{}, err
	}
	if !slices.Contains(AboutKinds, r.Kind) {
		return Ref{}, &RefError{Ref: s, Why: fmt.Sprintf("about takes a ref of kind %s", strings.Join(AboutKinds, ", "))}
	}
	return r, nil
}

// AboutMatch says how a request with these subjects and this digest (nil
// for none) matches the about filter about: MatchSubject when a subject
// names it or a part of it, else MatchCited when the digest's about holds
// it, else "".
func AboutMatch(about Ref, subjects []Ref, d *Digest) string {
	if slices.ContainsFunc(subjects, func(s Ref) bool { return subjectUnder(about, s) }) {
		return MatchSubject
	}
	if d != nil && citedIn(about, d.About) {
		return MatchCited
	}
	return ""
}

// subjectUnder reports whether the subject s names about or a part of it:
// a run's tasks and attempts, a task's attempts, a session's questions, a
// ticket's comments.
func subjectUnder(about, s Ref) bool {
	switch about.Kind {
	case RefRun:
		return (s.Kind == RefRun || s.Kind == RefTask || s.Kind == RefAttempt) && s.Run == about.Run
	case RefTask:
		return (s.Kind == RefTask || s.Kind == RefAttempt) && s.Run == about.Run && s.Task == about.Task
	case RefSession:
		return (s.Kind == RefSession || s.Kind == RefQuestion) && s.Session == about.Session
	case RefTicket:
		return (s.Kind == RefTicket || s.Kind == RefComment) && s.Ticket == about.Ticket
	}
	return false
}

// citedIn reports whether a digest's about lists the thing about names.
func citedIn(about Ref, a DigestAbout) bool {
	switch about.Kind {
	case RefRun:
		return slices.Contains(a.Runs, about.Run)
	case RefTask:
		return slices.Contains(a.Tasks, about.Run+"/"+about.Task)
	case RefSession:
		return slices.Contains(a.Sessions, about.Session)
	case RefTicket:
		return slices.Contains(a.Tickets, about.Ticket)
	}
	return false
}
