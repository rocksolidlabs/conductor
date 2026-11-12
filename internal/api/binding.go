package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/rocksolidlabs/conductor/internal/agents"
	"github.com/rocksolidlabs/conductor/internal/capacity"
	"github.com/rocksolidlabs/conductor/internal/catalog"
	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
	"github.com/rocksolidlabs/conductor/internal/pty"
	"github.com/rocksolidlabs/conductor/internal/session"
)

// A launch's login (docs/plans/capacity.md, PR-4). createLocalSession is
// the one place a server session's environment is built, a crew member's
// and a Brakeman turn's as much as a person's, and bindLogin, called there
// alone, decides the login its process gets: the account named (or the
// adapter's default at this moment), whose folder it sets as the adapter's
// folder variable and nowhere else, or the login the agent's entry or the
// launch's own additions set. What it decides is the session's binding
// (session.BoundAccount, in its Launched), and the binding's Login is the
// fingerprint of the environment the process starts with, read from it
// after it is built. A launch made again under a binding (a resume) builds
// its environment the same way and starts only when it gets the same
// login, compared before any process exists; never another.

// loginNames are the variables whose values say where adapter's login
// comes from: its folder variable, then its login variables (authEnv), in
// the order Login hashes them.
func loginNames(adapter string) []string {
	ad, _ := agents.Get(adapter)
	var names []string
	if ad.ConfigDirEnv != "" {
		names = append(names, ad.ConfigDirEnv)
	}
	return append(names, authEnv[adapter]...)
}

// loginOf is the fingerprint of adapter's login in an environment (lookup,
// pty.Lookup's) of a process started in cwd: a SHA-256 under the prefix
// conductor-login/1 of the adapter and each of loginNames with its value
// or a mark that it is unset, the folder variable's as the process sees
// it (seenFolder). It holds no value: only the hash is kept.
func loginOf(adapter, cwd string, lookup func(string) string) string {
	ad, _ := agents.Get(adapter)
	h := sha256.New()
	fmt.Fprintf(h, "conductor-login/1\x00%s\x00", adapter)
	for _, n := range loginNames(adapter) {
		v := lookup(n)
		if n == ad.ConfigDirEnv {
			v = seenFolder(v, cwd)
		}
		if v != "" {
			fmt.Fprintf(h, "%s=set:%d:%s\x00", n, len(v), v)
		} else {
			fmt.Fprintf(h, "%s=unset\x00", n)
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}

// seenFolder is the folder a folder variable set to v names for a process
// started in cwd: v when it is whole (or cwd is not known), else v under
// cwd, as the tool reads it.
func seenFolder(v, cwd string) string {
	if v == "" || filepath.IsAbs(v) || cwd == "" {
		return v
	}
	return filepath.Join(cwd, v)
}

// keptLogin is adapter's Login of a binding whose only login variable is
// its folder ("" for none): a binding of a registered account, the one a
// session's record keeps (journal.Launch).
func keptLogin(adapter, folder string) string {
	ad, _ := agents.Get(adapter)
	return loginOf(adapter, "", func(n string) string {
		if n == ad.ConfigDirEnv {
			return folder
		}
		return ""
	})
}

// loginWant is what a launch asks of its login, besides the account the
// request names (createSessionRequest.Account: "" for the adapter's
// default at this moment, "default" for the tool's own login, or a
// registered account's id): Brakeman's configured folder (folder), or,
// for a launch made again, the binding it must get again (bound), of
// which a session's record keeps the id and the source alone (kept)
// beside the adapter it was started as (adapter).
type loginWant struct {
	folder  string
	bound   *session.BoundAccount
	kept    bool
	adapter string
}

// launchEnv is the environment a launch of agent is started with, but for
// Conductor's own names (pty.Inject's, the crew's, a tree's): the part its
// entry and the server give (agentEnv over adapterEnv, which a usage read
// takes too), what the launch adds over it (its yolo recipe's variables
// when applied, set's, the allow names beside the passthrough, which is
// agentEnv's clone so one launch never grows the list another reads), then
// the login bindLogin decides over all of them, for a process started in
// cwd ("" while it is not known). createLocalSession starts every session
// with it, and bindMembers binds a member by it, as its session will be
// started.
func (s *Server) launchEnv(agent catalog.Agent, adapterEnv map[string]string, applied bool, set map[string]string, allow []string, account, cwd string, want loginWant) ([]string, map[string]string, session.BoundAccount, func(), *apiError) {
	passthrough, env := s.agentEnv(agent, adapterEnv)
	entry := pty.BuildEnv(pty.ParentEnv(), passthrough, env, nil)
	if applied {
		maps.Copy(env, agent.Yolo.Env)
	}
	maps.Copy(env, set)
	passthrough = append(passthrough, allow...)
	bound, release, aerr := s.bindLogin(agent, account, cwd, want, entry, pty.BuildEnv(pty.ParentEnv(), passthrough, env, nil), env)
	if aerr != nil {
		if was := want.bound; was != nil && aerr.Code == "invalid_account" {
			aerr = accountGone(*was, session.BoundAccount{ID: was.ID, Source: was.Source})
		}
		return nil, nil, session.BoundAccount{}, nil, aerr
	}
	return passthrough, env, bound, release, nil
}

// bindLogin decides the login a launch of agent, started in cwd, gets,
// from the environment its entry and the server give (entry, built as
// pty.BuildEnv builds it) and the one the launch's own additions give over
// it (full), and sets env, the variables the process is started with, for
// it. What is wanted is resolved first: an account's id (the one named, or
// the adapter's default at this moment), or a folder (Brakeman's, set as
// the adapter's folder variable over the entry's), which is the registered
// account that holds it or none. Then one judgement, the same for every
// want, decides the binding from the environment: a login variable set
// besides the folder wanted makes it the login its launch (launch_env) or
// its entry (pinned) sets; else it is the tool's own login, the registered
// account, or the folder no account holds. So a registered account is
// bound only when its folder is the one login variable the process gets,
// and one named beside another is refused. A new launch binds no retired
// account (Reserve); a launch made again holds the one it was bound to
// (Hold). The binding answered has no Login yet (createLocalSession reads
// it from the environment built). Release lets go of the account's
// reservation, once the session is registered or its launch failed.
func (s *Server) bindLogin(agent catalog.Agent, account, cwd string, want loginWant, entry, full []string, env map[string]string) (session.BoundAccount, func(), *apiError) {
	nothing := func() {}
	refuse := func(words string) (session.BoundAccount, func(), *apiError) {
		return session.BoundAccount{}, nil, newAPIError(http.StatusBadRequest, "invalid_account", words)
	}
	ad, _ := agents.Get(agent.Adapter)
	id, folder := account, want.folder
	if b := want.bound; b != nil {
		id = b.ID
		if b.Source == session.SourceUnlisted {
			folder = b.Folder
		}
	}
	if folder != "" && ad.ConfigDirEnv == "" {
		return session.BoundAccount{}, nil, newAPIError(http.StatusBadRequest, "invalid_agent", "the agent takes no account folder")
	}
	// The login variables the environment holds besides a folder wanted:
	// the first its entry or the server's passthrough sets (pinned), the
	// first the launch's own additions set or change (added).
	el, fl := pty.Lookup(entry), pty.Lookup(full)
	pinned, added := "", ""
	for _, n := range loginNames(agent.Adapter) {
		if folder != "" && n == ad.ConfigDirEnv {
			continue
		}
		if pinned == "" && el(n) != "" {
			pinned = n
		}
		if added == "" && fl(n) != el(n) {
			added = n
		}
	}
	var doc capacity.Accounts
	if folder != "" || (id == "" && pinned == "" && added == "") || (id != "" && id != capacity.DefaultID) {
		var aerr *apiError
		if doc, aerr = s.accountsForLaunch(); aerr != nil {
			return session.BoundAccount{}, nil, aerr
		}
	}
	// What is wanted, resolved: a folder is the registered account that
	// holds it ("" for none); no id is the adapter's default at this
	// moment, the tool's own login when a variable sets the login.
	switch {
	case folder != "":
		id = ""
		for _, a := range doc.Accounts {
			if a.Adapter == agent.Adapter && (filepath.Clean(a.Folder) == filepath.Clean(folder) || sameFolder(a.Folder, folder)) {
				if a.Retired && want.bound == nil {
					return refuse("the folder is the account " + a.ID + "'s, which is retired: a new launch takes no retired account")
				}
				id = a.ID
				break
			}
		}
	case id == "":
		id = capacity.DefaultID
		if pinned == "" && added == "" {
			id = capacity.DefaultOf(doc, agent.Adapter)
		}
	case id != capacity.DefaultID:
		// An account named (or the default naming one) beside another
		// login variable is refused.
		switch {
		case !capacity.ValidID(id):
			return refuse("an account is default or a- and 12 of a-z 2-7")
		case ad.ConfigDirEnv == "":
			return refuse(agent.Name + " does not move its login folder: it takes its own login alone")
		case pinned != "":
			return refuse("this agent's entry sets " + pinned + ": it uses that login, its default account only")
		case added != "":
			return refuse("this launch sets " + added + ": it uses that login, its default account only")
		}
	}
	// The one judgement, from the environment the process gets.
	if folder != "" {
		env[ad.ConfigDirEnv] = folder
	}
	switch {
	case added != "":
		return session.BoundAccount{ID: capacity.DefaultID, Folder: folder, Source: session.SourceLaunchEnv}, nothing, nil
	case pinned != "":
		b := session.BoundAccount{ID: capacity.DefaultID, Folder: folder, Source: session.SourcePinned}
		if folder == "" && ad.ConfigDirEnv != "" {
			b.Folder = seenFolder(el(ad.ConfigDirEnv), cwd)
		}
		return b, nothing, nil
	case folder == "" && id == capacity.DefaultID:
		return session.BoundAccount{ID: id, Source: session.SourceOwn}, nothing, nil
	case folder != "" && id == "":
		return session.BoundAccount{ID: session.SourceUnlisted, Folder: folder, Source: session.SourceUnlisted}, nothing, nil
	case s.capacity == nil || s.capacity.st == nil:
		return refuse("this server keeps no accounts")
	}
	// A registered account, its folder the one login variable.
	hold := s.capacity.st.Reserve
	if want.bound != nil {
		hold = s.capacity.st.Hold
	}
	a, release, err := hold(id)
	if err != nil {
		return session.BoundAccount{}, nil, loginRefusal(err)
	}
	if a.Adapter != agent.Adapter {
		release()
		return refuse("the account " + id + " is " + a.Adapter + "'s, not " + agent.Adapter + "'s")
	}
	if folder == "" {
		folder = a.Folder
		env[ad.ConfigDirEnv] = folder
	}
	return session.BoundAccount{ID: a.ID, Label: a.Label, Folder: folder, Source: session.SourceAccount}, release, nil
}

// accountsForLaunch is the accounts document a launch resolves an account
// in: none on a server that keeps no accounts (no data directory); a
// refusal while it cannot be read, since the launch could not say which
// account it takes.
func (s *Server) accountsForLaunch() (capacity.Accounts, *apiError) {
	unknown := newAPIError(http.StatusServiceUnavailable, "capacity_unavailable", "capacity/ could not be read, so the launch's account is not known: name the account default for the tool's own login")
	switch d := s.capacity; {
	case d == nil:
		return capacity.Accounts{}, nil
	case d.st == nil:
		return capacity.Accounts{}, unknown
	}
	doc, err := s.capacity.st.Accounts()
	if err != nil {
		return capacity.Accounts{}, unknown
	}
	return doc, nil
}

// loginRefusal answers a reservation the accounts refused.
func loginRefusal(err error) *apiError {
	var ce *capacity.Error
	switch {
	case errors.As(err, &ce) && ce.Code == capacity.CodeNotOwner:
		return newAPIError(http.StatusConflict, ce.Code, ce.Words)
	case errors.As(err, &ce) && ce.Code == capacity.CodeInvalidAccount:
		return newAPIError(http.StatusBadRequest, ce.Code, ce.Words)
	}
	return newAPIError(http.StatusServiceUnavailable, "capacity_unavailable", "the accounts could not be read, so the launch's account is not known")
}

// loginWords names a binding's login for people, never its folder.
func loginWords(b session.BoundAccount) string {
	switch b.Source {
	case session.SourceAccount:
		return "the account " + b.ID
	case session.SourcePinned:
		return "the login its agent's entry sets"
	case session.SourceLaunchEnv:
		return "a login its launch sets"
	case session.SourceUnlisted:
		return "a folder not in Accounts"
	}
	return "the tool's own login"
}

// accountGone is the refusal of a launch made again whose login is not the
// one it was bound to: it is never started under another.
func accountGone(was, now session.BoundAccount) *apiError {
	words := loginWords(now)
	if was.Source == now.Source && was.ID == now.ID {
		words += ", changed since"
	}
	return newAPIError(http.StatusConflict, "account_gone", "it was launched on "+loginWords(was)+" and would get "+words+" now: it is never moved to another login; launch it anew")
}

// launchModel is the arguments that choose model for agent (its adapter's
// ModelArgs, after the launch's own), none for "": a model is held to
// modelPattern, refused for an adapter that takes none, and refused beside
// arguments that choose one already.
func launchModel(agent catalog.Agent, model string, args []string) ([]string, *apiError) {
	if model == "" {
		return nil, nil
	}
	ad, ok := agents.Get(agent.Adapter)
	switch {
	case !modelPattern.MatchString(model):
		return nil, newAPIError(http.StatusBadRequest, "invalid_request", "a model is 1 to 100 of A-Z a-z 0-9 . _ : / @ -")
	case !ok || ad.ModelArgs == nil || len(ad.ModelArgs(model)) == 0:
		return nil, newAPIError(http.StatusBadRequest, "invalid_request", agent.Name+" takes no model")
	}
	margs := ad.ModelArgs(model)
	flag, _, _ := strings.Cut(margs[0], "=")
	for _, a := range args {
		if a == flag || strings.HasPrefix(a, flag+"=") {
			return nil, newAPIError(http.StatusBadRequest, "invalid_request", "the arguments choose a model already: set the model in one place")
		}
	}
	return margs, nil
}

// bindMembers binds each member of ms as its session will be started
// (launchEnv, under the run's yolo choice): to the account it names, or
// its adapter's default at this moment. On an agent's own routes (caller,
// the calling session), whose members name no account
// (agentNamesNoAccount), one of the caller's adapter takes the caller's
// login, resolved as a launch would resolve it
// with the caller's account and compared by effective login (its folder
// and its fingerprint), never copied by id; one of another adapter takes
// that adapter's default. It answers ms bound (Member.Bound) and the
// release of their reservations, to call once the run's record holds them
// or the launch failed.
func (s *Server) bindMembers(ms []crew.Member, yolo bool, caller *session.Local) ([]crew.Member, func(), *apiError) {
	var releases []func()
	release := func() {
		for _, r := range releases {
			r()
		}
	}
	refuse := func(m crew.Member, aerr *apiError) ([]crew.Member, func(), *apiError) {
		release()
		return nil, nil, newAPIError(aerr.status, aerr.Code, "member "+m.Name+": "+aerr.Message)
	}
	// The caller's login and adapter are what its session was started
	// with (Launched), never its catalog entry as it is now; a session whose
	// launch kept neither adds no member.
	var by session.BoundAccount
	byAdapter := ""
	if caller != nil {
		l := caller.Launched()
		by, byAdapter = l.Account, l.Adapter
		if by.Source == "" && len(ms) > 0 {
			return refuse(ms[0], newAPIError(http.StatusBadRequest, "invalid_account", "this session was started before its login was kept: it adds no member; start it anew"))
		}
	}
	out := slices.Clone(ms)
	for i := range out {
		m := &out[i]
		agent, ok := s.Catalog().Get(m.AgentID)
		switch {
		case m.Bound != nil:
			return refuse(*m, newAPIError(http.StatusBadRequest, "invalid_crew", "a binding is the server's to make: name an account"))
		case !ok:
			return refuse(*m, newAPIError(http.StatusBadRequest, "invalid_crew", "unknown agent "+m.AgentID))
		}
		if _, aerr := launchModel(agent, m.Model, m.Args); aerr != nil {
			return refuse(*m, aerr)
		}
		account, mine := m.Account, caller != nil && agent.Adapter == byAdapter
		if mine {
			if by.Source == session.SourceLaunchEnv {
				return refuse(*m, newAPIError(http.StatusBadRequest, "invalid_account", "this session's login is set by its launch: it adds no member of its adapter"))
			}
			account = capacity.DefaultID
			if by.Source == session.SourceAccount {
				account = by.ID
			}
		}
		applied := yolo && !agent.Yolo.Empty()
		_, adapterEnv := agents.InjectFor(agent.Adapter, agents.HooksDir(s.cfg.DataDir), agent.EffectiveSignal(), applied)
		passthrough, env, b, rel, aerr := s.launchEnv(agent, adapterEnv, applied, nil, nil, account, "", loginWant{})
		if aerr != nil {
			return refuse(*m, aerr)
		}
		releases = append(releases, rel)
		// A member is bound before the folder it starts in is known (each
		// attempt of a managed run has its own): a folder variable that is
		// not whole would be another login in each.
		lookup := pty.Lookup(pty.BuildEnv(pty.ParentEnv(), passthrough, env, nil))
		if ad, _ := agents.Get(agent.Adapter); ad.ConfigDirEnv != "" && lookup(ad.ConfigDirEnv) != "" && !filepath.IsAbs(lookup(ad.ConfigDirEnv)) {
			return refuse(*m, newAPIError(http.StatusBadRequest, "invalid_account", "its entry sets "+ad.ConfigDirEnv+" to a folder under the one it starts in, which would be another login in each of its working folders: set a whole path"))
		}
		b.Login = loginOf(agent.Adapter, "", lookup)
		if mine && (b.Folder != by.Folder || b.Login != by.Login) {
			return refuse(*m, newAPIError(http.StatusBadRequest, "invalid_account", "this agent's entry uses another login than the session's: "+loginWords(b)))
		}
		m.Bound = &b
	}
	return out, release, nil
}

// agentNamesNoAccount refuses the members an agent's route decoded when
// any names an account, the member the agent becomes included: an agent
// does not choose accounts.
func agentNamesNoAccount(ms []crew.Member) *apiError {
	for _, m := range ms {
		if m.Account != "" {
			return newAPIError(http.StatusBadRequest, "invalid_account", "member "+m.Name+": an agent does not choose accounts")
		}
	}
	return nil
}

// bindMember is bindMembers for a member added to the run runID, under its
// yolo choice (404 for a run the engine does not hold).
func (s *Server) bindMember(runID string, m crew.Member, caller *session.Local) (crew.Member, func(), *apiError) {
	run, ok := s.runs.Get(runID)
	if !ok {
		return m, nil, newAPIError(http.StatusNotFound, "not_found", "no such run")
	}
	ms, release, aerr := s.bindMembers([]crew.Member{m}, run.Yolo, caller)
	if aerr != nil {
		return m, nil, aerr
	}
	return ms[0], release, nil
}

// boundTo names, in words, what holds the account id (capacity.Facts.Bound):
// the server sessions bound to it whose process has not ended (the
// registry's Holders: a session whose stop failed, or removed from the
// listing while its process runs, holds it as a running one does), the
// engine's runs a member of which is bound to it and that have not ended,
// and the ledger's runs running or interrupted whose crew as launched
// binds it. A stopped or finished run is no bar: a continuation of it is
// refused account_gone. A ledger, or a run's crew in it, that cannot be
// read holds it, since what it holds is not known.
func (s *Server) boundTo(id string) []string {
	var names []string
	for _, d := range s.registry.Holders() {
		if l, ok := d.(*session.Local); ok && l.Info().Kind == session.KindServer && l.Launched().Account.ID == id {
			names = append(names, "session "+l.Info().Name)
		}
	}
	seen := map[string]bool{}
	for _, run := range s.runs.List() {
		if run.State == crew.RunStopped || run.State == crew.RunFinished {
			continue
		}
		if slices.ContainsFunc(run.Members, func(m crew.MemberState) bool { return m.Account != nil && m.Account.ID == id }) {
			seen[run.ID] = true
			names = append(names, "run "+run.ID)
		}
	}
	if s.ledger == nil {
		return names
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	rows, err := s.ledger.Runs(ctx)
	if err != nil {
		return append(names, "the ledger's runs, which could not be read")
	}
	for _, row := range rows {
		if seen[row.ID] || row.Crew == "" || (row.State != ledger.RunRunning && row.State != ledger.RunInterrupted) {
			continue
		}
		var c crew.Crew
		if err := json.Unmarshal([]byte(row.Crew), &c); err != nil {
			names = append(names, "run "+row.ID+", whose crew could not be read")
		} else if slices.ContainsFunc(c.Members, func(m crew.Member) bool { return m.Bound != nil && m.Bound.ID == id }) {
			names = append(names, "run "+row.ID)
		}
	}
	return names
}
