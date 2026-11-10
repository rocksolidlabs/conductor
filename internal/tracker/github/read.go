package github

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/rocksolidlabs/conductor/internal/tracker"
)

// issueJSON is what Read takes of GET /repos/{owner}/{repo}/issues/{n}.
type issueJSON struct {
	ID          json.Number `json:"id"`
	NodeID      string      `json:"node_id"`
	HTMLURL     string      `json:"html_url"`
	Title       string      `json:"title"`
	Body        *string     `json:"body"`
	BodyHTML    string      `json:"body_html"`
	State       string      `json:"state"`
	StateReason *string     `json:"state_reason"`
	Labels      []struct {
		Name string `json:"name"`
	} `json:"labels"`
	Assignees []struct {
		Login string `json:"login"`
	} `json:"assignees"`
	Milestone *struct {
		Title string `json:"title"`
	} `json:"milestone"`
	User *struct {
		Login string      `json:"login"`
		ID    json.Number `json:"id"`
	} `json:"user"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	ClosedAt    *time.Time `json:"closed_at"`
	PullRequest *struct {
		URL string `json:"url"`
	} `json:"pull_request"`
}

// pullJSON is what Read takes of GET /repos/{owner}/{repo}/pulls/{n}.
type pullJSON struct {
	Merged         bool       `json:"merged"`
	MergedAt       *time.Time `json:"merged_at"`
	MergeCommitSHA *string    `json:"merge_commit_sha"`
	Head           struct {
		SHA string `json:"sha"`
	} `json:"head"`
	Base struct {
		Ref string `json:"ref"`
	} `json:"base"`
}

// half is one of a read's two requests answered.
type half struct {
	ans answer
	r   request
}

// Read reads an issue or a pull request: GET …/issues/{n} with the full
// media type (body and body_html), and for a pull request GET …/pulls/{n}
// for its merge and head, each with its own If-None-Match. The item is
// not modified only when every request it took answered 304; a 304 beside
// a 200 reads the one that answered 304 again without its validator, so
// the item returned is whole and current. The item's place is the one its
// answer gives (html_url: its repository and its number as they are now),
// so a read answered from a redirect (a renamed repository, an issue
// transferred to another repository and so another number) is the item at
// its new place when its node id is the one v holds, and *tracker.ErrMoved
// naming that new place when it is another, as is a node id that changed
// at the same place; a redirected answer whose place cannot be read is
// refused. r is held to tracker.Ref.Check before any of it goes into a
// path.
func (a *Adapter) Read(ctx context.Context, c tracker.Credential, r tracker.Ref, v tracker.Validators) (tracker.Item, bool, error) {
	if err := r.Check(); err != nil {
		return tracker.Item{}, false, &tracker.RequestError{System: tracker.GitHub, Method: "-", Path: "-", Err: tracker.ErrUnsupported, Words: "not a GitHub item"}
	}
	ep, err := a.endpointFor(c, r.Host)
	if err != nil {
		return tracker.Item{}, false, err
	}
	base := "/repos/" + r.Space
	iss, err := a.half(ctx, c, ep, base+"/issues/"+r.ID, "application/vnd.github.full+json", v.Issue)
	if err != nil {
		return tracker.Item{}, false, err
	}
	var issue issueJSON
	isPull := r.Kind == tracker.KindChange
	if iss.ans.status == http.StatusOK {
		if err := decode(ep, iss.r, iss.ans.body, &issue); err != nil {
			return tracker.Item{}, false, err
		}
		isPull = issue.PullRequest != nil
		if err := a.placed(ep, iss, r, v, issue); err != nil {
			return tracker.Item{}, false, err
		}
	}
	if !isPull && iss.ans.status == http.StatusNotModified {
		return tracker.Item{}, true, nil
	}
	var pull pullJSON
	pullETag := ""
	if isPull {
		place := r
		if iss.ans.status == http.StatusOK {
			place, _ = placeOf(r, issue)
		}
		pl, err := a.half(ctx, c, ep, "/repos/"+place.Space+"/pulls/"+place.ID, "", v.Pull)
		if err != nil {
			return tracker.Item{}, false, err
		}
		if pl.ans.status == http.StatusNotModified && iss.ans.status == http.StatusNotModified {
			return tracker.Item{}, true, nil
		}
		if pl.ans.status == http.StatusNotModified {
			if pl, err = a.half(ctx, c, ep, pl.r.path, "", ""); err != nil {
				return tracker.Item{}, false, err
			}
		}
		if iss.ans.status == http.StatusNotModified {
			if iss, err = a.half(ctx, c, ep, iss.r.path, "application/vnd.github.full+json", ""); err != nil {
				return tracker.Item{}, false, err
			}
			if err := decode(ep, iss.r, iss.ans.body, &issue); err != nil {
				return tracker.Item{}, false, err
			}
			if err := a.placed(ep, iss, r, v, issue); err != nil {
				return tracker.Item{}, false, err
			}
		}
		if err := decode(ep, pl.r, pl.ans.body, &pull); err != nil {
			return tracker.Item{}, false, err
		}
		pullETag = pl.ans.header.Get("ETag")
	}
	item := a.item(r, issue, iss.ans.header.Get("ETag"))
	if isPull {
		item.Ref.Kind = tracker.KindChange
		item.Merged = pull.Merged
		if pull.MergedAt != nil {
			item.MergedAt = *pull.MergedAt
		}
		if pull.MergeCommitSHA != nil && pull.Merged {
			item.MergeSHA = *pull.MergeCommitSHA
		}
		item.HeadSHA = pull.Head.SHA
		item.BaseRef = pull.Base.Ref
		if pull.Merged {
			item.State = tracker.StateMerged
		}
		item.Validators.Pull = pullETag
	}
	return item, false, nil
}

// half makes one of a read's GETs; a 304 is an answer, not an error.
func (a *Adapter) half(ctx context.Context, c tracker.Credential, ep endpoint, path, accept, etag string) (half, error) {
	r := request{method: http.MethodGet, path: path, accept: accept, etag: etag, max: MaxItemAnswer}
	ans, err := a.do(ctx, c, ep, r)
	return half{ans: ans, r: r}, err
}

// placed checks an issue's answer against what the caller holds: a
// redirected answer whose place cannot be read is refused, and an answer
// whose node id is not the one v holds is *tracker.ErrMoved naming the
// item's place now. A redirect or not, another node id is another item.
func (a *Adapter) placed(ep endpoint, h half, r tracker.Ref, v tracker.Validators, issue issueJSON) error {
	place, ok := placeOf(r, issue)
	if !ok && h.ans.redirected {
		return &tracker.RequestError{System: tracker.GitHub, Method: h.r.method, Path: ep.errPath(h.r), Status: h.ans.status, Err: tracker.ErrUnsupported, Words: "the item moved and its new place cannot be read"}
	}
	if v.NodeID != "" && issue.NodeID != v.NodeID {
		return &tracker.RequestError{System: tracker.GitHub, Method: h.r.method, Path: ep.errPath(h.r), Status: h.ans.status, Err: &tracker.ErrMoved{To: place}}
	}
	return nil
}

// placeOf is the item's place as its answer says it: html_url's owner,
// name and number, when it is on r's host byte for byte and reads as a
// reference (ok), whatever the number (an issue transferred to another
// repository has another); r's place, with the answer's kind, otherwise.
func placeOf(r tracker.Ref, issue issueJSON) (tracker.Ref, bool) {
	out := r
	if issue.PullRequest != nil {
		out.Kind = tracker.KindChange
	} else {
		out.Kind = tracker.KindIssue
	}
	u, err := url.Parse(issue.HTMLURL)
	if err != nil || u.Host != r.Host || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return out, false
	}
	segs := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(segs) != 4 || (segs[2] != "issues" && segs[2] != "pull") {
		return out, false
	}
	ref, err := tracker.ParseGitHub(segs[0]+"/"+segs[1]+"#"+segs[3], "", tracker.ParseOptions{})
	if err != nil {
		return out, false
	}
	out.Space, out.ID = ref.Space, ref.ID
	return out, true
}

// item makes the Item of an issue's answer, cut to the tracker's bounds.
func (a *Adapter) item(r tracker.Ref, issue issueJSON, etag string) tracker.Item {
	place, _ := placeOf(r, issue)
	it := tracker.Item{
		Ref:        place,
		NodeID:     cut(issue.NodeID, tracker.MaxNodeID),
		DatabaseID: remoteID(issue.ID),
		URL:        itemURL(place, issue.HTMLURL),
		State:      tracker.StateOpen,
		CreatedAt:  issue.CreatedAt,
		UpdatedAt:  issue.UpdatedAt,
		Validators: tracker.Validators{Issue: etag, NodeID: issue.NodeID},
	}
	it.Title, _ = tracker.CutBytes(issue.Title, tracker.MaxTitle)
	if issue.Body != nil {
		it.Body, it.BodyTruncated = tracker.CutBytes(*issue.Body, tracker.MaxBody)
	}
	if issue.State == "closed" {
		it.State = tracker.StateClosed
	}
	if issue.StateReason != nil {
		switch *issue.StateReason {
		case "completed", "not_planned", "reopened":
			it.StateReason = *issue.StateReason
		}
	}
	for _, l := range issue.Labels {
		if len(it.Labels) < tracker.MaxLabels && l.Name != "" {
			it.Labels = append(it.Labels, cut(l.Name, 100))
		}
	}
	for _, u := range issue.Assignees {
		if len(it.Assignees) < tracker.MaxAssignees && u.Login != "" {
			it.Assignees = append(it.Assignees, cut(u.Login, 39))
		}
	}
	if issue.Milestone != nil {
		it.Milestone = cut(issue.Milestone.Title, 255)
	}
	if issue.User != nil {
		it.Author = cut(issue.User.Login, 39)
		it.AuthorID = remoteID(issue.User.ID)
	}
	if issue.ClosedAt != nil {
		it.ClosedAt = *issue.ClosedAt
	}
	it.Images, it.ImagesLeftOut = imagesOf(place.Host, issue.BodyHTML)
	return it
}

// itemURL is the item's html_url when it is on the item's host, or its
// URL made from the reference.
func itemURL(r tracker.Ref, html string) string {
	seg := "issues"
	if r.Kind == tracker.KindChange {
		seg = "pull"
	}
	made := "https://" + r.Host + "/" + r.Space + "/" + seg + "/" + r.ID
	u, err := url.Parse(html)
	if err != nil || u.Host != r.Host || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "https" && u.Scheme != "http") {
		return made
	}
	return u.String()
}

func cut(s string, n int) string {
	out, _ := tracker.CutBytes(s, n)
	return out
}
