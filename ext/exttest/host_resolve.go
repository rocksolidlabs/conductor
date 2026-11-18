package exttest

import (
	"context"
	"fmt"
	"slices"

	"github.com/rocksolidlabs/conductor/ext"
)

// SetResolved has a resolve of crew start from r (its Digest and Capacity
// set by the resolve): the crew as the server would hold it, saved anew,
// so its revision changes and so does every digest of it.
func (h *FakeHost) SetResolved(crew string, r ext.ResolvedLaunch) {
	h.srv.mu.Lock()
	h.srv.revs++
	h.srv.crews[crew] = fakeCrew{r: copyResolved(r), rev: crewRevision(crew, h.srv.revs)}
	h.srv.mu.Unlock()
	h.tell()
}

// SetServerYolo sets the server's yolo default: on, every member of every
// resolve runs yolo, as members that leave it to the server do.
func (h *FakeHost) SetServerYolo(on bool) {
	h.srv.mu.Lock()
	h.srv.yolo = on
	h.srv.mu.Unlock()
	h.tell()
}

// SetSelection has every member of agent resolve with sha as its
// selection (the hex SHA-256 of its catalogue entry), as an edit of the
// entry changes it.
func (h *FakeHost) SetSelection(agent, sha string) {
	h.srv.mu.Lock()
	h.srv.selected[agent] = sha
	h.srv.mu.Unlock()
	h.tell()
}

// SetManifestBlob has a resolve at commit ("" for the launch folder's
// HEAD) whose manifest is the repository's file name blob as that file's
// blob id.
func (h *FakeHost) SetManifestBlob(commit, blob string) {
	h.srv.mu.Lock()
	h.srv.blobs[commit] = blob
	h.srv.mu.Unlock()
	h.tell()
}

// SetBinding has member resolve bound to account (by its id), its login
// from source, with model ("" for the agent's default).
func (h *FakeHost) SetBinding(member, account, source, model string) {
	h.srv.mu.Lock()
	h.srv.bindings[member] = fakeBinding{account: account, source: source, model: model}
	h.srv.mu.Unlock()
	h.tell()
}

// SetCapacity has every resolve carry views as its capacity views: what
// the server kept of the bound accounts' usage. They are never in the
// digest.
func (h *FakeHost) SetCapacity(views ...ext.CapacityView) {
	h.srv.mu.Lock()
	h.srv.capacity = copyViews(views)
	h.srv.mu.Unlock()
	h.tell()
}

// OnResolveLaunch has ResolveLaunch answer fn's answer for a valid
// request.
func (h *FakeHost) OnResolveLaunch(fn func(ext.ResolveRequest) (ext.ResolvedLaunch, error)) {
	h.mu.Lock()
	h.onResolve = fn
	h.mu.Unlock()
}

// ResolveLaunch records r and answers what a launch of r.Crew would run
// now, as the server works it out, from what SetResolved gave for the crew
// and the settings now (the server's yolo default, the selections, the
// manifest's blob at the commit, the bindings, the capacity views), its
// digest by ext.ResolvedDigest over the crew's revision; or what
// OnResolveLaunch set. A crew it was given no record for answers
// ext.ErrNotFound; a commit for a crew without tasks is refused
// invalid_request, and one the repository does not hold (SetCommits)
// start_commit_missing; a record past ext.MaxResolvedLaunch
// resolved_too_large; a request its Valid refuses invalid_request. It
// launches nothing and keeps nothing.
func (h *FakeHost) ResolveLaunch(ctx context.Context, r ext.ResolveRequest) (ext.ResolvedLaunch, error) {
	if err := h.enter(ctx); err != nil {
		return ext.ResolvedLaunch{}, err
	}
	h.mu.Lock()
	h.resolves = append(h.resolves, r)
	fn := h.onResolve
	h.mu.Unlock()
	h.tell()
	if err := r.Valid(); err != nil {
		return ext.ResolvedLaunch{}, refusal(ext.RefusalInvalidRequest, "%v", err)
	}
	if fn != nil {
		return fn(r)
	}
	h.srv.mu.Lock()
	defer h.srv.mu.Unlock()
	return h.srv.resolve(r)
}

// Resolves answers every ResolveLaunch's request, in order.
func (h *FakeHost) Resolves() []ext.ResolveRequest {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.resolves)
}

// resolve is r resolved with the settings now. Under s.mu.
func (s *fakeServer) resolve(r ext.ResolveRequest) (ext.ResolvedLaunch, error) {
	c, ok := s.crews[r.Crew]
	if !ok {
		return ext.ResolvedLaunch{}, fmt.Errorf("%w: no crew %s", ext.ErrNotFound, r.Crew)
	}
	if r.Commit != "" && !c.r.Managed {
		return ext.ResolvedLaunch{}, refusal(ext.RefusalInvalidRequest, "a start commit is a crew with tasks' base, and the crew %s has none", r.Crew)
	}
	if r.Commit != "" && !s.holds(r.Commit) {
		return ext.ResolvedLaunch{}, refusal(ext.RefusalStartCommitMissing, "the folder's repository does not hold %s", r.Commit)
	}
	out := copyResolved(c.r)
	out.Project = r.Project
	if r.Project == "" {
		out.ProjectCwd, out.InProject = "", false
	}
	if blob, ok := s.blobs[r.Commit]; ok && out.Manifest.Source == ext.ManifestFile {
		out.Manifest.Blob = blob
	}
	for i := range out.Members {
		m := &out.Members[i]
		if s.yolo {
			m.Yolo = true
		}
		if sha, ok := s.selected[m.Agent]; ok {
			m.Selection = sha
		}
		if b, ok := s.bindings[m.Name]; ok {
			m.Account, m.AccountSource, m.Model = b.account, b.source, b.model
		}
	}
	out.Capacity = copyViews(s.capacity)
	digest, ok := ext.ResolvedDigest(out, c.rev)
	if !ok {
		return ext.ResolvedLaunch{}, refusal(ext.RefusalResolvedTooLarge, "the resolved launch is past %d bytes", ext.MaxResolvedLaunch)
	}
	out.Digest = digest
	return out, nil
}

// copyResolved is a copy of r that shares nothing with it.
func copyResolved(r ext.ResolvedLaunch) ext.ResolvedLaunch {
	r.Members = slices.Clone(r.Members)
	r.Capacity = copyViews(r.Capacity)
	return r
}

// copyViews is a copy of views that shares nothing with them.
func copyViews(views []ext.CapacityView) []ext.CapacityView {
	if views == nil {
		return nil
	}
	out := slices.Clone(views)
	for i := range out {
		out[i].Windows = slices.Clone(out[i].Windows)
		out[i].Credits = slices.Clone(out[i].Credits)
		for j := range out[i].Credits {
			if e := out[i].Credits[j].Enabled; e != nil {
				v := *e
				out[i].Credits[j].Enabled = &v
			}
		}
	}
	return out
}
