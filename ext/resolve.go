package ext

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"
)

// ResolveRequest is a saved crew whose launch to resolve, for a Dispatch
// project or none, at a commit or at the launch folder's HEAD.
type ResolveRequest struct {
	Crew    string `json:"crew"`
	Project string `json:"project,omitempty"`
	Commit  string `json:"commit,omitempty"` // 40 or 64 lower-case hex; none: the launch folder's HEAD
}

// ResolvedLaunch is what a launch of a saved crew would run now, as the
// server works it out (Host.ResolveLaunch), and its digest. Everything in
// it but Digest is pinned by the digest: a launch that carries the digest
// (LaunchRequest.ResolvedDigest) is resolved again in the launch itself
// and refused launch_changed on any difference. Nothing in it moves by
// itself (no reading, count or time).
type ResolvedLaunch struct {
	Crew    string `json:"crew"`
	Name    string `json:"name"`
	Managed bool   `json:"managed"`
	Tasks   int    `json:"tasks"` // how many; their content is in the crew's revision
	Where   string `json:"where"` // RunsOnServer | RunsOnHost
	Project string `json:"project,omitempty"`
	// ProjectCwd is the project's folder, resolved as the launch folder is
	// (a real path on the server); "" for no project, and for a project
	// with no folder.
	ProjectCwd string `json:"projectCwd,omitempty"`
	Cwd        string `json:"cwd"` // the launch folder, resolved: a real path on the server
	// InProject is whether Cwd is ProjectCwd or a folder under it; false
	// with no project or no project folder.
	InProject bool             `json:"inProject"`
	Manifest  ResolvedManifest `json:"manifest"`
	Members   []ResolvedMember `json:"members"` // 1 to MaxResolvedMembers, in the crew's order
	Digest    string           `json:"digest"`  // ResolvedDigest's, DigestLen lower-case hex
	// Capacity is each bound account's usage as the server last read it,
	// for each distinct agent and account among the members in their
	// order: what the server keeps, read without asking the agent's tool.
	// It is never in the digest.
	Capacity []CapacityView `json:"capacity,omitempty"` // at most MaxCapacityViews
}

// ResolvedManifest is the project manifest a launch would run with: none,
// the crew's own (at its revision), or the repository's file at the base
// (its blob's id).
type ResolvedManifest struct {
	Source   string `json:"source"`             // ManifestNone | ManifestCrew | ManifestFile
	Revision int64  `json:"revision,omitempty"` // crew: the crew's manifest revision
	Blob     string `json:"blob,omitempty"`     // file: the file's blob id at the base, 40 or 64 lower-case hex
}

// ResolvedMember is one member of a resolved launch: its agent, how it
// starts, whether it runs yolo (as resolved with the server's default),
// the hex SHA-256 of the agent's catalogue entry it would start from (its
// command, its session recipe and its environment's names, never a
// value), and the account and the model a launch would bind it to now
// (the crew's account for the member, else the agent's default). The
// account is named by its id and where its login comes from, never by its
// folder or its login.
type ResolvedMember struct {
	Name          string `json:"name"`
	Agent         string `json:"agent"`
	Start         string `json:"start"`           // immediately | after | manual | task
	After         string `json:"after,omitempty"` // with after: the member waited for
	Yolo          bool   `json:"yolo"`
	Selection     string `json:"selection"`               // 64 lower-case hex
	Account       string `json:"account,omitempty"`       // the account a launch would bind now, by its id
	AccountSource string `json:"accountSource,omitempty"` // own | account | pinned | launch_env
	Model         string `json:"model,omitempty"`         // "" for the agent's default
}

// CapacityView is what the server keeps of an account's usage, as last
// read: its state and freshness, this server's own counts of the sessions
// bound to it, and the windows and credit figures the agent's tool
// reported.
type CapacityView struct {
	Agent     string           `json:"agent"`
	Account   string           `json:"account"`
	Label     string           `json:"label,omitempty"` // at most MaxAccountLabel characters
	State     string           `json:"state"`           // 1 to MaxCapacityWord of a-z, 0-9 and '_'
	Fresh     string           `json:"fresh"`           // fresh | stale | reset_passed | none
	ReadAt    time.Time        `json:"readAt,omitzero"`
	Running   int              `json:"running"`
	Started5h int              `json:"started5h"`
	Started7d int              `json:"started7d"`
	Windows   []CapacityWindow `json:"windows,omitempty"` // at most MaxCapacityWindows
	Credits   []CapacityCredit `json:"credits,omitempty"` // at most MaxCapacityCredits
}

// CapacityWindow is one usage limit as the agent's tool reported it.
type CapacityWindow struct {
	Kind     string    `json:"kind"`               // at most MaxCapacityWord bytes, the tool's word
	Name     string    `json:"name"`               // at most MaxCapacityName bytes, for people
	Minutes  int       `json:"minutes,omitempty"`  // the window's length, when the tool says it
	Percent  int       `json:"percent"`            // 0 to 100, the share used
	ResetsAt time.Time `json:"resetsAt,omitzero"`  // none when the tool gave none
	Severity string    `json:"severity,omitempty"` // normal | warning | critical
}

// CapacityCredit is one of the tool's credit figures, as it wrote them.
type CapacityCredit struct {
	Kind      string `json:"kind"` // at most MaxCapacityWord bytes
	Enabled   *bool  `json:"enabled,omitempty"`
	Used      string `json:"used,omitempty"` // each figure at most MaxCapacityFigure bytes, as the tool wrote it
	Limit     string `json:"limit,omitempty"`
	Balance   string `json:"balance,omitempty"`
	Unlimited bool   `json:"unlimited,omitempty"`
	Currency  string `json:"currency,omitempty"` // at most MaxCapacityWord bytes
}

// Where a resolved member's account's login comes from
// (ResolvedMember.AccountSource).
const (
	AccountOwn       = "own"        // the tool's own login: nothing sets its folder or a login variable
	AccountAccount   = "account"    // a registered account
	AccountPinned    = "pinned"     // the agent's catalogue entry sets the login
	AccountLaunchEnv = "launch_env" // the launch's own environment sets the login
)

// The freshness of a capacity view (CapacityView.Fresh).
const (
	CapacityFresh       = "fresh"        // read a short while ago
	CapacityStale       = "stale"        // read longer ago
	CapacityResetPassed = "reset_passed" // a window's reset is behind the clock
	CapacityNone        = "none"         // nothing read
)

// Where a resolved launch runs (ResolvedLaunch.Where).
const (
	RunsOnServer = "server"
	RunsOnHost   = "host"
)

// The sources of a resolved launch's manifest (ResolvedManifest.Source).
const (
	ManifestNone = "none"
	ManifestCrew = "crew"
	ManifestFile = "file"
)

const (
	// MaxResolvedLaunch bounds a resolved launch, in bytes of its record as
	// ResolvedDigest encodes it (the part its digest pins). A launch past
	// it is refused resolved_too_large, never cut.
	MaxResolvedLaunch = 8 << 10
	// MaxResolvedMembers bounds a resolved launch's members: a crew's own.
	MaxResolvedMembers = 12
	// MaxCrewName bounds a crew's name, in characters: the crews' own.
	MaxCrewName = 60
	// MaxCrewTasks bounds a crew's tasks: the crews' own.
	MaxCrewTasks = 128
	// MaxMemberName bounds a crew member's name, in bytes: the crews' own.
	MaxMemberName = 40
	// MaxCapacityViews, MaxCapacityWindows and MaxCapacityCredits bound a
	// resolved launch's capacity views, a view's windows and its credit
	// figures.
	MaxCapacityViews   = 12
	MaxCapacityWindows = 16
	MaxCapacityCredits = 4
	// MaxAccountLabel bounds an account's label, in characters.
	MaxAccountLabel = 60
	// MaxCapacityWord bounds a state, a kind and a currency, in bytes;
	// MaxCapacityName a window's name; MaxCapacityFigure a credit's figure.
	MaxCapacityWord   = 40
	MaxCapacityName   = 80
	MaxCapacityFigure = 32
	// MaxResolvedReply bounds a whole resolved launch, its capacity views
	// with it, in bytes as encoding/json encodes it: the pinned part's
	// MaxResolvedLaunch and every view at its bounds, each byte one the
	// encoder writes as six.
	MaxResolvedReply = 288 << 10
)

// digestPrefix leads what a resolved launch's digest is the SHA-256 of; its
// number changes with the encoding, never otherwise.
const digestPrefix = "conductor resolved launch 1\n"

// ResolvedDigest is r's digest: the lower-case hex SHA-256 of
//
//	"conductor resolved launch 1\n" + crewRevision + "\n" + canonical
//
// where canonical is r encoded by encoding/json with Digest "" and
// Capacity nil (and Members [] for none), fields in their declared order, HTML escaping off,
// no indent, no final newline; crewRevision is the crew's revision as the
// server names a stored copy of the crew (64 lower-case hex). ok is false
// when canonical is past MaxResolvedLaunch, or crewRevision is not a
// revision. The server and ext/exttest's fake both call it; nothing else
// encodes the record for a digest. A digest says the launch is unchanged
// since it was resolved, never that a person agreed to it.
func ResolvedDigest(r ResolvedLaunch, crewRevision string) (digest string, ok bool) {
	canonical, ok := r.canonical()
	if !ok || !lowerHex(crewRevision, 64) {
		return "", false
	}
	h := sha256.New()
	h.Write([]byte(digestPrefix))
	h.Write([]byte(crewRevision))
	h.Write([]byte{'\n'})
	h.Write(canonical)
	return hex.EncodeToString(h.Sum(nil)), true
}

// canonical is r as ResolvedDigest encodes it, and whether it is within
// MaxResolvedLaunch.
func (r ResolvedLaunch) canonical() ([]byte, bool) {
	r.Digest, r.Capacity = "", nil
	if r.Members == nil {
		r.Members = []ResolvedMember{}
	}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(r); err != nil {
		return nil, false
	}
	out := bytes.TrimSuffix(b.Bytes(), []byte{'\n'})
	return out, len(out) <= MaxResolvedLaunch
}

// Valid checks r against its bounds: a crew id; a crew name of 1 to
// MaxCrewName characters on one line; Managed exactly when it has 1 to
// MaxCrewTasks tasks; where it runs; a project's key or none, and a
// project folder and InProject only with one; absolute folders of at most
// MaxFolder bytes; a valid manifest; 1 to MaxResolvedMembers valid
// members; a digest of DigestLen lower-case hex or none; at most
// MaxCapacityViews valid capacity views; its pinned record within
// MaxResolvedLaunch and the whole within MaxResolvedReply.
func (r ResolvedLaunch) Valid() error {
	const rec = "resolved launch"
	switch {
	case !slug(r.Crew, MaxCrewID, false):
		return invalid(rec, "crew %q is not a crew id", r.Crew)
	case r.Name == "" || !oneLine(r.Name) || utf8.RuneCountInString(r.Name) > MaxCrewName:
		return invalid(rec, "name is not one line of 1 to %d characters", MaxCrewName)
	case r.Tasks < 0 || r.Tasks > MaxCrewTasks:
		return invalid(rec, "tasks is not 0 to %d", MaxCrewTasks)
	case r.Managed != (r.Tasks > 0):
		return invalid(rec, "a crew is managed exactly when it has tasks")
	case r.Where != RunsOnServer && r.Where != RunsOnHost:
		return invalid(rec, "where %q is not %s or %s", r.Where, RunsOnServer, RunsOnHost)
	case r.Project != "" && !projectKey(r.Project):
		return invalid(rec, "project %q is not a project's key", r.Project)
	case r.Project == "" && (r.ProjectCwd != "" || r.InProject):
		return invalid(rec, "a project's folder is a project's, and none is named")
	case r.ProjectCwd == "" && r.InProject:
		return invalid(rec, "inProject with no project folder")
	case r.ProjectCwd != "" && !folder(r.ProjectCwd):
		return invalid(rec, "projectCwd is not an absolute path of at most %d bytes", MaxFolder)
	case !folder(r.Cwd):
		return invalid(rec, "cwd is not an absolute path of at most %d bytes", MaxFolder)
	case len(r.Members) == 0 || len(r.Members) > MaxResolvedMembers:
		return invalid(rec, "members are not 1 to %d", MaxResolvedMembers)
	case r.Digest != "" && !lowerHex(r.Digest, DigestLen):
		return invalid(rec, "digest is not %d lower-case hex", DigestLen)
	case len(r.Capacity) > MaxCapacityViews:
		return invalid(rec, "capacity is past %d views", MaxCapacityViews)
	}
	for _, v := range r.Capacity {
		if err := v.Valid(); err != nil {
			return invalid(rec, "%v", err)
		}
	}
	if err := r.Manifest.Valid(); err != nil {
		return invalid(rec, "%v", err)
	}
	for _, m := range r.Members {
		if err := m.Valid(); err != nil {
			return invalid(rec, "%v", err)
		}
	}
	if _, ok := r.canonical(); !ok {
		return invalid(rec, "the record is past %d bytes", MaxResolvedLaunch)
	}
	if b, err := json.Marshal(r); err != nil || len(b) > MaxResolvedReply {
		return invalid(rec, "the record with its capacity is past %d bytes", MaxResolvedReply)
	}
	return nil
}

// folder reports whether s is an absolute path of at most MaxFolder bytes
// on one line.
func folder(s string) bool {
	return s != "" && len(s) <= MaxFolder && oneLine(s) && filepath.IsAbs(s)
}

// Valid checks m against its source: none with no revision or blob; the
// crew's with a revision not below 0 and no blob; a file's with a blob id
// of 40 or 64 lower-case hex and no revision.
func (m ResolvedManifest) Valid() error {
	const rec = "resolved manifest"
	switch m.Source {
	case ManifestNone:
		if m.Revision != 0 || m.Blob != "" {
			return invalid(rec, "no manifest has no revision or blob")
		}
	case ManifestCrew:
		if m.Revision < 0 || m.Blob != "" {
			return invalid(rec, "the crew's manifest has a revision from 0 and no blob")
		}
	case ManifestFile:
		if m.Revision != 0 || !commitID(m.Blob) {
			return invalid(rec, "a file's manifest has a blob id of 40 or 64 lower-case hex and no revision")
		}
	default:
		return invalid(rec, "source %q is not %s, %s or %s", m.Source, ManifestNone, ManifestCrew, ManifestFile)
	}
	return nil
}

// Valid checks m against its bounds: a member's name (1 to MaxMemberName
// of a-z, 0-9, '.', '_' and '-', beginning with a letter or a digit); a
// catalog agent's id; one of the starts, and the member waited for with
// after alone; a selection of 64 lower-case hex; an account's id
// ("default", or "a-" and 12 of a-z and 2-7) and where its login comes
// from, both or neither; a model's name or none.
func (m ResolvedMember) Valid() error {
	const rec = "resolved member"
	switch {
	case !memberName(m.Name):
		return invalid(rec, "name %q is not a member's name", m.Name)
	case !agentID(m.Agent):
		return invalid(rec, "agent %q is not a catalog agent's id", m.Agent)
	case m.Start != "immediately" && m.Start != "after" && m.Start != "manual" && m.Start != "task":
		return invalid(rec, "start %q is not immediately, after, manual or task", m.Start)
	case (m.Start == "after") != (m.After != ""):
		return invalid(rec, "after names the member waited for, with start after alone")
	case m.After != "" && !memberName(m.After):
		return invalid(rec, "after %q is not a member's name", m.After)
	case !lowerHex(m.Selection, 64):
		return invalid(rec, "selection is not 64 lower-case hex")
	case (m.Account == "") != (m.AccountSource == ""):
		return invalid(rec, "an account and where its login comes from are given together")
	case m.Account != "" && !accountID(m.Account):
		return invalid(rec, "account %q is not an account's id", m.Account)
	case m.AccountSource != "" && m.AccountSource != AccountOwn && m.AccountSource != AccountAccount && m.AccountSource != AccountPinned && m.AccountSource != AccountLaunchEnv:
		return invalid(rec, "accountSource %q is not %s, %s, %s or %s", m.AccountSource, AccountOwn, AccountAccount, AccountPinned, AccountLaunchEnv)
	case m.Model != "" && !model(m.Model):
		return invalid(rec, "model %q is not a model's name", m.Model)
	}
	return nil
}

// accountID reports whether s is an account's id: "default", or "a-" and
// 12 of a-z and 2-7.
func accountID(s string) bool {
	if s == "default" {
		return true
	}
	rest, ok := strings.CutPrefix(s, "a-")
	if !ok || len(rest) != 12 {
		return false
	}
	for i := 0; i < len(rest); i++ {
		if c := rest[i]; (c < 'a' || c > 'z') && (c < '2' || c > '7') {
			return false
		}
	}
	return true
}

// capacityWord reports whether s is 1 to MaxCapacityWord of a-z, 0-9 and
// '_'.
func capacityWord(s string) bool {
	if s == "" || len(s) > MaxCapacityWord {
		return false
	}
	for i := 0; i < len(s); i++ {
		if c := s[i]; (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '_' {
			return false
		}
	}
	return true
}

// Valid checks v against its bounds: a catalog agent's id; an account's
// id; a label of at most MaxAccountLabel characters on one line, or none;
// a state of 1 to MaxCapacityWord of a-z, 0-9 and '_'; one of the
// freshnesses; counts not below 0; at most MaxCapacityWindows valid
// windows and MaxCapacityCredits valid credit figures.
func (v CapacityView) Valid() error {
	const rec = "capacity view"
	switch {
	case !agentID(v.Agent):
		return invalid(rec, "agent %q is not a catalog agent's id", v.Agent)
	case !accountID(v.Account):
		return invalid(rec, "account %q is not an account's id", v.Account)
	case !oneLine(v.Label) || utf8.RuneCountInString(v.Label) > MaxAccountLabel:
		return invalid(rec, "label is not one line of at most %d characters", MaxAccountLabel)
	case !capacityWord(v.State):
		return invalid(rec, "state %q is not 1 to %d of a-z, 0-9 and '_'", v.State, MaxCapacityWord)
	case v.Fresh != CapacityFresh && v.Fresh != CapacityStale && v.Fresh != CapacityResetPassed && v.Fresh != CapacityNone:
		return invalid(rec, "fresh %q is not %s, %s, %s or %s", v.Fresh, CapacityFresh, CapacityStale, CapacityResetPassed, CapacityNone)
	case v.Running < 0 || v.Started5h < 0 || v.Started7d < 0:
		return invalid(rec, "a count is below 0")
	case len(v.Windows) > MaxCapacityWindows:
		return invalid(rec, "windows are past %d", MaxCapacityWindows)
	case len(v.Credits) > MaxCapacityCredits:
		return invalid(rec, "credits are past %d", MaxCapacityCredits)
	}
	for _, w := range v.Windows {
		if err := w.Valid(); err != nil {
			return invalid(rec, "%v", err)
		}
	}
	for _, c := range v.Credits {
		if err := c.Valid(); err != nil {
			return invalid(rec, "%v", err)
		}
	}
	return nil
}

// Valid checks w against its bounds: a kind of 1 to MaxCapacityWord bytes
// and a name of at most MaxCapacityName bytes, each on one line; minutes
// not below 0; a percent from 0 to 100; a severity of normal, warning or
// critical, or none.
func (w CapacityWindow) Valid() error {
	const rec = "capacity window"
	if err := line(rec, "kind", w.Kind, MaxCapacityWord, true); err != nil {
		return err
	}
	if err := line(rec, "name", w.Name, MaxCapacityName, false); err != nil {
		return err
	}
	switch {
	case w.Minutes < 0:
		return invalid(rec, "minutes is below 0")
	case w.Percent < 0 || w.Percent > 100:
		return invalid(rec, "percent is not 0 to 100")
	case w.Severity != "" && w.Severity != "normal" && w.Severity != "warning" && w.Severity != "critical":
		return invalid(rec, "severity %q is not normal, warning or critical", w.Severity)
	}
	return nil
}

// Valid checks c against its bounds: a kind of 1 to MaxCapacityWord bytes,
// figures of at most MaxCapacityFigure bytes and a currency of at most
// MaxCapacityWord, each on one line.
func (c CapacityCredit) Valid() error {
	const rec = "capacity credit"
	if err := line(rec, "kind", c.Kind, MaxCapacityWord, true); err != nil {
		return err
	}
	for _, f := range []struct{ name, v string }{{"used", c.Used}, {"limit", c.Limit}, {"balance", c.Balance}} {
		if err := line(rec, f.name, f.v, MaxCapacityFigure, false); err != nil {
			return err
		}
	}
	return line(rec, "currency", c.Currency, MaxCapacityWord, false)
}

// memberName reports whether s is a crew member's name.
func memberName(s string) bool {
	if s == "" || len(s) > MaxMemberName {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
		case i > 0 && (c == '.' || c == '_' || c == '-'):
		default:
			return false
		}
	}
	return true
}

// Valid checks r against its bounds: a crew id, a project's key or none,
// and a full commit id (40 or 64 lower-case hex) or none. Whether the crew
// and the project exist, and whether the folder's repository holds the
// commit, is the server's to answer (ErrNotFound, project_unknown,
// start_commit_missing).
func (r ResolveRequest) Valid() error {
	const rec = "resolve request"
	switch {
	case !slug(r.Crew, MaxCrewID, false):
		return invalid(rec, "crew %q is not a crew id", r.Crew)
	case r.Project != "" && !projectKey(r.Project):
		return invalid(rec, "project %q is not a project's key", r.Project)
	case r.Commit != "" && !commitID(r.Commit):
		return invalid(rec, "commit %q is not a full commit id", r.Commit)
	}
	return nil
}
