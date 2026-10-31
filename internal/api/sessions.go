package api

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/rocksolidlabs/conductor/internal/agents"
	"github.com/rocksolidlabs/conductor/internal/catalog"
	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/proto"
	"github.com/rocksolidlabs/conductor/internal/pty"
	"github.com/rocksolidlabs/conductor/internal/session"
	"github.com/rocksolidlabs/conductor/internal/share"
)

var errShuttingDown = errors.New("server shutting down")

type createSessionRequest struct {
	AgentID string   `json:"agentId"`
	Name    string   `json:"name"`
	Cwd     string   `json:"cwd"`
	Args    []string `json:"args"`
	Cols    uint16   `json:"cols"`
	Rows    uint16   `json:"rows"`
	// Yolo overrides the server's yolo default for this launch: nil follows
	// it, false launches without the agent's yolo recipe, true with it.
	Yolo *bool `json:"yolo,omitempty"`
	// resume, set by Resume, is the agent session id the launch resumes with
	// the agent's recipe; resumedFrom the session it resumes or relaunches.
	resume, resumedFrom string
	// renamed, set by a resume, says Name is one a person renamed the
	// session it resumes to, or its own resumed from (session.Launched.Renamed).
	renamed bool
	// runChat, set by Launch, is the chat of the run the session is a member of.
	runChat *session.ChatRoom
	// reservation is the place reserved for the session among the server's
	// (Registry.Reserve), for a managed run's attempt; nil otherwise.
	reservation *session.Reservation
	// inject and allow, set by Launch for an attempt whose run has a
	// project manifest, are its tree's own names (crew.LaunchSpec.Inject:
	// its ports, its state directory, its preview), set after the crew's
	// as Conductor's own are, and the manifest's env names the server
	// approved (LaunchSpec.Allow), passed on beside the passthrough.
	inject map[string]string
	allow  []string
	// Env is set in the process over the agent's own variables: the GOAL of
	// a crew member. JSON cannot set it; a client sending "env" is refused
	// like one sending any other unknown field.
	Env map[string]string `json:"-"`
}

func (s *Server) handleListSessions(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"sessions": s.registry.List()})
}

func (s *Server) handleCreateSession(w http.ResponseWriter, r *http.Request) {
	var req createSessionRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if agent, ok := s.Catalog().Get(req.AgentID); ok {
		if aerr := s.notTheAgent(agent); aerr != nil {
			writeAPIError(w, aerr)
			return
		}
	}
	local, aerr := s.createLocalSession(req, nil)
	if aerr != nil {
		writeAPIError(w, aerr)
		return
	}
	if agent, ok := s.Catalog().Get(req.AgentID); ok {
		s.noteIdentity(local, agent)
	}
	writeJSON(w, http.StatusCreated, local.Info())
}

// crewInject is what a crew member's process learns of its place from the
// environment: its crew, run and member, and, for an attempt at a task of
// a managed run (#334), the task, the attempt's number and the task file
// the attempt reads (.conductor/task.md in its worktree). Hints, never
// authority: the server knows which attempt a session is.
func crewInject(ref *session.CrewRef) map[string]string {
	if ref == nil {
		return nil
	}
	env := map[string]string{"CONDUCTOR_CREW": ref.CrewID, "CONDUCTOR_RUN": ref.RunID, "CONDUCTOR_MEMBER": ref.Member}
	if ref.Task != "" {
		env["CONDUCTOR_TASK"] = ref.Task
		env["CONDUCTOR_ATTEMPT"] = strconv.Itoa(ref.Attempt)
		env["CONDUCTOR_TASK_FILE"] = filepath.Join(".conductor", "task.md")
	}
	return env
}

// createLocalSession starts a server session: the agent looked up in the
// catalog, its arguments checked, its working directory through resolveCwd,
// its hooks injected and its environment built, then the session registered
// and announced. It is the one path every server session takes, a crew
// member's too: crewRef, when set, tags the session with its run (Info.Crew)
// and gives its process CONDUCTOR_CREW, CONDUCTOR_RUN and CONDUCTOR_MEMBER.
func (s *Server) createLocalSession(req createSessionRequest, crewRef *session.CrewRef) (*session.Local, *apiError) {
	agent, ok := s.Catalog().Get(req.AgentID)
	if !ok {
		return nil, newAPIError(http.StatusBadRequest, "invalid_agent", "unknown agent id")
	}
	if len(req.Args) > 0 && !agent.AllowArgs {
		return nil, newAPIError(http.StatusBadRequest, "args_not_allowed", "this agent does not accept extra arguments")
	}
	if len(req.Args) > 64 {
		return nil, newAPIError(http.StatusBadRequest, "invalid_request", "too many arguments")
	}
	for _, a := range req.Args {
		if strings.ContainsRune(a, 0) || len(a) > 4096 {
			return nil, newAPIError(http.StatusBadRequest, "invalid_request", "invalid argument")
		}
	}
	cwdInput := req.Cwd
	if cwdInput == "" {
		cwdInput = agent.Cwd
	}
	if cwdInput == "" {
		cwdInput = s.cfg.DefaultCwd
	}
	folder, err := s.openCwd(cwdInput)
	if err != nil {
		return nil, newAPIError(http.StatusBadRequest, "invalid_cwd", err.Error())
	}
	// The session takes the folder; a launch that stops before closes it.
	keepFolder := false
	defer func() {
		if !keepFolder {
			folder.Close()
		}
	}()
	cwd := folder.Path()
	name := session.CutSessionName(req.Name)
	if name == "" {
		name = fmt.Sprintf("%s #%d", agent.Name, s.nextCounter())
	}
	cols, rows := req.Cols, req.Rows
	if cols == 0 {
		cols = 80
	}
	if rows == 0 {
		rows = 24
	}
	if !proto.ValidDimension(cols) || !proto.ValidDimension(rows) {
		return nil, newAPIError(http.StatusBadRequest, "invalid_request", "terminal size out of range")
	}
	// An agent with a screen pattern gets the detector. The catalog held the
	// pattern to the same rules when it took the agent in.
	sig := agent.EffectiveSignal()
	var pattern *regexp.Regexp
	if sig.Kind == catalog.SignalPattern {
		if pattern, err = catalog.CompilePattern(sig.Pattern); err != nil {
			return nil, newAPIError(http.StatusInternalServerError, "invalid_agent", "the agent's signal pattern is invalid")
		}
	}
	// An agent that reports through hooks gets them at launch: its adapter's
	// flags after the command and the user's arguments, and its adapter's
	// environment under the agent's own, so a variable the agent sets keeps
	// its value. The hooks are the assets `conductor serve` wrote at startup.
	// What the request sets (a crew's GOAL) goes over both.
	// Yolo: the launch's choice, else the server's; applied when the agent
	// has a recipe, whose arguments follow the user's and whose environment
	// goes over the agent's own, through the same filtered environment, never
	// with Conductor's own variables. An agent with a trust override (Codex)
	// trusts the launch's repository for this launch alone.
	yolo := s.cfg.Yolo
	if req.Yolo != nil {
		yolo = *req.Yolo
	}
	applied := yolo && !agent.Yolo.Empty()
	extra, adapterEnv := agents.InjectFor(agent.Adapter, agents.HooksDir(s.cfg.DataDir), sig, applied)
	// The agent's own session: resumed by its recipe, or named at launch when
	// the recipe lets Conductor choose; its arguments go right after the
	// command, as Codex resumes with a subcommand.
	argv := append([]string{}, agent.Command...)
	var agentSession *session.AgentSession
	switch r := agent.Session; {
	case req.resume != "":
		// One live session per agent conversation: the conversation is
		// taken now, before the process starts, and let go as this returns
		// (registered, the session itself holds it; failed, a retry may).
		if !s.takeResume(req.resume) {
			return nil, newAPIError(http.StatusConflict, "already_resumed", "a running session holds that conversation, or one is being made for it")
		}
		defer s.releaseResume(req.resume)
		argv = append(argv, catalog.Expand(r.ResumeArgs, req.resume)...)
		agentSession = &session.AgentSession{ID: req.resume, Resumable: true, Source: session.AgentSessionResumed}
	case !r.Empty() && len(r.StartArgs) > 0:
		id := newAgentSessionID(r.NewID)
		argv = append(argv, catalog.Expand(r.StartArgs, id)...)
		agentSession = &session.AgentSession{ID: id, Source: session.AgentSessionSet}
	}
	argv = append(argv, req.Args...)
	var yoloEnv map[string]string
	if applied {
		argv = append(argv, agent.Yolo.Args...)
		yoloEnv = agent.Yolo.Env
		argv = append(argv, agents.TrustArgsFor(agent.Adapter, func() []string {
			ctx, cancel := context.WithTimeout(context.Background(), gitCheckTimeout)
			defer cancel()
			return crew.RepoRoots(ctx, cwd)
		})...)
	}
	argv = append(argv, extra...)
	// Conductor's MCP server, for the agents that take one at launch.
	if s.cfg.MCP() {
		argv = append(argv, agents.MCPFor(agent.Adapter, agents.HooksDir(s.cfg.DataDir))...)
	}
	env := agent.Env
	if len(adapterEnv) > 0 || len(yoloEnv) > 0 || len(req.Env) > 0 {
		env = maps.Clone(adapterEnv)
		if env == nil {
			env = map[string]string{}
		}
		maps.Copy(env, agent.Env)
		maps.Copy(env, yoloEnv)
		maps.Copy(env, req.Env)
	}
	var trust *regexp.Regexp
	if agent.TrustPrompt != "" {
		if trust, err = catalog.CompilePattern(agent.TrustPrompt); err != nil {
			return nil, newAPIError(http.StatusInternalServerError, "invalid_agent", "the agent's trust prompt is invalid")
		}
	}
	// A window title that says the agent works (Codex's, with the title its
	// adapter's launch asks for) shows the session working.
	var workingTitle *regexp.Regexp
	if sig.WorkingTitle != "" {
		if workingTitle, err = catalog.CompileWorkingTitle(sig.WorkingTitle); err != nil {
			return nil, newAPIError(http.StatusInternalServerError, "invalid_agent", "the agent's working title is invalid")
		}
	}
	var questions []session.StartupQuestion
	for _, q := range agent.Questions {
		re, err := catalog.CompilePattern(q.Prompt)
		if err != nil {
			return nil, newAPIError(http.StatusInternalServerError, "invalid_agent", "one of the agent's startup questions is invalid")
		}
		var words, mode, typed *regexp.Regexp
		if q.Words != "" {
			if words, err = catalog.CompilePattern(q.Words); err != nil {
				return nil, newAPIError(http.StatusInternalServerError, "invalid_agent", "one of the agent's startup questions is invalid")
			}
		}
		if q.ModeAnswers != "" {
			if mode, err = catalog.CompilePattern(q.ModeAnswers); err != nil {
				return nil, newAPIError(http.StatusInternalServerError, "invalid_agent", "one of the agent's startup questions is invalid")
			}
		}
		if q.TypedAnswers != "" {
			if typed, err = catalog.CompilePattern(q.TypedAnswers); err != nil {
				return nil, newAPIError(http.StatusInternalServerError, "invalid_agent", "one of the agent's startup questions is invalid")
			}
		}
		questions = append(questions, session.StartupQuestion{Pattern: re, Words: words, Anytime: q.Anytime, Answers: trustAnswers(q.Answers), FromScreen: q.AnswersFromScreen && q.Anytime, Mode: mode, Typed: typed})
	}
	id := session.NewID()
	agentToken, _ := share.NewToken()
	notifyURL := s.notifyBase() + "/api/sessions/" + id + "/attention"
	// The binary the hooks run, for what the agent runs itself (the skill).
	bin, _ := agents.Binary()
	inject := pty.Inject(id, notifyURL, agentToken, bin)
	// Its agent's id (what a crew it forms names for itself) and the skill
	// (for an agent without a skills directory to read); the skill goes
	// into the agent's own skills directory first.
	inject["CONDUCTOR_AGENT"] = agent.ID
	hooksDir := agents.HooksDir(s.cfg.DataDir)
	if skill := agents.SkillFile(hooksDir); skill != "" {
		inject["CONDUCTOR_SKILL"] = skill
	}
	s.installSkillAtLaunch(agent.Adapter, hooksDir)
	// BuildEnv keeps CONDUCTOR_* out of env: a crew's travel with Inject's,
	// and an attempt's tree's after them (the engine's own names).
	maps.Copy(inject, crewInject(crewRef))
	maps.Copy(inject, req.inject)
	// The agent's envPassthrough adds to the server-wide list, and an
	// attempt's approved env names to both; the clone keeps one launch from
	// growing the list another launch reads.
	passthrough := append(slices.Clone(s.cfg.EnvPassthrough), agent.EnvPassthrough...)
	passthrough = append(passthrough, req.allow...)
	launchHook("checked")
	// The process starts in the folder opened and checked, not at its path.
	dirFile, err := folder.Dir()
	if err != nil {
		return nil, newAPIError(http.StatusBadRequest, "invalid_cwd", "working directory does not exist")
	}
	procEnv := pty.BuildEnv(pty.ParentEnv(), passthrough, env, inject)
	// An agent whose environment moves its configuration directory (a copy
	// of Claude Code under a CLAUDE_CONFIG_DIR of its own, #291) reads its
	// skills there: the skill goes there too.
	s.installSkillInConfigDir(agent.Adapter, agents.ConfigDirOf(agent.Adapter, pty.Lookup(procEnv)), hooksDir)
	proc, err := pty.Start(pty.Spec{
		Argv:    argv,
		Dir:     cwd,
		DirFile: dirFile,
		Env:     procEnv,
		Cols:    cols,
		Rows:    rows,
	})
	dirFile.Close()
	if err != nil {
		s.log.Warn("session start failed", "agent", agent.ID, "err", err)
		return nil, newAPIError(http.StatusBadGateway, "start_failed", "could not start the agent process")
	}
	info := session.Info{
		ID:           id,
		Name:         name,
		Kind:         session.KindServer,
		AgentID:      agent.ID,
		Command:      argv,
		Cwd:          cwd,
		Status:       session.StatusRunning,
		Cols:         cols,
		Rows:         rows,
		Branch:       session.GitBranch(cwd),
		Yolo:         applied,
		AgentSession: agentSession,
		ResumedFrom:  req.resumedFrom,
		CreatedAt:    time.Now().UTC(),
	}
	if crewRef != nil {
		ref := *crewRef
		info.Crew = &ref
	}
	local := session.NewLocal(info, proc, session.Options{
		ScrollbackBytes: s.cfg.ScrollbackBytes,
		MaxViewers:      s.cfg.MaxViewersPerSession,
		FileView:        string(s.cfg.FileView),
		FileEdit:        string(s.cfg.FileEdit),
		FileDenyFunc:    s.fileDeny,
		Transport:       proto.TransportWS,
		Log:             s.log,
		OnChange:        s.localChange,
		OnActivity:      s.localActivity,
		OnChat:          s.localChat,
		RunChat:         req.runChat,
		Pattern:         pattern,
		TrustPattern:    trust,
		TrustAnswers:    trustAnswers(agent.TrustAnswers),
		Questions:       questions,
		WorkingTitle:    workingTitle,
		ConfirmSubmit:   agents.ConfirmsSubmit(agent.Adapter, sig),
		WatchGit:        true,
		Launched:        session.Launched{AgentID: agent.ID, Name: name, Cwd: cwd, Args: slices.Clone(req.Args), Env: maps.Clone(req.Env), Yolo: yolo, Renamed: req.renamed},
		Folder:          folder,
		// Where the agent keeps its plans outside the folder (#289), from
		// its adapter and the environment the process was given: the
		// directory the agent itself writes to.
		Plans: agents.PlansDir(agent.Adapter, pty.Lookup(procEnv), cwd),
	})
	local.SetAgentToken(agentToken)
	if err := s.registry.AddReserved(local, req.reservation); err != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = local.Stop(ctx)
		return nil, newAPIError(http.StatusConflict, "too_many_sessions", "session limit reached")
	}
	keepFolder = true
	s.log.Info("session started", "session", id, "agent", agent.ID, "pid", proc.PID(), "yolo", applied)
	if yolo && !applied {
		local.Record(session.ActivityEntry{Type: session.ActivityStatus, Message: noYoloRecipe(agent)})
	}
	// Published while listed, as every change is (localChange): a session
	// that ended and was removed at once is not brought back by its launch.
	s.events.publishLatest(id, s.listedInfo)
	s.publish(local)
	return local, nil
}

// noYoloRecipe is the notice of a launch with yolo on whose agent has no yolo
// recipe: it is launched as it would be without.
func noYoloRecipe(agent catalog.Agent) string {
	return "yolo is on, but " + agent.Name + " has no yolo recipe: launched without one"
}

// localChange is the OnChange hook of a server session: the change goes to
// the event hub, and to the runs, whose waiting handoffs a member's change
// may let go (a prompt cleared records no activity entry). The runs' hook
// never waits.
// localChat is the OnChat hook of a server session: its chat reaches the
// browsers' unread counts over the events stream.
func (s *Server) localChat(id string, m session.ChatMessage) {
	s.events.chat(id, "", m)
	if pub := s.publishedOf(id); pub != nil {
		pub.OnChat(id, m)
	}
}

// listed says whether the registry holds a session with id. The event hub
// asks it under its own lock (publishIf); the registry never calls the hub
// under its lock.
func (s *Server) listed(id string) bool {
	_, ok := s.registry.Get(id)
	return ok
}

// listedInfo is the Info of a server session the registry lists, as it is
// now; false for one it does not (removed, or not yet registered). The event
// hub asks it under its own lock (publishLatest).
func (s *Server) listedInfo(id string) (session.Info, bool) {
	l, ok := s.lookupLocal(id)
	if !ok {
		return session.Info{}, false
	}
	return l.Info(), true
}

// localChange is the OnChange hook of a server session. Every change reaches
// the runs, the session's links and its publication; the browsers hear only
// of a session the server still lists, so a viewer leaving one that was
// removed does not bring it back.
func (s *Server) localChange(info session.Info) {
	// The browsers get the session as it is when the hub sends it, not the
	// snapshot handed here, which a later change may already have overtaken.
	s.events.publishLatest(info.ID, s.listedInfo)
	s.runs.OnChange(info)
	if info.Status.Ended() {
		s.endLinks(info.ID)
	}
	if pub := s.publishedOf(info.ID); pub != nil {
		pub.OnChange(info)
	}
}

func (s *Server) nextCounter() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.counter++
	return s.counter
}

// launchHookFn, when set (by a test), runs as a launch's working directory
// is "resolved" (its path, by resolveCwd) and once it is "checked" (opened
// through its allowed root), before the process starts (launchHook).
var launchHookFn atomic.Pointer[func(stage string)]

func launchHook(stage string) {
	if fn := launchHookFn.Load(); fn != nil {
		(*fn)(stage)
	}
}

// openCwd is resolveCwd for a launch: cwd resolved and checked by path as
// resolveCwd does, then opened through the allowed root it lies under
// (session.OpenFolderIn, an os.Root), so that the folder opened is under
// that root whatever a link on its way does meanwhile. The session's
// process starts in that folder and its Files tab serves it, whatever its
// path names later.
func (s *Server) openCwd(cwd string) (*session.Folder, error) {
	real, err := s.resolveCwd(cwd)
	if err != nil {
		return nil, err
	}
	launchHook("resolved")
	for _, root := range s.cfg.AllowedRoots {
		rootReal, err := filepath.EvalSymlinks(root)
		if err != nil {
			continue
		}
		rel, err := filepath.Rel(rootReal, real)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		// The root as checked: the folder is opened only through that one.
		want, err := os.Stat(rootReal)
		if err != nil {
			continue
		}
		launchHook("root")
		if f, err := session.OpenFolderIn(rootReal, rel, want); err == nil {
			return f, nil
		}
	}
	return nil, errors.New("working directory is outside the allowed roots")
}

// resolveCwd makes cwd absolute, follows symlinks, and requires it to be an
// existing directory under one of the allowed roots. A launch opens it
// through its root (openCwd); a check alone (a resume's, the pickers')
// takes the path.
func (s *Server) resolveCwd(cwd string) (string, error) {
	abs, err := filepath.Abs(cwd)
	if err != nil {
		return "", errors.New("invalid working directory")
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", errors.New("working directory does not exist")
	}
	fi, err := os.Stat(real)
	if err != nil || !fi.IsDir() {
		return "", errors.New("working directory is not a directory")
	}
	for _, root := range s.cfg.AllowedRoots {
		rootReal, err := filepath.EvalSymlinks(root)
		if err != nil {
			continue
		}
		if real == rootReal || strings.HasPrefix(real, rootReal+string(filepath.Separator)) {
			return real, nil
		}
	}
	return "", errors.New("working directory is outside the allowed roots")
}

func (s *Server) handleGetSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	p := s.authenticate(r, true)
	// A switchyard's workbench token reads what its listing says, with no role.
	if p.role(id) == "" && !p.admin {
		if !s.limiter.allow(clientKey(r)) {
			writeError(w, http.StatusTooManyRequests, "rate_limited", "too many requests")
			return
		}
		writeError(w, http.StatusUnauthorized, "unauthorized", "no access to this session")
		return
	}
	d, ok := s.registry.Get(id)
	if !ok {
		writeError(w, http.StatusNotFound, "not_found", "no such session")
		return
	}
	out := map[string]any{"session": d.Info(), "role": p.role(id)}
	if p.admin {
		out["links"] = s.sessionLinks(id, d)
	}
	writeJSON(w, http.StatusOK, out)
}

// handleDeleteSession stops a running session; a second DELETE on an ended
// session removes it from the listing.
func (s *Server) handleDeleteSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	d, ok := s.registry.Get(id)
	if !ok {
		writeError(w, http.StatusNotFound, "not_found", "no such session")
		return
	}
	if d.Info().Status.Ended() {
		s.registry.Remove(id)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 15*time.Second)
	defer cancel()
	if err := d.Stop(ctx); err != nil {
		s.log.Warn("stop failed", "session", id, "err", err)
		writeError(w, http.StatusInternalServerError, "stop_failed", "could not stop the session")
		return
	}
	writeJSON(w, http.StatusOK, d.Info())
}

// renameRequest is the body of PATCH /api/sessions/{id}.
type renameRequest struct {
	Name string `json:"name"`
}

// maxRenameBody bounds a rename's body: a name cut to proto.MaxSessionName
// bytes, with room for one sent longer, which is cut.
const maxRenameBody = 4 << 10

// handleRenameSession renames a server session, running or ended: 200 with
// its Info. The change reaches the events stream as a session event (the
// session's OnChange), and a resume of the session starts under the new name
// (Local.Launched; a member's through Launch). A hosted session is named by
// its host.
func (s *Server) handleRenameSession(w http.ResponseWriter, r *http.Request) {
	var req renameRequest
	if err := decodeJSONLimit(w, r, &req, maxRenameBody); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	d, ok := s.registry.Get(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "not_found", "no such session")
		return
	}
	local, ok := d.(*session.Local)
	if !ok {
		writeError(w, http.StatusBadRequest, "hosted_session", "a hosted session is named by its host")
		return
	}
	name := session.CleanSessionName(req.Name)
	if name == "" {
		writeError(w, http.StatusBadRequest, "invalid_name", "the name is empty without its control characters and surrounding space")
		return
	}
	local.Rename(name)
	writeJSON(w, http.StatusOK, local.Info())
}

// installSkillAtLaunch puts the Conductor skill where the adapter's agent
// reads skills, in the server user's home, once per adapter since the
// server started (agents.installSkill, on by default): a SKILL.md of the
// user's own is left to them, said once in the log. Nothing here stops the
// launch.
func (s *Server) installSkillAtLaunch(adapter, hooksDir string) {
	if adapter == "" || s.home == "" || !s.cfg.InstallSkill() {
		return
	}
	s.skillMu.Lock()
	done := s.skillDone[adapter]
	s.skillDone[adapter] = true
	s.skillMu.Unlock()
	if done {
		return
	}
	path, changed, err := agents.InstallSkill(adapter, s.home, hooksDir)
	switch {
	case path == "":
	case errors.Is(err, agents.ErrByHand):
		s.log.Info("the agent's skills directory holds a SKILL.md of the user's own, which stays; conductor skill prints Conductor's", "adapter", adapter, "path", path)
	case err != nil:
		s.log.Warn("skill install failed", "adapter", adapter, "path", path, "err", err)
	case changed:
		s.log.Info("skill installed", "adapter", adapter, "path", path)
	}
}

// installSkillInConfigDir puts the Conductor skill under dir, the
// configuration directory of its own the adapter's agent is launched with
// (agents.ConfigDirOf; "" for none), once per adapter and directory since the
// server started, as installSkillAtLaunch does for the home. Nothing here
// stops the launch.
func (s *Server) installSkillInConfigDir(adapter, dir, hooksDir string) {
	if adapter == "" || dir == "" || !s.cfg.InstallSkill() {
		return
	}
	key := adapter + "\x00" + dir
	s.skillMu.Lock()
	done := s.skillDone[key]
	s.skillDone[key] = true
	s.skillMu.Unlock()
	if done {
		return
	}
	path, changed, err := agents.InstallSkillIn(adapter, dir, hooksDir)
	switch {
	case path == "":
	case errors.Is(err, agents.ErrByHand):
		s.log.Info("the agent's configuration directory holds a SKILL.md of the user's own, which stays; conductor skill prints Conductor's", "adapter", adapter, "path", path)
	case err != nil:
		s.log.Warn("skill install failed", "adapter", adapter, "path", path, "err", err)
	case changed:
		s.log.Info("skill installed", "adapter", adapter, "path", path)
	}
}

// trustAnswers are the catalog's answers to the agent's trust question as the
// session's quick-reply choices for it.
func trustAnswers(in []catalog.Answer) []session.Option {
	out := make([]session.Option, 0, len(in))
	for _, a := range in {
		out = append(out, session.Option{Label: a.Label, Input: a.Input})
	}
	return out
}
