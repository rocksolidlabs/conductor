package ext

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"path/filepath"
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
// starts, whether it runs yolo (as resolved with the server's default) and
// the hex SHA-256 of the agent's catalogue entry it would start from (its
// command, its session recipe and its environment's names, never a
// value).
type ResolvedMember struct {
	Name      string `json:"name"`
	Agent     string `json:"agent"`
	Start     string `json:"start"`           // immediately | after | manual | task
	After     string `json:"after,omitempty"` // with after: the member waited for
	Yolo      bool   `json:"yolo"`
	Selection string `json:"selection"` // 64 lower-case hex
}

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
)

// digestPrefix leads what a resolved launch's digest is the SHA-256 of; its
// number changes with the encoding, never otherwise.
const digestPrefix = "conductor resolved launch 1\n"

// ResolvedDigest is r's digest: the lower-case hex SHA-256 of
//
//	"conductor resolved launch 1\n" + crewRevision + "\n" + canonical
//
// where canonical is r encoded by encoding/json with Digest "" (and
// Members [] for none), fields in their declared order, HTML escaping off,
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
	r.Digest = ""
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
// members; a digest of DigestLen lower-case hex or none; and its pinned
// record within MaxResolvedLaunch.
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
// after alone; a selection of 64 lower-case hex.
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
	}
	return nil
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
