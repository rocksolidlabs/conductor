package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/rocksolidlabs/conductor/internal/api"
	"github.com/rocksolidlabs/conductor/internal/dispatch"
)

// conductor dispatch import, link-issue, unlink-issue, refresh, authority,
// read and publish (docs/plans/tracker-linking.md, "internal/cli"): a
// ticket's GitHub item, read with the token the server keeps for its host,
// and a person's publications to it, through the admin's routes as the
// workbench uses them. Each command waits its route's own deadline and a
// request's time beside it (the *RequestTimeout values below, each
// derived from the route's exported bound). A publication is prepared and
// printed whole with its digest, and posted only by publish send naming
// that digest: nothing here posts without it. Text GitHub holds (a title,
// a description, a check's name) is printed through oneLine and
// textBlock, so nothing it carries reaches the terminal as a control
// sequence.

// The commands of this file, which runDispatch hands here.
var dispatchTrackerArgs = map[string]bool{"import": true, "link-issue": true, "unlink-issue": true, "refresh": true, "authority": true, "read": true, "publish": true}

// publishVerbs is what conductor dispatch publish does beside preparing.
var publishVerbs = []string{"send", "check", "close"}

// Each command's bound: its route's deadline (internal/api) and a
// request's time beside it, for the reply to come back. Variables so a
// test reads what each command waits.
var (
	// importRequestTimeout bounds an import and a refresh (their reads and
	// the images' downloads).
	importRequestTimeout = api.ImportTimeout + requestTimeout
	// trackerRequestTimeout bounds a preview, a link, an unlink, a field
	// given back and a link's read.
	trackerRequestTimeout = api.TrackerReadTimeout + requestTimeout
	// prepareRequestTimeout bounds a publication's prepare.
	prepareRequestTimeout = api.PrepareTimeout + requestTimeout
	// publishRequestTimeout bounds a send (its look, its post, its close).
	publishRequestTimeout = api.PublishTimeout + requestTimeout
	// checkRequestTimeout bounds a Check.
	checkRequestTimeout = api.CheckTimeout + requestTimeout
	// closeRequestTimeout bounds Close now.
	closeRequestTimeout = api.CloseNowTimeout + requestTimeout
	// mergedCloseRequestTimeout bounds an edit to done, which reads the
	// links' pull requests when the project requires merged.
	mergedCloseRequestTimeout = api.MergedCloseTimeout + requestTimeout
)

// trackerFlags is what this file's commands take beside the server's.
type trackerFlags struct {
	project, target, bodyFile, with, closeIssue, digest *string
	noImages, preview                                   *bool
}

// runDispatchTracker is this file's commands. Exit 2 for a usage error,
// before any request; 1 when the server refuses or cannot be asked.
func runDispatchTracker(ctx context.Context, sub string, rest []string, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	name := sub
	if sub == "publish" {
		if verb, others := planVerb(rest); slices.Contains(publishVerbs, verb) {
			name, rest = "publish "+verb, others
		}
	}
	fs := flag.NewFlagSet("dispatch "+name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	server := addAPIFlags(fs)
	var f trackerFlags
	switch name {
	case "import":
		f.project = fs.String("project", "", "the project's key the ticket is made in (required)")
		f.noImages = fs.Bool("no-images", false, "leave the item's images pending: a refresh downloads them")
		f.preview = fs.Bool("preview", false, "read the item and print what an import would make; writes nothing")
	case "refresh":
		f.noImages = fs.Bool("no-images", false, "leave the images still pending as they are")
	case "publish":
		f.target = fs.String("target", "", "where to post: linked (the ticket's linked issue), or a delivery or external link by its id or its URL (required)")
		f.bodyFile = fs.String("body-file", "", "a file of Markdown, the comment's text, - for stdin")
		f.with = fs.String("with", "", "the parts the server offers to add after the text, by name: deliveries, checks, key (comma-separated)")
		f.closeIssue = fs.String("close", "", "close the issue once the comment is posted: completed or not_planned (an issue alone)")
	case "publish send":
		f.digest = fs.String("digest", "", "the SHA-256 the prepare printed, of the exact text that is posted (required)")
	}
	fs.Usage = func() {
		fmt.Fprint(stderr, dispatchUsage, "\nFlags of dispatch "+name+":\n")
		fs.PrintDefaults()
	}
	pos, err := parseInterspersed(fs, rest)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0, nil
		}
		return 2, err
	}
	want := map[string]int{"import": 1, "link-issue": 2, "unlink-issue": 1, "refresh": 1, "authority": 3, "read": 2, "publish": 1, "publish send": 1, "publish check": 1, "publish close": 1}[name]
	forms := map[string]string{"import": "a GitHub issue's or pull request's URL", "link-issue": "a ticket's key and a GitHub issue's or pull request's URL", "unlink-issue": "a ticket's key",
		"refresh": "a ticket's key", "authority": "a ticket's key, title or description, and github or dispatch", "read": "a ticket's key and a link's id or URL",
		"publish": "a ticket's key", "publish send": "a publication's id", "publish check": "a publication's id", "publish close": "a publication's id"}
	if len(pos) != want {
		return 2, fmt.Errorf("dispatch %s takes %s", name, forms[name])
	}
	var key, id string
	switch {
	case strings.HasPrefix(name, "publish "):
		if id = pos[0]; !dispatch.ValidID(id) {
			return 2, fmt.Errorf("dispatch %s: a publication's id is 16 lower-case hex characters", name)
		}
	case name == "import":
		if *f.project == "" || !dispatch.ValidKey(*f.project) {
			return 2, errors.New("dispatch import: --project names the project by its key, as CND")
		}
	default:
		if key = pos[0]; !validTicketKey(key) {
			return 2, fmt.Errorf("dispatch %s: %q is no ticket's key: a project's key and a number, as CND-12", name, oneLine(key))
		}
	}
	var pub *publishDraft
	switch name {
	case "authority":
		if !slices.Contains([]string{"title", "description"}, pos[1]) || !slices.Contains([]string{"github", "dispatch"}, pos[2]) {
			return 2, errors.New("dispatch authority takes the field, title or description, and its owner, github or dispatch")
		}
	case "publish":
		if pub, err = f.draft(stdin); err != nil {
			return 2, fmt.Errorf("dispatch publish: %w", err)
		}
	case "publish send":
		if !sha256Hex(*f.digest) {
			return 2, errors.New("dispatch publish send: --digest is the SHA-256 the prepare printed (64 lower-case hex characters): nothing is posted without it")
		}
	}
	c, err := server.client(stderr)
	if err != nil {
		return 2, err
	}
	ticketPath := "/api/dispatch/tickets/" + url.PathEscape(key)
	switch name {
	case "import":
		if *f.preview {
			return dispatchImportPreview(ctx, c, *f.project, pos[0], stdout)
		}
		body := map[string]any{"url": pos[0]}
		if *f.noImages {
			body["images"] = false
		}
		return trackerWrite(ctx, c, importRequestTimeout, "/api/dispatch/projects/"+url.PathEscape(*f.project)+"/import", body, stdout, func(o io.Writer, out trackerReply) {
			tk := out.Ticket
			fmt.Fprintf(o, "%s imported from %s: %s\n", oneLine(cmpKey(tk, out.Receipt)), oneLine(tkURL(tk, pos[0])), oneLine(tkTitle(tk)))
			imagesWords(o, out.Images)
		})
	case "link-issue":
		return trackerWrite(ctx, c, trackerRequestTimeout, ticketPath+"/external", map[string]any{"url": pos[1]}, stdout, func(o io.Writer, out trackerReply) {
			fmt.Fprintf(o, "%s linked to %s: every field stays Dispatch's\n", key, oneLine(tkURL(out.Ticket, pos[1])))
		})
	case "unlink-issue":
		return trackerWrite(ctx, c, trackerRequestTimeout, ticketPath+"/external/delete", map[string]any{}, stdout, func(o io.Writer, out trackerReply) {
			fmt.Fprintf(o, "%s unlinked: the fields GitHub owned are Dispatch's, with their text\n", key)
		})
	case "refresh":
		body := map[string]any{}
		if *f.noImages {
			body["images"] = false
		}
		return trackerWrite(ctx, c, importRequestTimeout, ticketPath+"/external/refresh", body, stdout, func(o io.Writer, out trackerReply) {
			if out.Changed {
				fmt.Fprintf(o, "%s refreshed: what changed on GitHub is written to the fields GitHub owns\n", key)
			} else {
				fmt.Fprintf(o, "%s refreshed: nothing changed\n", key)
			}
			imagesWords(o, out.Images)
		})
	case "authority":
		field, owner := pos[1], pos[2]
		return trackerWrite(ctx, c, trackerRequestTimeout, ticketPath+"/external/authority", map[string]any{"field": field, "owner": owner}, stdout, func(o io.Writer, out trackerReply) {
			if owner == "github" {
				fmt.Fprintf(o, "%s: its %s is GitHub's again, read from GitHub now\n", key, field)
				return
			}
			fmt.Fprintf(o, "%s: its %s is Dispatch's now, its text kept\n", key, field)
		})
	case "read":
		return dispatchLinkRead(ctx, c, key, pos[1], stdout)
	case "publish":
		return pub.prepare(ctx, c, key, stdout)
	case "publish send":
		return publicationAct(ctx, c, id, "send", publishRequestTimeout, map[string]any{"opId": newOpID(), "postSha256": *f.digest}, stdout)
	case "publish check":
		return publicationAct(ctx, c, id, "check", checkRequestTimeout, map[string]any{}, stdout)
	}
	return publicationAct(ctx, c, id, "close", closeRequestTimeout, map[string]any{"opId": newOpID()}, stdout)
}

// validTicketKey reports a ticket's key, as CND-12.
func validTicketKey(key string) bool {
	_, _, ok := dispatch.ParseKey(key)
	return ok
}

// sha256Hex reports 64 lower-case hex characters.
func sha256Hex(s string) bool {
	if len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if !(s[i] >= '0' && s[i] <= '9' || s[i] >= 'a' && s[i] <= 'f') {
			return false
		}
	}
	return true
}

// trackerReply is a write's reply: the receipt, the ticket, an import's
// or a refresh's images, and whether a refresh changed anything.
type trackerReply struct {
	Receipt dispatch.Receipt     `json:"receipt"`
	Ticket  *dispatch.TicketView `json:"ticket"`
	Images  []importImage        `json:"images"`
	Changed bool                 `json:"changed"`
}

// importImage is an image an import or a refresh reports: made an
// attachment, skipped (its type or its size), or still pending.
type importImage struct {
	ID           string `json:"id"`
	AttachmentID string `json:"attachmentId"`
	Skipped      string `json:"skipped"`
	Pending      bool   `json:"pending"`
}

// trackerWrite mints the write's operation's id, sends it within timeout,
// once more on a lost reply (the server answers the second from its
// receipt), and prints its reply.
func trackerWrite(ctx context.Context, c *apiClient, timeout time.Duration, path string, body map[string]any, stdout io.Writer, print func(io.Writer, trackerReply)) (int, error) {
	body["opId"] = newOpID()
	var out trackerReply
	if err := askTwice(ctx, func(ctx context.Context) error {
		return c.doWithin(ctx, timeout, http.MethodPost, path, body, &out, maxDispatchReply)
	}); err != nil {
		return 1, err
	}
	print(stdout, out)
	return 0, nil
}

func cmpKey(tk *dispatch.TicketView, rc dispatch.Receipt) string {
	if tk != nil && tk.Key != "" {
		return tk.Key
	}
	return rc.Key
}

func tkURL(tk *dispatch.TicketView, given string) string {
	if tk != nil && tk.ExternalURL != "" {
		return tk.ExternalURL
	}
	return given
}

func tkTitle(tk *dispatch.TicketView) string {
	if tk == nil {
		return ""
	}
	return tk.Title
}

// imagesWords says what became of an import's or a refresh's images.
func imagesWords(w io.Writer, images []importImage) {
	var attached, skipped, pending int
	for _, im := range images {
		switch {
		case im.AttachmentID != "":
			attached++
		case im.Skipped != "":
			skipped++
		case im.Pending:
			pending++
		}
	}
	if attached+skipped+pending == 0 {
		return
	}
	fmt.Fprintf(w, "images: %d attached, %d skipped (a type or a size Conductor does not keep), %d pending", attached, skipped, pending)
	if pending > 0 {
		fmt.Fprint(w, " (a refresh downloads them)")
	}
	fmt.Fprintln(w)
}

// dispatchImportPreview prints what an import of the item would make,
// writing nothing.
func dispatchImportPreview(ctx context.Context, c *apiClient, project, item string, stdout io.Writer) (int, error) {
	var out struct {
		Preview struct {
			URL               string          `json:"url"`
			Title             string          `json:"title"`
			Description       string          `json:"description"`
			DescriptionLength int             `json:"descriptionLength"`
			Images            []string        `json:"images"`
			Repro             string          `json:"repro"`
			State             string          `json:"state"`
			StateReason       string          `json:"stateReason"`
			Labels            []string        `json:"labels"`
			Assignees         []string        `json:"assignees"`
			Owners            dispatch.Owners `json:"owners"`
			Linked            string          `json:"linked"`
		} `json:"preview"`
	}
	if err := c.doWithin(ctx, trackerRequestTimeout, http.MethodPost, "/api/dispatch/external/preview", map[string]any{"url": item, "project": project}, &out, maxDispatchReply); err != nil {
		return 1, err
	}
	p := out.Preview
	fmt.Fprintf(stdout, "# %s\n\n- item: %s\n- state: %s", oneLine(p.Title), oneLine(p.URL), oneLine(p.State))
	if p.StateReason != "" {
		fmt.Fprintf(stdout, " (%s)", oneLine(p.StateReason))
	}
	fmt.Fprintf(stdout, "\n- the title is %s's, the description %s's\n", oneLine(p.Owners.Title), oneLine(p.Owners.Description))
	if len(p.Labels) > 0 {
		fmt.Fprintf(stdout, "- GitHub's labels: %s\n", oneLine(strings.Join(p.Labels, ", ")))
	}
	if len(p.Assignees) > 0 {
		fmt.Fprintf(stdout, "- GitHub's assignees: %s\n", oneLine(strings.Join(p.Assignees, ", ")))
	}
	if len(p.Images) > 0 {
		fmt.Fprintf(stdout, "- images: %d, made attachments\n", len(p.Images))
	}
	if p.Linked != "" {
		fmt.Fprintf(stdout, "- linked already: %s has it as its linked issue, so an import is refused\n", oneLine(p.Linked))
	}
	if strings.TrimSpace(p.Description) != "" {
		fmt.Fprintf(stdout, "\n## Description\n\n%s\n", strings.TrimRight(textBlock(p.Description), "\n"))
		if len(p.Description) < p.DescriptionLength {
			fmt.Fprintf(stdout, "\n(the first %d of its %d bytes)\n", len(p.Description), p.DescriptionLength)
		}
	}
	if strings.TrimSpace(p.Repro) != "" {
		fmt.Fprintf(stdout, "\n## Steps to reproduce\n\n%s\n", strings.TrimRight(textBlock(p.Repro), "\n"))
	}
	fmt.Fprintln(stdout, "\nnothing was written: import it with conductor dispatch import URL --project "+project)
	return 0, nil
}

// ticketOf reads the ticket whole.
func ticketOf(ctx context.Context, c *apiClient, key string) (dispatch.TicketView, error) {
	var tv struct {
		Ticket dispatch.TicketView `json:"ticket"`
	}
	err := c.doLimit(ctx, http.MethodGet, "/api/dispatch/tickets/"+url.PathEscape(key), nil, &tv, maxDispatchReply)
	return tv.Ticket, err
}

// linkIDOf is the id of the ticket's live delivery or external link that
// ref names: its id itself, else its URL exactly. A URL two links share
// is refused rather than guessed between, naming each one's id, kind and
// ref (dispatch show prints each link's id too).
func linkIDOf(key string, tk dispatch.TicketView, ref string) (string, error) {
	var found []dispatch.Link
	for _, l := range tk.Links {
		if !l.DeletedAt.IsZero() || (l.Kind != dispatch.LinkDelivery && l.Kind != dispatch.LinkExternal) {
			continue
		}
		if l.ID == ref {
			return l.ID, nil
		}
		if l.URL == ref {
			found = append(found, l)
		}
	}
	switch len(found) {
	case 0:
		return "", fmt.Errorf("%s has no delivery or external link %s: name it by its id or its URL", key, oneLine(ref))
	case 1:
		return found[0].ID, nil
	}
	ids := make([]string, len(found))
	for i, l := range found {
		ids[i] = l.ID + " (" + oneLine(l.Kind)
		if l.Ref != "" && l.Ref != l.URL {
			ids[i] += ", " + oneLine(l.Ref)
		}
		ids[i] += ")"
	}
	return "", fmt.Errorf("%s has %d links to %s: name one by its id, %s", key, len(found), oneLine(ref), strings.Join(ids, ", "))
}

// dispatchLinkRead reads what GitHub says of the ticket's link now, and
// prints it.
func dispatchLinkRead(ctx context.Context, c *apiClient, key, ref string, stdout io.Writer) (int, error) {
	id := ref
	if !dispatch.ValidID(ref) {
		tk, err := ticketOf(ctx, c, key)
		if err != nil {
			return 1, err
		}
		if id, err = linkIDOf(key, tk, ref); err != nil {
			return 1, err
		}
	}
	var out struct {
		Read *dispatch.LinkRead `json:"read"`
	}
	if err := c.doWithin(ctx, trackerRequestTimeout, http.MethodPost, "/api/dispatch/tickets/"+url.PathEscape(key)+"/links/"+url.PathEscape(id)+"/read", map[string]any{}, &out, maxDispatchReply); err != nil {
		return 1, err
	}
	if out.Read == nil {
		fmt.Fprintf(stdout, "%s link %s: not read\n", key, id)
		return 0, nil
	}
	writeLinkRead(stdout, key, id, *out.Read)
	return 0, nil
}

// writeLinkRead prints a link's read: the item's state, a pull request's
// merge and head, its checks one a line, and why the set is not whole.
func writeLinkRead(w io.Writer, key, id string, r dispatch.LinkRead) {
	state := oneLine(r.State)
	if r.Merged {
		state = "merged"
		if !r.MergedAt.IsZero() {
			state += " " + when(r.MergedAt)
		}
		if r.MergeSha != "" {
			state += " as " + short(oneLine(r.MergeSha))
		}
	}
	fmt.Fprintf(w, "%s link %s: a %s, %s", key, id, oneLine(r.Kind), state)
	if !r.ReadAt.IsZero() {
		fmt.Fprintf(w, ", read %s", when(r.ReadAt))
	}
	fmt.Fprintln(w)
	if r.Error != "" {
		fmt.Fprintf(w, "the last read failed: %s\n", oneLine(r.Error))
	}
	if r.HeadSha != "" {
		fmt.Fprintf(w, "checks at %s:\n", short(oneLine(r.HeadSha)))
	}
	for _, ch := range r.Checks {
		fmt.Fprintf(w, "  %s  %s", oneLine(ch.Name), oneLine(ch.Status))
		if ch.Conclusion != "" {
			fmt.Fprintf(w, " %s", oneLine(ch.Conclusion))
		}
		fmt.Fprintln(w)
	}
	if in := r.Incomplete; in != nil {
		fmt.Fprintf(w, "not every check is here: %s", oneLine(strings.Join(in.Words, "; ")))
		if in.Omitted > 0 {
			fmt.Fprintf(w, " (%d left out)", in.Omitted)
		}
		fmt.Fprintln(w)
	}
}

// publishDraft is a publication as its flags give it, before the
// ticket's offer and links are read: the target, the text, the parts to
// add after it and the close.
type publishDraft struct {
	target, text, closeIssue string
	parts                    []string
}

// draft checks publish's flags and reads the text, before any request:
// its errors are usage errors.
func (f trackerFlags) draft(stdin io.Reader) (*publishDraft, error) {
	d := &publishDraft{target: strings.TrimSpace(*f.target), closeIssue: *f.closeIssue}
	if d.target == "" {
		return nil, errors.New("--target names where to post: linked, or a delivery or external link by its id or its URL")
	}
	if d.closeIssue != "" && d.closeIssue != "completed" && d.closeIssue != "not_planned" {
		return nil, errors.New("--close is completed or not_planned")
	}
	if *f.with != "" {
		for p := range strings.SplitSeq(*f.with, ",") {
			p = strings.TrimSpace(p)
			if !slices.Contains(dispatch.PublicationParts, p) || slices.Contains(d.parts, p) {
				return nil, fmt.Errorf("--with names the parts, each once: %s", strings.Join(dispatch.PublicationParts, ", "))
			}
			d.parts = append(d.parts, p)
		}
	}
	if *f.bodyFile != "" {
		text, err := readMarkdownFile(*f.bodyFile, stdin, dispatch.MaxPublicationBody, "a publication's text")
		if err != nil {
			return nil, err
		}
		d.text = strings.TrimSpace(text)
	}
	if d.text == "" && len(d.parts) == 0 {
		return nil, errors.New("say what to publish: --body-file, --with, or both")
	}
	return d, nil
}

// prepare reads the ticket's offer for the parts and its links for a
// link's URL, composes the text (the person's, then each part's Markdown
// as the server offers it, a blank line between), prepares the
// publication, and prints the exact text a send posts with its digest:
// nothing is posted.
func (d *publishDraft) prepare(ctx context.Context, c *apiClient, key string, stdout io.Writer) (int, error) {
	ticketPath := "/api/dispatch/tickets/" + url.PathEscape(key)
	target := "linked"
	if d.target != "linked" {
		id := d.target
		if !dispatch.ValidID(id) {
			tk, err := ticketOf(ctx, c, key)
			if err != nil {
				return 1, err
			}
			if id, err = linkIDOf(key, tk, d.target); err != nil {
				return 1, err
			}
		}
		target = "link:" + id
	}
	pieces := []string{}
	if d.text != "" {
		pieces = append(pieces, d.text)
	}
	if len(d.parts) > 0 {
		var offer struct {
			Parts []struct {
				Name     string `json:"name"`
				Markdown string `json:"markdown"`
			} `json:"parts"`
		}
		if err := c.doLimit(ctx, http.MethodGet, ticketPath+"/publish/offer", nil, &offer, maxDispatchReply); err != nil {
			return 1, err
		}
		for _, name := range d.parts {
			i := slices.IndexFunc(offer.Parts, func(p struct {
				Name     string `json:"name"`
				Markdown string `json:"markdown"`
			}) bool {
				return p.Name == name
			})
			if i < 0 || strings.TrimSpace(offer.Parts[i].Markdown) == "" {
				return 1, fmt.Errorf("%s offers no %s part now", key, name)
			}
			pieces = append(pieces, strings.TrimSpace(offer.Parts[i].Markdown))
		}
	}
	body := map[string]any{"opId": newOpID(), "target": target, "body": strings.Join(pieces, "\n\n")}
	if len(d.parts) > 0 {
		body["parts"] = d.parts
	}
	if d.closeIssue != "" {
		body["closeIssue"] = d.closeIssue
	}
	var out struct {
		Publication dispatch.PublicationSummary `json:"publication"`
		Preview     string                      `json:"preview"`
		PostSha256  string                      `json:"postSha256"`
	}
	if err := askTwice(ctx, func(ctx context.Context) error {
		return c.doWithin(ctx, prepareRequestTimeout, http.MethodPost, ticketPath+"/publications", body, &out, maxDispatchReply)
	}); err != nil {
		return 1, err
	}
	p := out.Publication
	fmt.Fprintf(stdout, "%s: publication %s prepared for %s, nothing posted\n", key, oneLine(p.ID), oneLine(cmpOrString(p.TargetURL, p.TargetRef, target)))
	if p.CloseIssue != "" {
		fmt.Fprintf(stdout, "once posted, the issue is closed as %s\n", oneLine(p.CloseIssue))
	}
	fmt.Fprintf(stdout, "--- the exact text a send posts, its SHA-256 %s ---\n%s\n--- end of the text ---\n", oneLine(out.PostSha256), strings.TrimRight(textBlock(out.Preview), "\n"))
	fmt.Fprintf(stdout, "post it once with: conductor dispatch publish send %s --digest %s\n", oneLine(p.ID), oneLine(out.PostSha256))
	return 0, nil
}

// cmpOrString is the first of ss that is not empty.
func cmpOrString(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

// publicationAct sends one of a publication's acts (send, check, close)
// within timeout, and prints where it stands. A send and a close are
// sent once more on a lost reply, with the same operation id, which the
// server answers from its receipt: a comment is never posted twice. A
// check names no operation and is asked once.
func publicationAct(ctx context.Context, c *apiClient, id, verb string, timeout time.Duration, body map[string]any, stdout io.Writer) (int, error) {
	path := "/api/dispatch/publications/" + url.PathEscape(id) + "/" + verb
	var out struct {
		Publication dispatch.PublicationSummary `json:"publication"`
	}
	send := func(ctx context.Context) error {
		return c.doWithin(ctx, timeout, http.MethodPost, path, body, &out, maxDispatchReply)
	}
	var err error
	if _, op := body["opId"]; op {
		err = askTwice(ctx, send)
	} else {
		err = send(ctx)
	}
	if err != nil {
		return 1, err
	}
	writePublication(stdout, out.Publication)
	return 0, nil
}

// writePublication prints a publication's state: sent with its comment's
// address, or why not, and its close.
func writePublication(w io.Writer, p dispatch.PublicationSummary) {
	fmt.Fprintf(w, "publication %s: %s", oneLine(p.ID), publicationStateWords(p.State))
	if p.RemoteURL != "" {
		fmt.Fprintf(w, " as %s", oneLine(p.RemoteURL))
	}
	if p.SentAsLogin != "" {
		fmt.Fprintf(w, " by @%s", oneLine(p.SentAsLogin))
	}
	fmt.Fprintln(w)
	if p.Error != "" {
		fmt.Fprintf(w, "  %s\n", oneLine(p.Error))
	}
	if p.CloseIssue != "" && p.CloseState != "" {
		fmt.Fprintf(w, "  the close (%s): %s", oneLine(p.CloseIssue), oneLine(p.CloseState))
		if p.CloseError != "" {
			fmt.Fprintf(w, ", %s", oneLine(p.CloseError))
		}
		fmt.Fprintln(w)
	}
}

// publicationStateWords is a publication's state in words.
func publicationStateWords(state string) string {
	switch state {
	case "draft":
		return "prepared, not sent"
	case "sending":
		return "being sent: conductor dispatch publish check looks for its comment"
	case "sent":
		return "sent"
	case "failed":
		return "not posted: send it again"
	case "unconfirmed":
		return "not confirmed: never sent again; check looks for its comment, or prepare a new one"
	case "discarded":
		return "discarded"
	}
	return oneLine(state)
}
