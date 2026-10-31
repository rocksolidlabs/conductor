package api

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/rocksolidlabs/conductor/internal/agents"
	"github.com/rocksolidlabs/conductor/internal/catalog"
	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/session"
	"github.com/rocksolidlabs/conductor/internal/share"
)

const (
	// runTimeout bounds a launch or a member start: making the worktrees and
	// starting the sessions. It runs to its end when the client goes away:
	// the run is the server's, and GET /api/runs finds it. The prompts are
	// typed afterwards, on the run's own context.
	runTimeout = 2 * time.Minute
	// maxMemberBody bounds the body of POST /api/runs/{run}/members: a member
	// at the crew limits, a prompt of 4000 characters and 8 KiB of arguments,
	// takes up to about 72 KiB of JSON.
	maxMemberBody = 128 << 10
	// maxBroadcast bounds the text of a broadcast, in bytes, once made one
	// line (broadcastLine).
	maxBroadcast = 4096
)

// Why a broadcast skips a member (broadcastSkip.Reason): the reasons a run
// chat's send is not typed either (session.ErrNotSent).
const (
	skipNeedsInput = session.NotSentNeedsInput
	skipNotRunning = session.NotSentNotRunning
	skipUnknown    = session.NotSentUnknown
	skipNoEnter    = session.NotSentNoEnter
)

// broadcastTimeout bounds a broadcast's submissions, which go on when the
// client goes away: one cut in its pause would leave a line without its Enter.
const broadcastTimeout = 15 * time.Second

// Launch starts the session of a crew member through createLocalSession, the
// path POST /api/sessions takes (crew.Launcher). An attempt's tree's own
// names (spec.Inject) and the env names the server approved for its
// project manifest (spec.Allow) go into the session's environment. An
// error is an *apiError.
func (s *Server) Launch(ctx context.Context, spec crew.LaunchSpec) (*session.Local, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	yolo := spec.Yolo
	ref := spec.Ref
	// The run's chat: the session is made with it, so that its first viewer finds it, and joins it once it exists.
	room := s.runs.ChatRoom(ref.RunID)
	name, renamed := s.memberSessionName(spec)
	local, aerr := s.createLocalSession(createSessionRequest{AgentID: spec.AgentID, Name: name, Cwd: spec.Cwd, Args: spec.Args, Env: spec.Env, Yolo: &yolo, resume: spec.Resume, resumedFrom: spec.ResumedFrom,
		renamed: renamed, runChat: room, reservation: spec.Reservation, inject: spec.Inject, allow: spec.Allow}, &ref)
	if aerr != nil {
		return nil, aerr
	}
	if room != nil {
		room.Join(local, spec.Name)
	}
	if yolo && !local.Info().Yolo {
		if a, ok := s.Catalog().Get(spec.AgentID); ok {
			s.runs.Note(ref.RunID, session.ActivityStatus, spec.Name+": "+noYoloRecipe(a))
		}
	}
	return local, nil
}

// memberSessionName is the name a member's session is launched under: the
// member's, or, for a resume of a session a person renamed (PATCH
// /api/sessions/{id}), or of one resumed from such a session, that name,
// which the new session carries on as renamed. A session that nobody renamed
// resumes under the member's name, whatever it was launched as (a session
// adopted as a crew's self keeps its own launch name until then). The member
// keeps its own name in the run: its chat, its messages and its handoffs go
// by it.
func (s *Server) memberSessionName(spec crew.LaunchSpec) (name string, renamed bool) {
	if spec.ResumedFrom != "" {
		if old, ok := s.lookupLocal(spec.ResumedFrom); ok {
			if l := old.Launched(); l.Renamed && l.Name != "" {
				return l.Name, true
			}
		}
	}
	return spec.Name, false
}

// Reserve holds a place for a managed run's attempt among the server's
// sessions (crew.Launcher): refused at the cap before anything is made.
func (s *Server) Reserve(ctx context.Context) (*session.Reservation, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return s.registry.Reserve()
}

// lookupLocal finds a server session in the registry, for the run engine.
func (s *Server) lookupLocal(id string) (*session.Local, bool) {
	d, ok := s.registry.Get(id)
	if !ok {
		return nil, false
	}
	l, ok := d.(*session.Local)
	return l, ok
}

// checkLaunch reports the first member whose agent the catalog lacks, whose
// agent's program is not installed on this server (installed, the server's
// lookups), whose program the identity probe found to be another one
// (identity, nil to skip; a pending or failed probe refuses nothing), or who
// is given arguments its agent does not take, so that a launch refuses it
// before any session starts. The error matches crew.ErrInvalid.
func checkLaunch(members []crew.Member, cat catalog.Catalog, installed func(program string) bool, identity func(a catalog.Agent) *Identity) error {
	if err := (crew.Crew{Members: members}).CheckAgents(cat); err != nil {
		return err
	}
	for _, m := range members {
		a, _ := cat.Get(m.AgentID)
		if !installed(a.Command[0]) {
			return fmt.Errorf("%w: member %q: agent %q is not installed on the server (%s was not found)", crew.ErrInvalid, m.Name, m.AgentID, a.Command[0])
		}
		if identity != nil {
			if id := identity(a); id != nil && id.Misidentified() {
				return fmt.Errorf("%w: member %q: agent %q on the server is not %s (%s --version printed %q)", crew.ErrInvalid, m.Name, m.AgentID, id.Name, a.Command[0], id.Output)
			}
		}
		if len(m.Args) > 0 && !a.AllowArgs {
			return fmt.Errorf("%w: member %q: agent %q takes no extra arguments", crew.ErrInvalid, m.Name, m.AgentID)
		}
	}
	return nil
}

// installedFor looks up the programs of the members' agents in cat together
// (warm), so that a launch waits about one lookup's time for them, at most
// lookupWait, and answers checkLaunch from those answers: a program whose
// lookup did not answer in time, or before ctx ended, counts as installed (a
// relative path with a separator always does, since it resolves against the
// session's directory).
func (s *Server) installedFor(ctx context.Context, cat catalog.Catalog, members []crew.Member) func(program string) bool {
	var programs []string
	for _, m := range members {
		if a, ok := cat.Get(m.AgentID); ok && len(a.Command) > 0 {
			programs = append(programs, a.Command[0])
		}
	}
	answers := s.lookups.warm(ctx, programs)
	return func(program string) bool { return answers[program] }
}

// identityFor runs (or reads) the identity probes of the members' agents
// together, for checkLaunch: a probe not answered within probeWait is
// pending, which refuses nothing.
func (s *Server) identityFor(ctx context.Context, cat catalog.Catalog, members []crew.Member) func(a catalog.Agent) *Identity {
	var programs []string
	for _, m := range members {
		if a, ok := cat.Get(m.AgentID); ok && len(a.Command) > 0 && a.Probed() && agents.ProbeFor(a.Adapter) != nil {
			programs = append(programs, a.Command[0])
		}
	}
	paths := s.lookups.warmPaths(ctx, programs)
	var mu sync.Mutex
	var wg sync.WaitGroup
	ids := map[string]*Identity{}
	for _, m := range members {
		a, ok := cat.Get(m.AgentID)
		if !ok || len(a.Command) == 0 {
			continue
		}
		wg.Go(func() {
			id := s.identityOf(ctx, a, paths[a.Command[0]].path, false)
			mu.Lock()
			ids[a.ID] = id
			mu.Unlock()
		})
	}
	wg.Wait()
	return func(a catalog.Agent) *Identity { return ids[a.ID] }
}

// runContext is the context of a launch or a start: the request's values,
// not its end, bounded by runTimeout.
func runContext(r *http.Request) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(r.Context()), runTimeout)
}

// handleLaunchCrew launches a saved crew as a run: 201 {run} once the session
// of every member that starts immediately exists; each prompt is typed once
// its session is ready.
// launchBody is the optional body of POST /api/crews/{id}/launch: the run's own name.
type launchBody struct {
	Label string `json:"label"`
}

// maxLaunchBody bounds that body.
const maxLaunchBody = 4 << 10

func (s *Server) handleLaunchCrew(w http.ResponseWriter, r *http.Request) {
	var body launchBody
	if err := decodeOptionalJSON(w, r, &body, maxLaunchBody); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	label := strings.TrimSpace(body.Label)
	if err := crew.ValidateLabel(label); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if s.crews == nil {
		writeError(w, http.StatusServiceUnavailable, "store_unavailable", "no data directory is configured")
		return
	}
	c, err := s.crews.Get(r.PathValue("id"))
	if err != nil {
		s.crewStoreError(w, r.PathValue("id"), err)
		return
	}
	if c.Managed() {
		// A managed run needs the server's ledger, as its owner, and the
		// crews.managed switch on (the engine checks the ledger again).
		if !s.cfg.ManagedCrews() {
			writeError(w, http.StatusConflict, "managed_off", "crews with tasks are off on this server (crews.managed)")
			return
		}
		if s.ledger == nil {
			writeError(w, http.StatusServiceUnavailable, "ledger_unavailable", "the ledger could not be opened; see the server's log")
			return
		}
		if ro, why := s.ledger.ReadOnly(); ro {
			writeError(w, http.StatusServiceUnavailable, "ledger_unavailable", why)
			return
		}
	}
	if err := c.Launchable(); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_crew", err.Error())
		return
	}
	cat := s.Catalog()
	if err := checkLaunch(c.Members, cat, s.installedFor(r.Context(), cat, c.Members), s.identityFor(r.Context(), cat, c.Members)); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_crew", err.Error())
		return
	}
	cwd, err := s.resolveCwd(cmp.Or(c.Cwd, s.cfg.DefaultCwd))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_cwd", err.Error())
		return
	}
	c.Cwd = cwd
	// The run's yolo choice: the crew's, else the server's, fixed now.
	yolo := s.cfg.Yolo
	if c.Yolo != nil {
		yolo = *c.Yolo
	}
	c.Yolo = &yolo
	ctx, cancel := runContext(r)
	defer cancel()
	run, release, err := s.runs.LaunchNamed(ctx, c, label)
	// Released once the reply is written: the view link is minted first, so a
	// launch at the cap meanwhile cannot forget the run.
	defer release()
	if err != nil {
		s.runError(w, "launch", c.ID, err)
		return
	}
	s.log.Info("crew launched", "crew", c.ID, "run", run.ID)
	reply := map[string]any{"run": run}
	if c.ViewLinkTTLSeconds > 0 {
		if view := s.launchViewLink(r, run.ID, time.Duration(c.ViewLinkTTLSeconds)*time.Second); view != nil {
			reply["viewLink"] = view
			// The run as it is now: its log has the link's entry.
			if now, ok := s.runs.Get(run.ID); ok {
				reply["run"] = now
			}
		}
	}
	writeJSON(w, http.StatusCreated, reply)
}

// launchViewLink creates the view link a crew asks for at launch, as POST
// /api/runs/{run}/links would, and returns it as that route's reply: the
// token is in the launch reply and nowhere else. A link the store refuses
// does not fail the launch, which has happened: it is logged and noted in
// the run's log, and the reply has no viewLink.
func (s *Server) launchViewLink(r *http.Request, runID string, ttl time.Duration) map[string]any {
	var (
		link  *share.Link
		token string
		err   error
	)
	if !s.runs.IfKept(runID, func() { link, token, err = s.links.CreateRunLink(runID, session.RoleView, "launch", ttl) }) {
		return nil
	}
	if err != nil {
		s.log.Warn("launch view link not created", "run", runID, "err", err)
		s.runs.Note(runID, session.ActivityError, "the view link could not be created: "+err.Error())
		return nil
	}
	s.runs.Note(runID, session.ActivityLink, "link created: "+linkLabelOr(link.Label)+" ("+string(link.Role)+")")
	return s.linkReply(r, link, token)
}

func (s *Server) handleListRuns(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"runs": s.runs.List()})
}

// handleGetRun answers one run with the diff of every member's branch; a
// run the engine no longer holds (ended before the server last started, or
// evicted) is answered from its record (crew.Records), marked `record`, so
// that its page opens read-only: no diffs, nothing of it live, and its
// actions (stop, resume, a member added or started, a broadcast, its links)
// answer 404 as for any run the engine does not hold.
func (s *Server) handleGetRun(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("run")
	if run, ok := s.runs.GetWithDiffs(r.Context(), id); ok {
		writeJSON(w, http.StatusOK, map[string]any{"run": run})
		return
	}
	if s.records != nil {
		run, ok, err := s.records.Get(id)
		if err != nil {
			s.log.Warn("run record", "run", id, "err", err)
		}
		if ok {
			writeJSON(w, http.StatusOK, map[string]any{"run": run, "record": true})
			return
		}
	}
	writeError(w, http.StatusNotFound, "not_found", "no such run")
}

// handleAddRunMember adds a member to a run: 201 {run}. A member that starts
// immediately has its session by then.
func (s *Server) handleAddRunMember(w http.ResponseWriter, r *http.Request) {
	var m crew.Member
	if err := decodeJSONLimit(w, r, &m, maxMemberBody); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if err := m.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_crew", err.Error())
		return
	}
	cat := s.Catalog()
	if err := checkLaunch([]crew.Member{m}, cat, s.installedFor(r.Context(), cat, []crew.Member{m}), s.identityFor(r.Context(), cat, []crew.Member{m})); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_crew", err.Error())
		return
	}
	id := r.PathValue("run")
	ctx, cancel := runContext(r)
	defer cancel()
	if err := s.runs.AddMember(ctx, id, m); err != nil {
		s.runError(w, "add member", id, err)
		return
	}
	s.writeRun(w, http.StatusCreated, id)
}

// handleStartRunMember starts a pending member by hand: 200 {run}.
func (s *Server) handleStartRunMember(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("run")
	ctx, cancel := runContext(r)
	defer cancel()
	if err := s.runs.StartMember(ctx, id, r.PathValue("name")); err != nil {
		s.runError(w, "start member", id, err)
		return
	}
	s.writeRun(w, http.StatusOK, id)
}

// handleStopRun stops every member of a run: 200 {run}, marked stopped. The
// worktrees stay. A session that did not stop cleanly is logged; the run is
// stopped all the same.
func (s *Server) handleStopRun(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("run")
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 30*time.Second)
	defer cancel()
	switch err := s.runs.Stop(ctx, id); {
	case errors.Is(err, crew.ErrRunNotFound):
		writeError(w, http.StatusNotFound, "not_found", "no such run")
		return
	case err != nil:
		s.log.Warn("run stop: a session did not stop cleanly", "run", id, "err", err)
	}
	s.log.Info("run stopped", "run", id)
	s.writeRun(w, http.StatusOK, id)
}

// broadcastRequest is the body of POST /api/runs/{run}/broadcast: the text to
// type, the members to type it into (every member when empty) and the display
// name of the admin who sends it.
type broadcastRequest struct {
	Text    string   `json:"text"`
	Members []string `json:"members"`
	ByName  string   `json:"byName"`
}

// broadcastSkip is a member a broadcast was not typed into, and why.
type broadcastSkip struct {
	Member string `json:"member"`
	Reason string `json:"reason"`
}

// broadcastLine is the line a broadcast types, less its carriage return: the
// text with its line breaks and tabs made spaces, as a handoff's message is,
// so that it is one line, without its other control characters, trimmed. A
// handoff's message has had its control characters dropped by the session
// that recorded it; a broadcast's text comes as the client sent it.
func broadcastLine(text string) string {
	return strings.TrimSpace(strings.Map(func(r rune) rune {
		switch {
		case r == '\r' || r == '\n' || r == '\t':
			return ' '
		case unicode.IsControl(r):
			return -1
		}
		return r
	}, text))
}

// handleBroadcast submits a line into the members of a run a request names,
// or into every member: 200 {sent, skipped}, both in the order asked, a name
// given twice typed once. Each member's line goes in as Local.Submit types
// (a paste, then Enter 250 ms later), all at once, recorded as input by the
// admin's display name. A member whose session waits on a prompt is skipped
// (needs_input), as is one not running (not_running), a name no member has
// (unknown), and one whose Enter was left out because a prompt came up during
// the pause (no_enter): its line waits in its input, never typed again.
func (s *Server) handleBroadcast(w http.ResponseWriter, r *http.Request) {
	var req broadcastRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	line := broadcastLine(req.Text)
	switch {
	case line == "":
		writeError(w, http.StatusBadRequest, "invalid_request", "text is empty")
		return
	case len(line) > maxBroadcast:
		writeError(w, http.StatusBadRequest, "invalid_request", fmt.Sprintf("text is longer than %d bytes", maxBroadcast))
		return
	}
	run, ok := s.runs.Get(r.PathValue("run"))
	if !ok {
		writeError(w, http.StatusNotFound, "not_found", "no such run")
		return
	}
	names := req.Members
	if len(names) == 0 {
		for _, m := range run.Members {
			names = append(names, m.Name)
		}
	}
	byName := session.CleanName(req.ByName)
	var unique []string
	seen := map[string]bool{}
	for _, name := range names {
		if !seen[name] {
			seen[name] = true
			unique = append(unique, name)
		}
	}
	// Each member's submission pauses before its Enter: they go together,
	// one per member, and the reply keeps the order asked.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), broadcastTimeout)
	defer cancel()
	reasons := make([]string, len(unique))
	var wg sync.WaitGroup
	for i, name := range unique {
		wg.Add(1)
		go func() {
			defer wg.Done()
			reasons[i] = s.broadcastTo(ctx, run, name, line, byName)
		}()
	}
	wg.Wait()
	sent, skipped := []string{}, []broadcastSkip{}
	for i, name := range unique {
		if reasons[i] == "" {
			sent = append(sent, name)
		} else {
			skipped = append(skipped, broadcastSkip{Member: name, Reason: reasons[i]})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"sent": sent, "skipped": skipped})
}

// broadcastTo submits line into the member of run with the given name, unless
// its session waits on a prompt, and returns why it did not, or "".
func (s *Server) broadcastTo(ctx context.Context, run crew.Run, name, line, byName string) string {
	i := slices.IndexFunc(run.Members, func(m crew.MemberState) bool { return m.Name == name })
	if i < 0 {
		return skipUnknown
	}
	m := run.Members[i]
	if m.Status != crew.MemberRunning {
		return skipNotRunning
	}
	local, ok := s.lookupLocal(m.SessionID)
	if !ok {
		return skipNotRunning
	}
	res, err := local.Submit(ctx, session.Submission{Text: line, ByName: byName, UnlessWaiting: true})
	switch {
	case errors.Is(err, session.ErrSubmitPending):
		// Accepted: entered as the member's program reads.
		return ""
	case err != nil && !res.Typed:
		// It ended, or the write failed as its process went.
		if !errors.Is(err, session.ErrSessionEnded) {
			s.log.Warn("broadcast: could not type into a member", "run", run.ID, "member", name, "err", err)
		}
		return skipNotRunning
	case err != nil:
		s.log.Warn("broadcast: typed into a member without its Enter", "run", run.ID, "member", name, "err", err)
		return skipNoEnter
	case !res.Typed:
		return skipNeedsInput
	case !res.Entered:
		return skipNoEnter
	}
	return ""
}

// writeRun answers the run with the given ID, or 404 when it is gone.
func (s *Server) writeRun(w http.ResponseWriter, status int, id string) {
	run, ok := s.runs.Get(id)
	if !ok {
		writeError(w, http.StatusNotFound, "not_found", "no such run")
		return
	}
	writeJSON(w, status, map[string]any{"run": run})
}

// runError answers an error of the run engine. A member's session that could
// not be created answers as POST /api/sessions would, its message naming the
// member; git missing from the server's PATH (crew.ErrNoGit) is 500
// launch_failed with its message, as is anything else unforeseen.
func (s *Server) runError(w http.ResponseWriter, what, id string, err error) {
	var aerr *apiError
	switch {
	case errors.Is(err, crew.ErrRunNotFound):
		writeError(w, http.StatusNotFound, "not_found", "no such run")
	case errors.Is(err, crew.ErrMemberNotFound):
		writeError(w, http.StatusNotFound, "not_found", "no such member in the run")
	case errors.Is(err, crew.ErrInvalid):
		writeError(w, http.StatusBadRequest, "invalid_crew", err.Error())
	case errors.Is(err, crew.ErrNotRepo):
		writeError(w, http.StatusConflict, "not_a_repo", err.Error())
	case errors.Is(err, crew.ErrNoGit):
		s.log.Warn("crew "+what+" failed: git is not on the server's PATH", "id", id)
		writeError(w, http.StatusInternalServerError, "launch_failed", err.Error())
	case errors.Is(err, crew.ErrMemberStarted):
		writeError(w, http.StatusConflict, "member_started", err.Error())
	case errors.Is(err, crew.ErrRunStopped):
		writeError(w, http.StatusConflict, "run_stopped", err.Error())
	case errors.Is(err, crew.ErrRunRunning):
		writeError(w, http.StatusConflict, "run_running", err.Error())
	case errors.Is(err, crew.ErrRunUnsettled):
		writeError(w, http.StatusServiceUnavailable, "ledger_unavailable", err.Error())
	case errors.Is(err, crew.ErrRunResumed):
		writeError(w, http.StatusConflict, "already_resumed", err.Error())
	case errors.Is(err, crew.ErrMemberRunning):
		writeError(w, http.StatusConflict, "still_running", err.Error())
	case errors.Is(err, crew.ErrManaged):
		writeError(w, http.StatusConflict, "managed_run", err.Error())
	case errors.As(err, &aerr):
		writeError(w, aerr.status, aerr.Code, err.Error())
	default:
		s.log.Warn("crew "+what+" failed", "id", id, "err", err)
		writeError(w, http.StatusInternalServerError, "launch_failed", err.Error())
	}
}

// resumeRecord resumes the run with the given ID from its record
// (Engine.ResumeRecord), and reports whether it answered: false for no
// record. The record is read and checked under a lease on it, so two
// requests at once make one continuation (the other is refused as being
// resumed); 409 already_resumed, naming the continuation, for a record
// resumed already. The continuation's record is written before the source
// is marked with it, so the source never names a run no record holds (the
// records' open repairs the other order, a continuation recorded while its
// source is not yet marked); then 201 {run}.
func (s *Server) resumeRecord(w http.ResponseWriter, ctx context.Context, id string) bool {
	key := "record:" + id
	if !s.takeResume(key) {
		writeError(w, http.StatusConflict, "already_resumed", "the run is being resumed")
		return true
	}
	defer s.releaseResume(key)
	rec, ok, err := s.records.Get(id)
	if err != nil {
		s.log.Warn("run record", "run", id, "err", err)
	}
	if !ok {
		return false
	}
	if rec.ResumedBy != "" {
		writeJSON(w, http.StatusConflict, map[string]any{"error": map[string]any{"code": "already_resumed", "message": "the run was resumed already, as " + rec.ResumedBy, "run": rec.ResumedBy}})
		return true
	}
	if by := s.recordMark(id); by != "" {
		// Resumed already, its mark not yet on the record: tried again, and
		// the continuation named.
		s.markRecord(rec, by)
		writeJSON(w, http.StatusConflict, map[string]any{"error": map[string]any{"code": "already_resumed", "message": "the run was resumed already, as " + by, "run": by}})
		return true
	}
	var run *crew.Run
	if rec.Managed != nil {
		// A managed run's record is a cache of the ledger, which the
		// continuation reads (#334, row 8).
		run, err = s.runs.ContinueRun(ctx, id, rec.Label)
	} else {
		run, err = s.runs.ResumeRecord(ctx, rec)
	}
	if err != nil {
		s.runError(w, "resume run from its record", id, err)
		return true
	}
	s.recordContinuation(ctx, run.ID)
	s.markRecord(rec, run.ID)
	writeJSON(w, http.StatusCreated, map[string]any{"run": run})
	return true
}

// recordContinuation writes the record of a run just made by a resume and
// waits for it (recordsGrace at most, under ctx): the source's mark
// (resumedBy) follows it, never the other way round.
func (s *Server) recordContinuation(ctx context.Context, runID string) {
	if s.records == nil {
		return
	}
	s.runs.Journal(runID)
	wctx, cancel := context.WithTimeout(ctx, recordsGrace)
	defer cancel()
	if !s.runs.WaitRecorded(wctx) {
		s.log.Warn("the continuation's record is still being written", "run", runID)
	}
}

// markRecord marks the record rec with its continuation by (ResumedBy) and
// writes it. A write that fails leaves the claim in memory (recordMarks),
// for the next resume of the record to answer the continuation and try
// again, so a failed write never lets a record be resumed twice.
func (s *Server) markRecord(rec crew.Run, by string) {
	s.resumingMu.Lock()
	if s.recordMarks == nil {
		s.recordMarks = map[string]string{}
	}
	s.recordMarks[rec.ID] = by
	s.resumingMu.Unlock()
	rec.ResumedBy = by
	if err := s.records.Save(rec); err != nil {
		s.log.Warn("run record not marked with its continuation; the mark is kept here", "run", rec.ID, "continuation", by, "err", err)
		return
	}
	s.resumingMu.Lock()
	delete(s.recordMarks, rec.ID)
	s.resumingMu.Unlock()
}

// recordMark is the continuation a record was resumed as whose mark is not
// on the record yet, "" for none.
func (s *Server) recordMark(id string) string {
	s.resumingMu.Lock()
	defer s.resumingMu.Unlock()
	return s.recordMarks[id]
}

// handleResumeRun starts a new run of a stopped run's crew in which every
// member with a resumable conversation continues it in its kept worktree
// (Engine.ResumeRun): 201 {run}.
func (s *Server) handleResumeRun(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("run")
	ctx, cancel := runContext(r)
	defer cancel()
	run, err := s.runs.ResumeRun(ctx, id)
	if errors.Is(err, crew.ErrManaged) {
		// A managed run is continued (#334, row 8): its results kept, the
		// rest dispatched again.
		run, err = s.runs.ContinueRun(ctx, id, "")
	}
	if errors.Is(err, crew.ErrRunNotFound) && s.records != nil {
		// A run the engine no longer holds is resumed from its record,
		// once: the record then names the continuation, as a held run does.
		if s.resumeRecord(w, ctx, id) {
			return
		}
	}
	if err == nil {
		// Its record first, before the source's journal can name it: the
		// source never names a run no record holds.
		s.recordContinuation(ctx, run.ID)
	}
	var resumed *crew.RunResumedError
	if errors.As(err, &resumed) {
		// Resumed already, or being: 409 naming the continuation, which the
		// page opens in place of making another.
		body := map[string]any{"code": "already_resumed", "message": err.Error()}
		if resumed.By != "" {
			body["run"] = resumed.By
		}
		writeJSON(w, http.StatusConflict, map[string]any{"error": body})
		return
	}
	if err != nil {
		s.runError(w, "resume run", id, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"run": run})
}
