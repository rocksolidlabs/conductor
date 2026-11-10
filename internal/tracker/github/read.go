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
// the item returned is whole and current. A read answered from a redirect
// (a renamed or transferred repository) is the item at its new place when
// its node id is the one v holds, and *tracker.ErrMoved when it is
// another, as is a node id that changed at the same place.
func (a *Adapter) Read(ctx context.Context, c tracker.Credential, r tracker.Ref, v tracker.Validators) (tracker.Item, bool, error) {
	if r.System != tracker.GitHub || (r.Kind != tracker.KindIssue && r.Kind != tracker.KindChange) {
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
		if merr := moved(r, v, issue); merr != nil {
			return tracker.Item{}, false, &tracker.RequestError{System: tracker.GitHub, Method: iss.r.method, Path: ep.errPath(iss.r), Status: iss.ans.status, Err: merr}
		}
	}
	if !isPull && iss.ans.status == http.StatusNotModified {
		return tracker.Item{}, true, nil
	}
	var pull pullJSON
	pullETag := ""
	if isPull {
		space := r.Space
		if iss.ans.status == http.StatusOK {
			space = placeOf(r, issue).Space
		}
		pl, err := a.half(ctx, c, ep, "/repos/"+space+"/pulls/"+r.ID, "", v.Pull)
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
			if merr := moved(r, v, issue); merr != nil {
				return tracker.Item{}, false, &tracker.RequestError{System: tracker.GitHub, Method: iss.r.method, Path: ep.errPath(iss.r), Status: iss.ans.status, Err: merr}
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

// moved is the error of an answer whose node id is not the one v holds.
// A redirect or not, another node id is another item.
func moved(r tracker.Ref, v tracker.Validators, issue issueJSON) error {
	if v.NodeID == "" || issue.NodeID == v.NodeID {
		return nil
	}
	return &tracker.ErrMoved{To: placeOf(r, issue)}
}

// placeOf is the item's place as its answer says it (html_url), or r's.
func placeOf(r tracker.Ref, issue issueJSON) tracker.Ref {
	out := r
	if issue.PullRequest != nil {
		out.Kind = tracker.KindChange
	} else {
		out.Kind = tracker.KindIssue
	}
	u, err := url.Parse(issue.HTMLURL)
	if err != nil {
		return out
	}
	segs := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(segs) == 4 && (segs[2] == "issues" || segs[2] == "pull") && segs[3] == r.ID {
		if ref, err := tracker.ParseGitHub(segs[0]+"/"+segs[1]+"#"+segs[3], "", tracker.ParseOptions{}); err == nil {
			out.Space = ref.Space
		}
	}
	return out
}

// item makes the Item of an issue's answer, cut to the tracker's bounds.
func (a *Adapter) item(r tracker.Ref, issue issueJSON, etag string) tracker.Item {
	place := placeOf(r, issue)
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
	it.Images = imagesOf(place.Host, issue.BodyHTML)
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
