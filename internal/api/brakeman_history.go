package api

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/rocksolidlabs/conductor/internal/brakeman"
	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/history"
	"github.com/rocksolidlabs/conductor/internal/journal"
)

// Brakeman reads history within its grant (docs/plans/history.md, PR-11).
// GET /api/brakeman/history is the History search's line answered for the
// caller: the admin, or a Brakeman turn held to its grant as its refs are
// (refGrant). A turn's read is answered from the rows its grant covers and
// reads no other, in its cost too: the index draws them by their own
// columns (historyRestriction: the kinds it may be served, each placed
// within the scope by the one column its rows carry) and matches the
// line's words against those rows alone, at most maxHistoryTurnExamine a
// reply; the page is newest first, never ranked; and each row is then
// admitted as Brakeman's refs are: every
// class its text reads visible (historyKinds), the record it is of
// resolving within the scope now, read from that record's own store and
// never from the index. A row that cannot be judged (a record that cannot
// be read) is left out as a refused one is, and the read fails only for a
// store it needs as a whole, checked before it looks at a row
// (historyStores). An entry is one stored record's: no kind whose text is
// built from more than one run's records or written through an act is
// served (historyWithheld), and an entry carries no links. One table
// (historyKinds with historyFields) decides both what an entry serves and
// the classes its read is noted in, each with no revision: a history row
// is a line of the index, not a snapshot of its record. A continuation
// names only what the reply showed: the position after its last entry,
// sealed for the caller and the line (sealContinuation); a reply of no
// entries carries none.

// The bounds of a turn's read of the history.
const (
	// maxHistoryTurnPage bounds the entries an answer holds, and
	// defaultHistoryTurnPage is a read that names no limit: each entry
	// costs the resolution of its record, read from its store.
	maxHistoryTurnPage     = 20
	defaultHistoryTurnPage = 10
	// maxHistoryTurnExamine bounds the rows of the read's restriction the
	// index draws for one reply, which is its whole cost: past it the
	// answer is what was found, with cut "examined" when that is less
	// than a page (history.MaxExamine). historyTurnPage is how many
	// entries the index is asked for at a time.
	maxHistoryTurnExamine = history.MaxExamine
	historyTurnPage       = 50
	// maxHistoryTurnAfter bounds a read's after: a continuation this
	// route gave, an index cursor (at most maxHistoryAfter) sealed.
	maxHistoryTurnAfter = 3 << 10
	// historyReplyReserve is what a reply holds beside its entries: its
	// braces and names, its indexing's state and a next at its bound.
	historyReplyReserve = 256 + maxHistoryTurnAfter
)

// The records whose columns place a kind's rows within a scope
// (historyRestriction): a session's (its repository, its run and crew, its
// own id), a run's (its crew, its run, its repository, the project of the
// ticket it was launched from), a ticket's (its project, and the run a
// launch or a delivery names) and a brief's, whose footprint its admission
// judges (resolveBrief).
const (
	placeSession = "session"
	placeRun     = "run"
	placeTicket  = "ticket"
	placeBrief   = "brief"
)

// historyKind is what a kind served to a turn reads: every visibility
// class its indexed text reads (the class of the record it is of, and a
// check's log, its body, the logs'), and the record whose columns place its
// rows within a scope.
type historyKind struct {
	classes []string
	place   string
}

// historyKinds is every kind the route serves, each one stored record's,
// by its kind. With historyWithheld it names every kind of the index's
// (TestEveryHistoryKindIsDecided): a kind added to the index is served to
// no turn until it is given its classes here. A brief's item names none:
// the brief carries its footprint's classes (resolveBrief), which its
// resolution checks.
var historyKinds = map[string]historyKind{
	"session":          {[]string{brakeman.ClassSessions}, placeSession},
	"session.typed":    {[]string{brakeman.ClassSessions}, placeSession},
	"session.question": {[]string{brakeman.ClassSessions}, placeSession},
	"session.answer":   {[]string{brakeman.ClassSessions}, placeSession},
	"session.report":   {[]string{brakeman.ClassSessions}, placeSession},
	"session.chat":     {[]string{brakeman.ClassSessions}, placeSession},
	"session.file":     {[]string{brakeman.ClassSessions, brakeman.ClassTouched}, placeSession},

	"attempt":            {[]string{brakeman.ClassRuns}, placeRun},
	"attempt.result":     {[]string{brakeman.ClassRuns}, placeRun},
	"attempt.checkpoint": {[]string{brakeman.ClassRuns}, placeRun},
	"attempt.blocker":    {[]string{brakeman.ClassRuns}, placeRun},
	"message":            {[]string{brakeman.ClassRuns}, placeRun},
	"command":            {[]string{brakeman.ClassRuns}, placeRun},
	"step":               {[]string{brakeman.ClassRuns}, placeRun},
	"candidate.verified": {[]string{brakeman.ClassRuns}, placeRun},
	"check":              {[]string{brakeman.ClassRuns, brakeman.ClassLogs}, placeRun},
	"verify":             {[]string{brakeman.ClassRuns, brakeman.ClassLogs}, placeRun},
	"decision":           {[]string{brakeman.ClassRuns}, placeRun},
	"decision.response":  {[]string{brakeman.ClassRuns}, placeRun},

	"ticket":            {[]string{brakeman.ClassDispatch}, placeTicket},
	"comment":           {[]string{brakeman.ClassDispatch}, placeTicket},
	"plan":              {[]string{brakeman.ClassDispatch}, placeTicket},
	"plan.approved":     {[]string{brakeman.ClassDispatch}, placeTicket},
	"plan.withdrawn":    {[]string{brakeman.ClassDispatch}, placeTicket},
	"plan.superseded":   {[]string{brakeman.ClassDispatch}, placeTicket},
	"launch":            {[]string{brakeman.ClassDispatch}, placeTicket},
	"ticket.accepted":   {[]string{brakeman.ClassDispatch}, placeTicket},
	"ticket.unaccepted": {[]string{brakeman.ClassDispatch}, placeTicket},
	"ticket.closed":     {[]string{brakeman.ClassDispatch}, placeTicket},
	// A ticket's link to the run's candidate it delivers, with its
	// verification: read with its ticket and of that run, admitted only
	// when that run is too (keptOfRun).
	"delivery.verified": {[]string{brakeman.ClassDispatch, brakeman.ClassRuns}, placeTicket},

	"brief.item": {nil, placeBrief},
}

// historyWithheld is every kind of the index's the route never serves,
// with why: its text is not one stored record's that its row's columns
// place, or it holds words an act gives (a launch's label, which the run
// and its tasks are titled by).
var historyWithheld = map[string]string{
	"run":             "titled by its launch's label",
	"task":            "titled by its run's label",
	"review.verdict":  "a review's, read through the review routes",
	"review.finding":  "a review's, read through the review routes",
	"review.batch":    "a review's, read through the review routes",
	"review.resolved": "a review's, built from more than one candidate's records",
}

// historyActWords is, by each operation an act may be granted
// (brakeman.Grantable), the kinds whose indexed text holds words the act
// gives, each kept from every turn (TestEveryActsWordsAreKeptFromTurns): a
// launch and a plan revision an act makes are left out by their actor
// (historyRestriction), a plan's gate, whose question and id the revision
// gives, by its anchor (historyDecisionKinds), a run and its tasks titled
// by the launch's label and a review are withheld kinds
// (historyWithheld). A routing's note is kept in the decision's own
// history, which the index does not hold. TestNoActsWordsReachATurn takes
// every text field of every act through what follows it.
var historyActWords = map[string][]string{
	brakeman.ActLaunchApprovedPlan: {"launch", "run", "task"},
	brakeman.ActRequestRepair:      {"review.batch", "review.finding"},
	brakeman.ActPrepareFollowup:    {"plan", "decision", "decision.response"},
	brakeman.ActRouteDecision:      nil,
}

// historyActors are the actors an act's records carry in the index: its
// actor, and the name an operation records as by.
var historyActors = []string{brakemanActor, brakemanBy}

// historyFields is, by field of a turn's entry (its JSON name; a field of
// its project by project.<name>), the visibility classes it reads beyond
// its kind's: a project's key and a ticket's are Dispatch's records,
// whatever the entry's kind. A field it does not name is never served
// (emitFields); one whose classes the grant does not see is served empty,
// and one served is noted in its classes (TestEveryTurnFieldIsDecided). An
// anchor is served only when its own ref resolves within the grant, in
// its own class too (refReads).
var historyFields = map[string][]string{
	"ref": nil, "kind": nil, "class": nil, "outcome": nil, "title": nil, "summary": nil, "snippet": nil, "at": nil,
	"actor": nil, "source": nil, "host": nil, "revision": nil, "agent": nil, "crew": nil, "run": nil, "anchor": nil,
	"url": nil, "availability": nil, "project.repo": nil, "project.tree": nil,
	"project.key": {brakeman.ClassDispatch},
	"ticket":      {brakeman.ClassDispatch},
}

// emitFields serves te as historyFields says, the grant seeing what sees
// does: each field it does not name, or whose classes are not seen, left
// empty; it answers the classes the fields served read beyond their
// kind's.
func emitFields(te *turnHistoryEntry, sees func(string) bool) []string {
	var read []string
	var walk func(v reflect.Value, prefix string)
	walk = func(v reflect.Value, prefix string) {
		t := v.Type()
		for i := range t.NumField() {
			name, _, _ := strings.Cut(t.Field(i).Tag.Get("json"), ",")
			path, f := prefix+name, v.Field(i)
			classes, ok := historyFields[path]
			if !ok && f.Kind() == reflect.Struct && hasFieldsUnder(path) {
				walk(f, path+".")
				continue
			}
			if !ok || slices.ContainsFunc(classes, func(c string) bool { return !sees(c) }) {
				f.SetZero()
				continue
			}
			if !f.IsZero() {
				read = append(read, classes...)
			}
		}
	}
	walk(reflect.ValueOf(te).Elem(), "")
	return read
}

// hasFieldsUnder reports whether historyFields names a field of the
// struct at path.
func hasFieldsUnder(path string) bool {
	for k := range historyFields {
		if strings.HasPrefix(k, path+".") {
			return true
		}
	}
	return false
}

// entryClasses is every visibility class e's indexed text reads, its
// kind's (historyKinds); known is false for a kind the route does not
// serve.
func entryClasses(e history.Entry) (classes []string, known bool) {
	k, known := historyKinds[e.Kind]
	return k.classes, known
}

// keptOfRun reports whether e is what a ticket keeps of a run's candidate
// (its verified delivery): admitted only when that run is too.
func keptOfRun(e history.Entry) bool {
	return e.Kind == "delivery.verified"
}

// turnHistoryEntry is an entry as a turn reads it: the admin's served
// entry less what names the index's rows (its key, its repeats) and its
// links, with the ref a brief cites it by.
type turnHistoryEntry struct {
	Ref          string         `json:"ref"`
	Kind         string         `json:"kind"`
	Class        string         `json:"class"`
	Outcome      string         `json:"outcome,omitempty"`
	Title        string         `json:"title"`
	Summary      string         `json:"summary,omitempty"`
	Snippet      string         `json:"snippet,omitempty"`
	At           time.Time      `json:"at"`
	Actor        string         `json:"actor,omitempty"`
	Source       turnSource     `json:"source"`
	Host         servedHost     `json:"host"`
	Project      servedProject  `json:"project"`
	Revision     servedRevision `json:"revision"`
	Agent        *servedAgent   `json:"agent,omitempty"`
	Crew         string         `json:"crew,omitempty"`
	Run          string         `json:"run,omitempty"`
	Ticket       string         `json:"ticket,omitempty"`
	Anchor       *servedAnchor  `json:"anchor,omitempty"`
	URL          string         `json:"url"`
	Availability availabilityOf `json:"availability"`
}

// turnSource is an entry's source as a turn reads it: the store and its
// words.
type turnSource struct {
	Store string `json:"store"`
	Label string `json:"label"`
}

// historyScope admits a read's entries: each record's resolution read
// once per read.
type historyScope struct {
	o    *observer
	done map[brakeman.Ref]historyAdmission
	// noted are the reads noted for the reply, each once.
	noted map[brakeman.Read]bool
}

// historyAdmission is a scope ref's resolution: within the grant (why
// ""), with its state now.
type historyAdmission struct {
	res brakeman.Resolution
	why string
}

// admitRef resolves ref once: within the grant, and its resolution. For a
// turn, a Brakeman turn's own run or session is never within it: what an
// earlier turn's agent reported was read under that turn's grant, which
// only its brief's footprint carries (resolveBrief). A ref that cannot be
// judged (its record unread) answers its error once and is refused from
// then on.
func (hs *historyScope) admitRef(ctx context.Context, ref brakeman.Ref) (brakeman.Resolution, bool, error) {
	if a, ok := hs.done[ref]; ok {
		return a.res, a.why == "", nil
	}
	if hs.o.tp != nil {
		turn, err := hs.turnsOwn(ctx, ref)
		if err != nil {
			hs.done[ref] = historyAdmission{why: "not judged"}
			return brakeman.Resolution{}, false, err
		}
		if turn {
			hs.done[ref] = historyAdmission{why: "a Brakeman turn's own"}
			return brakeman.Resolution{}, false, nil
		}
	}
	res, why, err := hs.o.rr.resolve(ctx, ref)
	if err != nil {
		hs.done[ref] = historyAdmission{why: "not judged"}
		return brakeman.Resolution{}, false, err
	}
	hs.done[ref] = historyAdmission{res: res, why: why}
	return res, why == "", nil
}

// turnsOwn reports whether a scope ref is a Brakeman turn's run or
// session: a run of Brakeman's class, as its id or its record says; a
// session whose crew is a turn's, as the registry or its record says.
func (hs *historyScope) turnsOwn(ctx context.Context, ref brakeman.Ref) (bool, error) {
	rr := hs.o.rr
	switch ref.Kind {
	case brakeman.RefRun:
		if crewOfRun(ref.Run) == crew.ClassBrakeman {
			return true, nil
		}
		r, err := rr.run(ctx, ref.Run)
		if err != nil {
			return false, err
		}
		return r.item.Crew == crew.ClassBrakeman || (r.held && r.run.Class == crew.ClassBrakeman), nil
	case brakeman.RefSession:
		if d, ok := rr.s.registry.Get(ref.Session); ok {
			c := d.Info().Crew
			return c != nil && (c.Brakeman != nil || c.CrewID == crew.ClassBrakeman), nil
		}
		rec, ok, err := rr.recordRef(ref.Session)
		if err != nil || !ok {
			return false, err
		}
		return rec.Crew != nil && (rec.Crew.Brakeman != "" || rec.Crew.CrewID == crew.ClassBrakeman), nil
	}
	return false, nil
}

// scopeOfRef is the record a ref of Brakeman's grammar is of, which
// admits it: a run's part its run, a session's question or file its
// session, a ticket's comment, receipt or plan revision its ticket; a
// launch, a brief's item and a decision are their own (the resolver
// admits each by what it is of now). ok is false for none.
func scopeOfRef(r brakeman.Ref) (brakeman.Ref, bool) {
	switch r.Kind {
	case brakeman.RefRun, brakeman.RefTask, brakeman.RefAttempt, brakeman.RefCheck, brakeman.RefCommand,
		brakeman.RefEvent, brakeman.RefMessage, brakeman.RefCommit, brakeman.RefCandidate:
		return brakeman.Ref{Kind: brakeman.RefRun, Run: r.Run}, r.Run != ""
	case brakeman.RefSession, brakeman.RefQuestion, brakeman.RefFile:
		return brakeman.Ref{Kind: brakeman.RefSession, Session: r.Session}, r.Session != ""
	case brakeman.RefTicket, brakeman.RefComment, brakeman.RefPlan:
		return brakeman.Ref{Kind: brakeman.RefTicket, Ticket: r.Ticket}, r.Ticket != ""
	case brakeman.RefLaunch, brakeman.RefBrief, brakeman.RefDecision:
		return r, true
	}
	return brakeman.Ref{}, false
}

// entryScope is the record e is of (its scope ref) and the ref a brief
// cites e by: e's own ref when Brakeman's grammar names it, whose record
// is then its scope; else, for a decision's response, its decision. ok is
// false for an entry of no record a turn can be admitted to.
func entryScope(e history.Entry) (scope, cited brakeman.Ref, ok bool) {
	if r, err := brakeman.ParseRef(e.Ref); err == nil {
		if s, ok := scopeOfRef(r); ok {
			return s, r, true
		}
		return brakeman.Ref{}, brakeman.Ref{}, false
	}
	if isDecisionKind(e.Kind) {
		if id, _, ok := decisionOfRef(e.Ref); ok {
			s := brakeman.Ref{Kind: brakeman.RefDecision, Decision: id}
			return s, s, true
		}
	}
	return brakeman.Ref{}, brakeman.Ref{}, false
}

// admit reports whether the turn may read e, and with which scope ref,
// its resolution and the ref it cites e by: every class e's text reads
// visible, its record within the scope now, and, for what a ticket keeps
// of a run, that run too.
func (hs *historyScope) admit(ctx context.Context, e history.Entry) (scope, cited brakeman.Ref, res brakeman.Resolution, ok bool, err error) {
	classes, known := entryClasses(e)
	if !known || slices.ContainsFunc(classes, func(c string) bool { return !hs.o.sees(c) }) {
		return scope, cited, res, false, nil
	}
	if scope, cited, ok = entryScope(e); !ok {
		return scope, cited, res, false, nil
	}
	if res, ok, err = hs.admitRef(ctx, scope); err != nil || !ok {
		return scope, cited, res, false, err
	}
	if keptOfRun(e) {
		run := brakeman.Ref{Kind: brakeman.RefRun, Run: e.Run}
		if e.Run == "" || run.Valid() != nil {
			return scope, cited, res, false, nil
		}
		if _, ok, err = hs.admitRef(ctx, run); err != nil || !ok {
			return scope, cited, res, false, err
		}
	}
	return scope, cited, res, true, nil
}

// refReads are the reads of a bare ref an anchor names, ok when it
// resolves within the grant (its record's resolution) and its own class
// is seen (a file's touched as well as its session's): its class and its
// record's, at its record's state now when it is that record, else
// "linked"; ok false for one Brakeman's grammar does not name.
func (hs *historyScope) refReads(ctx context.Context, ref string) (reads []brakeman.Read, ok bool, err error) {
	r, err := brakeman.ParseRef(ref)
	if err != nil {
		return nil, false, nil
	}
	scope, ok := scopeOfRef(r)
	if !ok {
		return nil, false, nil
	}
	if c := refClass(r.Kind); c != "" && !hs.o.sees(c) {
		return nil, false, nil
	}
	res, ok, err := hs.admitRef(ctx, scope)
	if err != nil || !ok {
		return nil, false, err
	}
	state := "linked"
	if r == scope {
		state = res.Now.State
	}
	classes := []string{refClass(r.Kind), refClass(scope.Kind)}
	if scope.Kind == brakeman.RefBrief {
		classes = hs.footprint(scope)
	}
	return readsOf(r, classes, state), true, nil
}

// footprint is the classes a brief's item reads: the ones its brief is
// admitted on (briefClasses: its footprint's, or every class its snapshot
// had on for one that keeps none), as an earlier brief given whole is;
// the runs' for one of none.
func (hs *historyScope) footprint(brief brakeman.Ref) []string {
	if req, err := hs.o.rr.desk.Get(brief.Request); err == nil {
		if classes := briefClasses(req); len(classes) > 0 {
			return classes
		}
	}
	return []string{brakeman.ClassRuns}
}

// readsOf is a read of ref in each class of classes, each once, at
// state, with no revision.
func readsOf(ref brakeman.Ref, classes []string, state string) []brakeman.Read {
	var out []brakeman.Read
	for _, c := range classes {
		rd := brakeman.Read{Ref: ref, Class: c, State: state}
		if c != "" && !slices.Contains(out, rd) {
			out = append(out, rd)
		}
	}
	return out
}

// note notes reads in the turn's read log, each once a reply.
func (hs *historyScope) note(reads []brakeman.Read) {
	for _, rd := range reads {
		if !hs.noted[rd] {
			hs.noted[rd] = true
			hs.o.noteRead(rd)
		}
	}
}

// turnEntry is e as served (out) for a turn, and the reads serving it
// makes: the ref it cites, at its record's state now when it cites that
// record, else at its availability; its fields as historyFields says,
// each noted in its kind's classes (a brief's item in its footprint's)
// and its fields' beyond them; its anchor only when it resolves within the
// grant (refReads), noted as it is.
func (hs *historyScope) turnEntry(ctx context.Context, out servedEntry, e history.Entry, scope, cited brakeman.Ref, res brakeman.Resolution) (turnHistoryEntry, []brakeman.Read, error) {
	te := turnHistoryEntry{
		Ref: cited.String(), Kind: out.Kind, Class: out.Class, Outcome: out.Outcome, Title: out.Title, Summary: out.Summary,
		Snippet: out.Snippet, At: out.At, Actor: out.Actor, Source: turnSource{Store: out.Source.Store, Label: out.Source.Label},
		Host: out.Host, Project: out.Project, Revision: out.Revision, Agent: out.Agent, Crew: out.Crew, Run: out.Run, Ticket: out.Ticket,
		URL: out.URL, Availability: out.Availability,
	}
	var reads []brakeman.Read
	if out.Anchor.Ref != "" {
		rds, ok, err := hs.refReads(ctx, out.Anchor.Ref)
		if err != nil {
			return te, nil, err
		}
		if ok {
			a := out.Anchor
			te.Anchor = &a
			reads = rds
		}
	}
	state := out.Availability.State
	if cited == scope {
		state = res.Now.State
	}
	classes, _ := entryClasses(e)
	if scope.Kind == brakeman.RefBrief {
		classes = hs.footprint(scope)
	}
	classes = append(slices.Clone(classes), emitFields(&te, hs.o.sees)...)
	return te, append(readsOf(cited, classes, state), reads...), nil
}

// historyRestriction is the rows a read may answer at all, which the
// index draws them by (history.Restriction): the kinds it may be served
// (historyKinds, every class of each seen), each placed within every scope
// of the grant by one column its rows carry, as brakeman.Scope.Admits
// places their records, and drawn by that column's values alone, so the
// read never reads a row outside it, in what it answers or in its cost:
//   - a run's part by its run's crew, its run, its repository or the
//     project of the ticket the run was launched from; a decision's only
//     where an asking task is its anchor (a plan's gate, whose question the
//     plan revision's author wrote, is left out);
//   - a session's entries by its repository or its own id (a plain
//     session's and a crew member's alike), a crew member's alone by its
//     run's crew or its run; none under a scope of projects, which no
//     column of theirs places;
//   - a ticket's entries by its project, none under a scope of runs or
//     crews; a launch's by its project or the run it names, a delivery's
//     by that run, neither under a scope of repositories (no column of
//     theirs holds the run's), a delivery not under one of projects (nor
//     the run's project); none an act made (its actor);
//   - a brief's items, each then admitted on its footprint (resolveBrief).
//
// A place the scope puts on two columns at once (a run's rows under a
// scope of repositories and crews) is not drawn at all: one column a
// place. The admin's read is what a turn with every class and no scope is
// served.
func historyRestriction(o *observer) *history.Restriction {
	sees, scopes := o.sees, o.grant.scopes
	if o.tp == nil {
		sees, scopes = func(string) bool { return true }, nil
	}
	var repos, sessions, crews, runs, projects selector
	plainOK, memberOK := true, true
	for _, s := range scopes {
		refs := make([]string, len(s.Sessions))
		for i, id := range s.Sessions {
			refs[i] = hSessionRef(id)
		}
		repos, sessions, crews, runs, projects = repos.and(s.Repos), sessions.and(refs), crews.and(s.Crews), runs.and(s.Runs), projects.and(s.Projects)
		if len(s.Sessions) == 0 && len(s.Crews)+len(s.Runs)+len(s.Projects) > 0 {
			plainOK = false
		}
		if len(s.Projects) > 0 {
			memberOK = false
		}
	}
	// A run's id names its crew: runs and crews together are the runs of
	// those crews, a crew alone the runs whose id names it.
	ofRun := runs.as("run")
	if crews.set && runs.set {
		ofRun.values = slices.DeleteFunc(ofRun.values, func(r string) bool { return !slices.Contains(crews.values, crewOfRun(r)) })
	} else if crews.set {
		ofRun = crews.as("run.crew")
	}
	tree, anchor, project := repos.as("tree"), sessions.as("anchor"), projects.as("ticket.project")
	r := &history.Restriction{Clauses: []history.Clause{}}
	add := func(kinds []string, keep []history.Cond, by ...placed) {
		place, ok := placeOf(by...)
		if !ok || len(kinds) == 0 {
			return
		}
		r.Clauses = append(r.Clauses, history.Clause{Kinds: kinds, Place: place, Keep: keep})
	}
	seen := func(place string, ok func(string) bool) []string {
		var out []string
		for _, k := range slices.Sorted(maps.Keys(historyKinds)) {
			hk := historyKinds[k]
			if hk.place == place && ok(k) && !slices.ContainsFunc(hk.classes, func(c string) bool { return !sees(c) }) {
				out = append(out, k)
			}
		}
		return out
	}
	not := func(set ...string) func(string) bool { return func(k string) bool { return !slices.Contains(set, k) } }
	only := func(set ...string) func(string) bool { return func(k string) bool { return slices.Contains(set, k) } }
	asked := []history.Cond{{Column: "anchor.kind", Values: []string{brakeman.RefTask}}}
	add(seen(placeRun, not(historyDecisionKinds...)), nil, ofRun, tree, project)
	add(seen(placeRun, only(historyDecisionKinds...)), asked, ofRun, tree, project)
	switch {
	case plainOK && memberOK && !crews.set && !runs.set:
		add(seen(placeSession, not()), nil, tree, anchor)
	case memberOK && !plainOK && (crews.set || runs.set):
		// A crew member's alone: its run's column places it.
		if place, ok := placeOf(tree, anchor, ofRun); ok && place != nil && (place.Column == "run" || place.Column == "run.crew") {
			add(seen(placeSession, not()), nil, ofRun)
		}
	}
	act := []history.Cond{{Column: "actor", Values: historyActors, Not: true}}
	if !crews.set && !runs.set {
		add(seen(placeTicket, not(historyNamesRun...)), act, project)
	}
	if !repos.set {
		add(seen(placeTicket, only("launch")), act, project, ofRun)
		if !projects.set {
			add(seen(placeTicket, only("delivery.verified")), act, ofRun)
		}
	}
	add(seen(placeBrief, not()), nil)
	return r
}

// historyDecisionKinds are the kinds of a decision's rows, whose anchor is
// its asking task, or its run for a plan's gate (decisionAnchor).
var historyDecisionKinds = []string{"decision", "decision.response"}

// historyNamesRun are a ticket's kinds whose rows name a run (launch,
// delivery): placed by that run too.
var historyNamesRun = []string{"launch", "delivery.verified"}

// selector is one selector of the grant's scopes: set when a scope names
// it, its values those every scope that names it names.
type selector struct {
	set    bool
	values []string
}

func (s selector) and(vs []string) selector {
	switch {
	case len(vs) == 0:
		return s
	case !s.set:
		return selector{set: true, values: slices.Clone(vs)}
	}
	return selector{set: true, values: slices.DeleteFunc(slices.Clone(s.values), func(v string) bool { return !slices.Contains(vs, v) })}
}

// placed is a selector as the column it places rows by.
type placed struct {
	column string
	selector
}

func (s selector) as(column string) placed { return placed{column, s} }

// placeOf is the one column of by that is set, as a place (nil when none
// is: every row of the kinds); ok false when two are, which no index
// draws by.
func placeOf(by ...placed) (*history.Cond, bool) {
	var place *history.Cond
	for _, p := range by {
		if !p.set {
			continue
		}
		if place != nil {
			return nil, false
		}
		place = &history.Cond{Column: p.column, Values: slices.Clone(p.values)}
	}
	return place, true
}

// historyStores is nil when every store a read may resolve a row in is
// open (or one the server keeps none of): the ledger, Dispatch's store,
// the session journal and, for a turn, Brakeman's; else the first that
// is not, which the read answers as a whole before it looks at a row
// (store_unavailable), never because of which rows matched.
func historyStores(o *observer) error {
	rr := o.rr
	if _, err := rr.ledger(); err != nil {
		return err
	}
	if _, err := rr.dispatch(); err != nil {
		return err
	}
	if s := rr.s; s.sessionRecords == nil && s.store != nil && !s.cfg.Switchyard.Enabled {
		return &errRefStore{what: "the session records"}
	}
	if o.tp != nil && rr.desk == nil {
		return &errRefStore{what: "Brakeman's store"}
	}
	return nil
}

// historySealer seals a history read's continuations under a key of the
// server's, made at its first use; a restart makes another, and the
// continuations given before it are refused.
type historySealer struct {
	once sync.Once
	aead cipher.AEAD
	err  error
}

func (hs *historySealer) get() (cipher.AEAD, error) {
	hs.once.Do(func() {
		key := make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			hs.err = err
			return
		}
		block, err := aes.NewCipher(key)
		if err != nil {
			hs.err = err
			return
		}
		hs.aead, hs.err = cipher.NewGCM(block)
	})
	return hs.aead, hs.err
}

// continuationData binds a continuation to the read it reads on: its
// caller (holder) and its line, each with its length.
func continuationData(holder, line string) []byte {
	return fmt.Appendf(nil, "conductor brakeman history next\x00%d:%s%d:%s", len(holder), holder, len(line), line)
}

// historyHolder is who a continuation is given to: the admin, or a turn
// by its request and session.
func historyHolder(o *observer) string {
	if o.tp == nil {
		return "admin"
	}
	return "turn " + o.tp.req.ID + " " + o.tp.session
}

// sealContinuation is the continuation of holder's read of line at pos,
// the index's cursor after the reply's last entry: pos sealed (AES-GCM),
// so its holder can neither read it nor make one, and it reads on only
// the read it was given for, on this server.
func (s *Server) sealContinuation(holder, line, pos string) (string, error) {
	aead, err := s.historySeal.get()
	if err != nil {
		return "", err
	}
	nonce := make([]byte, aead.NonceSize(), aead.NonceSize()+len(pos)+aead.Overhead())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(aead.Seal(nonce, nonce, []byte(pos), continuationData(holder, line))), nil
}

// openContinuation is the index's cursor a continuation sealed for
// holder's read of line: ok false for anything else.
func (s *Server) openContinuation(holder, line, next string) (string, bool) {
	aead, err := s.historySeal.get()
	if err != nil {
		return "", false
	}
	raw, err := base64.RawURLEncoding.DecodeString(next)
	if err != nil || len(raw) < aead.NonceSize()+aead.Overhead() {
		return "", false
	}
	pos, err := aead.Open(nil, raw[:aead.NonceSize()], raw[aead.NonceSize():], continuationData(holder, line))
	if err != nil {
		return "", false
	}
	return string(pos), true
}

// handleBrakemanHistory answers GET /api/brakeman/history?q=&limit=&after=:
// the History search's entries the caller may read, newest first, at most
// limit (1 to maxHistoryTurnPage, defaultHistoryTurnPage when absent) and
// maxObserveReply bytes, next among them, {entries, next?, cut?, indexing:
// {state}}. The query's words and filters are the search's
// (handleHistorySearch), its words matching and never ranking, Brakeman's
// own runs and sessions left out (a turn's always, the admin's unless the
// line names a crew or the kind brief.item), and the read answered from
// the rows its restriction covers (historyRestriction; the admin's, every
// kind a turn may be served), drawn by their own columns, at most
// maxHistoryTurnExamine of them: cut "examined" when they held less than a
// page. next, on a reply with entries, reads on after its last entry
// (sealed for the caller and the line); a reply with none carries none.
func (s *Server) handleBrakemanHistory(w http.ResponseWriter, r *http.Request, o *observer) {
	h, ok := s.historyRead(w, r, "q", "limit", "after")
	if !ok {
		return
	}
	defer h.release()
	q, ix, ctx := h.q, h.ix, h.ctx
	line, err := q.one("q")
	var after string
	if err == nil {
		after, err = q.one("after")
	}
	var limit int64
	if err == nil {
		limit, err = q.number("limit")
	}
	if err == nil && (limit < 0 || limit > maxHistoryTurnPage) {
		err = fmt.Errorf("limit is 1 to %d, not %d", maxHistoryTurnPage, limit)
	}
	if err != nil {
		h.refuse(http.StatusBadRequest, "bad_query", err.Error())
		return
	}
	if limit == 0 {
		limit = defaultHistoryTurnPage
	}
	if len(after) > maxHistoryTurnAfter {
		h.refuse(http.StatusBadRequest, "bad_cursor", fmt.Sprintf("the cursor is %d bytes, more than %d", len(after), maxHistoryTurnAfter))
		return
	}
	holder, cursor := historyHolder(o), ""
	if after != "" {
		var ok bool
		if cursor, ok = s.openContinuation(holder, line, after); !ok {
			h.refuse(http.StatusBadRequest, "bad_cursor", "the cursor is not a next this read of this line gave")
			return
		}
	}
	hq, err := history.ParseQuery(line)
	if err != nil {
		h.fail(err)
		return
	}
	if !o.sees(brakeman.ClassDispatch) {
		// A filter on a field the turn is not given (emitFields) would
		// read it all the same, by which entries it finds.
		for _, name := range []string{"ticket", "project"} {
			if _, ok := hq.Filter(name); ok {
				h.refuse(http.StatusForbidden, "not_visible", name+": filters on Dispatch's records, which this turn does not see")
				return
			}
		}
	}
	_, crews := hq.Filter("crew")
	if kinds, _ := hq.Filter("kind"); o.tp != nil || (!crews && !slices.Contains(kinds, "brief.item")) {
		// A turn never reads Brakeman's own runs and sessions (admitRef),
		// whatever the line names; the admin's default leaves them out.
		hq.ExceptCrews = []string{crew.ClassBrakeman}
	}
	hq.Restrict, hq.Newest = historyRestriction(o), true
	if o.tp != nil && o.sees(brakeman.ClassDispatch) && s.brakeman != nil {
		// A filter on Dispatch's fields reads them, by which entries it
		// finds, whether it finds any or not.
		for _, name := range []string{"ticket", "project"} {
			if _, ok := hq.Filter(name); ok {
				s.brakeman.usedClass(o.tp.req.ID, brakeman.ClassDispatch)
			}
		}
	}
	// The indexing's state alone: how far the index reaches, and what it
	// dropped, are counts of rows a turn may not see.
	indexing := func(pageState string) map[string]any {
		return map[string]any{"state": s.historyIndexing(ctx, nil, pageState)["state"]}
	}
	if ix == nil {
		h.reply(http.StatusOK, map[string]any{"entries": []any{}, "indexing": indexing("")})
		return
	}
	if err := historyStores(o); err != nil {
		s.observeError(h.w, err)
		return
	}
	av := s.newAvailability(ctx, ix, s.ledger)
	defer av.close()
	hs := &historyScope{o: o, done: map[brakeman.Ref]historyAdmission{}, noted: map[brakeman.Read]bool{}}
	bound := maxObserveReply
	if b := s.historyTurnReply.Load(); b > 0 {
		bound = int(b)
	}
	out := []turnHistoryEntry{}
	used, examine, cut := historyReplyReserve, maxHistoryTurnExamine, false
	last, state := "", ""
	for done := false; !done; {
		if examine <= 0 {
			cut = true
			break
		}
		hq.Limit, hq.After, hq.Examine = historyTurnPage, cursor, examine
		page, err := ix.Search(ctx, hq)
		if err != nil {
			h.fail(err)
			return
		}
		state, examine = page.State, examine-page.Drawn
		for i, hit := range page.Entries {
			if len(out) == int(limit) {
				done = true
				break
			}
			scope, cited, res, ok, err := hs.admit(ctx, hit.Entry)
			var te turnHistoryEntry
			var reads []brakeman.Read
			if err == nil && ok {
				served := s.serveEntry(av, hit.Entry, hit.Snippet, history.Repeats{}, history.Links{}, false)
				te, reads, err = hs.turnEntry(ctx, served, hit.Entry, scope, cited, res)
			}
			if err != nil {
				if ctx.Err() != nil {
					s.observeError(h.w, ctx.Err())
					return
				}
				// A row that cannot be judged is left out as a refused
				// one is: the reply never turns on which rows matched.
				s.log.Warn("a history entry was left out of a Brakeman read: it could not be judged", "ref", hit.Entry.Ref, "err", err)
				continue
			}
			if !ok {
				continue
			}
			b, _ := json.Marshal(te)
			if len(out) > 0 && used+len(b)+1 > bound {
				done = true
				break
			}
			used += len(b) + 1
			hs.note(reads)
			out = append(out, te)
			last = page.After(i)
		}
		if !done {
			if page.Next == "" {
				break
			}
			if page.Cut == history.CutExamined {
				cut = true
				break
			}
			cursor = page.Next
		}
	}
	reply := map[string]any{"entries": out, "indexing": indexing(state)}
	if cut && len(out) < int(limit) {
		// The rows the reply may examine are spent short of a page: the
		// line is to be narrowed (a time window, a kind).
		reply["cut"] = history.CutExamined
	}
	if len(out) > 0 && last != "" {
		// After the last entry served, whether or not the index holds
		// more: whether it does is a count of rows the caller may not see.
		next, err := s.sealContinuation(holder, line, last)
		if err != nil {
			s.observeError(h.w, err)
			return
		}
		if len(next) <= maxHistoryTurnAfter {
			reply["next"] = next
		}
	}
	h.reply(http.StatusOK, reply)
}

// ---- the session journal as the resolver's third source

// recordRef reads the record of the session id from the journal, for a
// session the registry does not list: ok false when the server keeps no
// journal (no data directory, a switchyard) or the journal holds no
// record of id; an error for a journal the server keeps and could not
// open (as the ledger and Dispatch's store answer one), or a record it
// could not read, neither of which is its absence.
func (rr *refResolver) recordRef(id string) (journal.Record, bool, error) {
	j := rr.s.sessionRecords
	switch {
	case j == nil && (rr.s.store == nil || rr.s.cfg.Switchyard.Enabled):
		return journal.Record{}, false, nil
	case j == nil:
		return journal.Record{}, false, &errRefStore{what: "the session records"}
	}
	rec, _, err := j.Get(id)
	switch {
	case errors.Is(err, journal.ErrNotFound):
		return journal.Record{}, false, nil
	case err != nil:
		return journal.Record{}, false, fmt.Errorf("the record of session %s: %w", id, err)
	}
	return rec, true, nil
}

// recordRepo is the repository a record's session ran in, as the
// resolver's top answers a live session's: the worktree's top the
// session read at launch (Repo), a folder in it; "" for a hosted record
// (its folder is on its host, as a live hosted session's is), for none,
// for a top its folder is not in (a working tree a repository's settings
// put elsewhere, which the resolver's top answers as none too), and for a
// folder or a top the journal may have cut at its bound
// (journal.CwdMayBeCut), which names no repository of the session's.
func recordRepo(rec journal.Record) string {
	if rec.Host.Hosted || rec.Kind == "hosted" || rec.Repo == "" || journal.CwdMayBeCut(rec.Cwd) || journal.CwdMayBeCut(rec.Repo) {
		// A folder at the journal's bound may be cut, to its parent or
		// another folder: no identity of the session's repository.
		return ""
	}
	dir := rec.Cwd
	if real, err := filepath.EvalSymlinks(dir); err == nil {
		dir = real
	} else if _, serr := os.Lstat(dir); serr == nil {
		// There, and its links could not be followed: not a folder of
		// the top as far as anyone can tell here.
		return ""
	}
	if dir != rec.Repo && !folderWithin(dir, rec.Repo) {
		return ""
	}
	return rec.Repo
}

// recordState is a record's state as a ref's now: record, or lost when
// the server stopped observing its session while it ran.
func recordState(rec journal.Record) string {
	if rec.Ending == journal.EndingLost || rec.Ending == journal.EndingHostAway || !rec.LostAt.IsZero() {
		return availLost
	}
	return availRecord
}

// recordURL is a session record's page.
func recordURL(id string) string { return "/history/sessions/" + url.PathEscape(id) }
