package api

import (
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/rocksolidlabs/conductor/internal/brakeman"
)

// Brakeman's list of requests, filtered (docs/protocol.md, Brakeman):
// GET /api/brakeman/requests, the admin's alone, a page of requests
// newest first, each with its brief's digest, narrowed by kind, by what
// the brief holds, by what the request is about and by a word. A filter
// only narrows what the admin already reads. The query is never logged.

// The list's bounds.
const (
	// maxBrakemanSearch bounds q, in bytes.
	maxBrakemanSearch = 200
)

// The has filter's words: a digest (brief), or one with a count above 0.
var brakemanHas = []string{"decision", "proposal", "draft", "brief"}

// brakemanRow is a row of the list: the request, its digest (absent with
// no brief) and, with about, how it matched (subject or cited).
type brakemanRow struct {
	brakeman.Request
	Digest *brakeman.Digest `json:"digest,omitempty"`
	Match  string           `json:"match,omitempty"`
}

// brakemanListQuery is the list's query, read and checked.
type brakemanListQuery struct {
	after string
	limit int
	kinds []string
	has   string
	about *brakeman.Ref
	q     string // lower-cased
}

// readBrakemanList reads the list's query: every parameter optional and
// given at most once, each refused with its words when it is not what it
// takes.
func readBrakemanList(r *http.Request) (brakemanListQuery, error) {
	out := brakemanListQuery{limit: brakeman.MaxPage}
	dq, err := readQuery(r, "after", "limit", "kind", "has", "about", "q")
	if err != nil {
		return out, err
	}
	val := func(name string) (string, bool, error) {
		v, err := dq.one(name)
		_, given := dq[name]
		return v, given, err
	}
	if v, given, err := val("limit"); err != nil {
		return out, err
	} else if given {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > brakeman.MaxPage || strconv.Itoa(n) != v {
			return out, fmt.Errorf("limit is 1 to %d", brakeman.MaxPage)
		}
		out.limit = n
	}
	if v, given, err := val("after"); err != nil {
		return out, err
	} else if given {
		if !brakemanIDPattern(v) {
			return out, fmt.Errorf("after is a request's id (br-<n>)")
		}
		out.after = v
	}
	kinds := append(slices.Clone(brakeman.Kinds), brakeman.KindWake)
	if v, given, err := val("kind"); err != nil {
		return out, err
	} else if given {
		if v == "" {
			return out, fmt.Errorf("kind is a comma list of %s", strings.Join(kinds, ", "))
		}
		for _, k := range strings.Split(v, ",") {
			if !slices.Contains(kinds, k) {
				return out, fmt.Errorf("kind %q is not one of %s", cut(k, 40), strings.Join(kinds, ", "))
			}
			if slices.Contains(out.kinds, k) {
				return out, fmt.Errorf("kind %q is given twice", k)
			}
			out.kinds = append(out.kinds, k)
		}
	}
	if v, given, err := val("has"); err != nil {
		return out, err
	} else if given {
		if !slices.Contains(brakemanHas, v) {
			return out, fmt.Errorf("has is one of %s", strings.Join(brakemanHas, ", "))
		}
		out.has = v
	}
	if v, given, err := val("about"); err != nil {
		return out, err
	} else if given {
		ref, err := brakeman.ParseAbout(v)
		if err != nil {
			return out, fmt.Errorf("about is one ref of kind %s: %w", strings.Join(brakeman.AboutKinds, ", "), err)
		}
		out.about = &ref
	}
	if v, given, err := val("q"); err != nil {
		return out, err
	} else if given {
		switch {
		case v == "":
			return out, fmt.Errorf("q is a word or a line, 1 to %d bytes", maxBrakemanSearch)
		case len(v) > maxBrakemanSearch:
			return out, fmt.Errorf("q is %d bytes, at most %d", len(v), maxBrakemanSearch)
		case strings.ContainsAny(v, "\r\n") || !utf8.ValidString(v):
			return out, fmt.Errorf("q is one line of text")
		}
		out.q = strings.ToLower(v)
	}
	return out, nil
}

// keep is the filter's answer for a request and its digest: whether it
// is listed, and with about how it matched.
func (q brakemanListQuery) keep(r brakeman.Request, d *brakeman.Digest) (bool, string) {
	if q.kinds != nil && !slices.Contains(q.kinds, r.Kind) {
		return false, ""
	}
	switch q.has {
	case "brief":
		if d == nil {
			return false, ""
		}
	case "decision", "proposal", "draft":
		if d == nil || (q.has == "decision" && d.Counts.Decisions == 0) || (q.has == "proposal" && d.Counts.Proposals == 0) || (q.has == "draft" && d.Counts.Drafts == 0) {
			return false, ""
		}
	}
	if q.q != "" && !q.found(r, d) {
		return false, ""
	}
	if q.about == nil {
		return true, ""
	}
	match := brakeman.AboutMatch(*q.about, r.Subjects, d)
	return match != "", match
}

// found reports whether the search's word is in the request's id, note
// or subjects, or its digest's title, reading or what the brief cites,
// whatever its case.
func (q brakemanListQuery) found(r brakeman.Request, d *brakeman.Digest) bool {
	in := func(s string) bool { return strings.Contains(strings.ToLower(s), q.q) }
	if in(r.ID) || in(r.Note) || slices.ContainsFunc(r.Subjects, func(s brakeman.Ref) bool { return in(s.String()) }) {
		return true
	}
	if d == nil {
		return false
	}
	if in(d.Title) || in(d.Reading) {
		return true
	}
	for _, list := range [][]string{d.About.Runs, d.About.Tasks, d.About.Sessions, d.About.Tickets} {
		if slices.ContainsFunc(list, in) {
			return true
		}
	}
	return false
}

// handleBrakemanRequests answers a page of requests, newest first, each
// with its digest: {requests, next}, narrowed by ?kind=, ?has=, ?about=
// and ?q=, read after ?after=, at most ?limit= (1 to 100) of them; next
// is the last id answered when the page filled and more are left to
// look at, "" otherwise.
func (s *Server) handleBrakemanRequests(w http.ResponseWriter, r *http.Request) {
	d, ok := s.brakemanDeskOf(w)
	if !ok {
		return
	}
	q, err := readBrakemanList(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	page, next, err := d.st.ListWhere(q.after, q.limit, q.keep)
	if err != nil {
		s.brakemanError(w, err)
		return
	}
	rows := make([]brakemanRow, 0, len(page))
	for _, l := range page {
		rows = append(rows, brakemanRow{Request: l.Request, Digest: l.Digest, Match: l.Match})
	}
	writeJSON(w, http.StatusOK, map[string]any{"requests": rows, "next": next})
}
