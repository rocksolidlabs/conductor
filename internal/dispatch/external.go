package dispatch

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// A ticket's linked issue (Program item IN1a, docs/plans/tracker-linking.md,
// "internal/dispatch"): the one item of an outside tracker (a GitHub issue
// or pull request) a ticket mirrors, kept in the four columns DS1
// reserved: external_id, its identity, <system>:<host>:<node id>;
// external_url, its page; external_snapshot, what the system said at the
// last read that changed it (ExternalSnapshot); authority, which side owns
// the title and the description (Authority). An import makes a ticket of
// an item, GitHub owning both fields; a link sets a ticket's linked issue,
// Dispatch owning both; the person moves either (SetAuthority). A field
// the system owns is written by a refresh and by giving it back alone
// (ticket.edit refuses it: external_owned); a field Dispatch owns is never
// written by a read. Status, labels, assignees, milestone and owner are
// always Dispatch's, the system's shown from the snapshot. The store
// never reads the tracker: the API reads it and hands each operation the
// read as an argument apart from the request (ExternalRead), so an
// operation's fingerprint is the client's request alone and a resend is a
// replay. One ticket per linked item is a rule of the local write
// (external_linked), never an index. The reads themselves, ticket_reads
// and link_reads, are derived and local: never a changes row, never a
// receipt, never replicated (RecordTicketRead, reads.go).

// SourceNative is a ticket's source when it was made in Dispatch; an
// imported one's is its item's system.
const SourceNative = "native"

// OwnerDispatch is a field's owner when Dispatch owns it; the other owner
// is the linked item's system.
const OwnerDispatch = "dispatch"

// The fields a linked item's system may own.
const (
	FieldTitle       = "title"
	FieldDescription = "description"
)

// The kinds of an outside item: an issue, or a change (a pull request).
const (
	ExternalIssue  = "issue"
	ExternalChange = "change"
)

// The states of an outside item, and the reasons GitHub gives a close.
var (
	externalStates  = []string{"open", "closed", "merged"}
	externalReasons = []string{"", "completed", "not_planned", "reopened"}
)

// SnapshotVersion is the version of ExternalSnapshot this store writes.
const SnapshotVersion = 1

var (
	// hostPattern is a tracker's host: a lower-case DNS name, a port beside
	// it only for a loopback host (the tests' fake).
	hostPattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9.-]*[a-z0-9])?(:[0-9]{1,5})?$`)
	// nodeIDPattern is a system's stable id of an item (GitHub's node id).
	nodeIDPattern = regexp.MustCompile(`^[A-Za-z0-9_=-]{1,100}$`)
	// validatorPattern is a read's validator (an ETag): printable ASCII.
	validatorPattern = regexp.MustCompile(`^[\x21-\x7e]{0,200}$`)
)

// ExternalSnapshot is what the linked item's system said at the last read
// that changed it (version 1): its place (system, host, space, kind, id),
// its identity (nodeId) and database id, its page, its whole title, its
// body by its digest alone (bodySha256, bodyBytes, bodyTruncated), its
// state, a pull request's merge, head and base, its labels, assignees and
// milestone (shown, never mapped onto Dispatch's), its author, its times
// by the system's clock, its steps to reproduce and their digest, the
// digest of the description FromExternal makes of its body (the text a
// system-owned description holds, which a merge finds by it), and the
// import's image manifest (at most MaxImportImages). It holds no read
// time and no validator (ticket_reads keeps them), so two reads of an
// unchanged item make the same snapshot. At most MaxExternalSnapshot as
// encoded.
type ExternalSnapshot struct {
	V                 int             `json:"v"`
	System            string          `json:"system"`
	Host              string          `json:"host"`
	Space             string          `json:"space"`
	Kind              string          `json:"kind"`
	ID                string          `json:"id"`
	NodeID            string          `json:"nodeId"`
	DatabaseID        string          `json:"databaseId,omitempty"`
	URL               string          `json:"url"`
	Title             string          `json:"title"`
	BodySha256        string          `json:"bodySha256"`
	BodyBytes         int             `json:"bodyBytes"`
	BodyTruncated     bool            `json:"bodyTruncated,omitempty"`
	State             string          `json:"state"`
	StateReason       string          `json:"stateReason,omitempty"`
	Merged            bool            `json:"merged,omitempty"`
	MergedAt          time.Time       `json:"mergedAt,omitzero"`
	MergeSha          string          `json:"mergeSha,omitempty"`
	HeadSha           string          `json:"headSha,omitempty"`
	BaseRef           string          `json:"baseRef,omitempty"`
	Labels            []string        `json:"labels"`
	LabelsTotal       int             `json:"labelsTotal"`
	Assignees         []string        `json:"assignees"`
	Milestone         string          `json:"milestone,omitempty"`
	Author            string          `json:"author,omitempty"`
	AuthorID          string          `json:"authorId,omitempty"`
	CreatedAt         time.Time       `json:"createdAt,omitzero"`
	UpdatedAt         time.Time       `json:"updatedAt"`
	ClosedAt          time.Time       `json:"closedAt,omitzero"`
	Repro             string          `json:"repro,omitempty"`
	ReproSha256       string          `json:"reproSha256,omitempty"`
	DescriptionSha256 string          `json:"descriptionSha256"`
	Images            []SnapshotImage `json:"images"`
}

// Identity is the snapshot's item's identity, <system>:<host>:<node id>.
func (sn ExternalSnapshot) Identity() string { return ExternalIdentity(sn.System, sn.Host, sn.NodeID) }

// SnapshotImage is an image of an import's manifest: its id (its URL
// without query or fragment), and once it is done the attachment made of
// it with its content's SHA-256, or why it was skipped (SkippedType,
// SkippedSize: refused once, never fetched again). An entry with neither
// is pending.
type SnapshotImage struct {
	ID           string `json:"id"`
	AttachmentID string `json:"attachmentId,omitempty"`
	SHA256       string `json:"sha256,omitempty"`
	Skipped      string `json:"skipped,omitempty"`
}

// The reasons an image of a manifest is skipped.
const (
	SkippedType = "type"
	SkippedSize = "size"
)

// ImageTypes is the content types an imported image's attachment is made
// with, its type as the API sniffed it; any other image is skipped
// (SkippedType).
var ImageTypes = []string{"image/png", "image/jpeg", "image/gif", "image/webp"}

// done reports whether the image needs no more work: an attachment made of
// it, or a skip recorded.
func (im SnapshotImage) done() bool { return im.AttachmentID != "" || im.Skipped != "" }

// ExternalRead is a read of a linked item as the API hands it to an
// operation, apart from the request: the snapshot as the read gives it
// (the store derives its digests, cleans and cuts its texts, and keeps
// the manifest's done entries by image id), the item's body as the system
// gave it (its Markdown, which FromExternal makes the description of), and
// the read's validators, which ticket_reads keeps.
type ExternalRead struct {
	Snapshot  ExternalSnapshot
	Body      string
	IssueETag string
	PullETag  string
}

// Authority is a ticket's field owners as its authority column holds them:
// Of, the first 16 hex digits of the SHA-256 of the linked item's identity
// the owners were set for (so a merge can tell which item an owner change
// belongs to), and the owner of the title and of the description, the
// linked item's system or "" for Dispatch. An empty column is every field
// Dispatch's.
type Authority struct {
	Of          string `json:"of,omitempty"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
}

// Owners is a ticket's field owners as a read shows them: each field's
// owner, OwnerDispatch or the linked item's system.
type Owners struct {
	Title       string `json:"title"`
	Description string `json:"description"`
}

// ParseAuthority reads an authority column; anything unreadable is every
// field Dispatch's.
func ParseAuthority(raw string) Authority {
	var a Authority
	if raw == "" || json.Unmarshal([]byte(raw), &a) != nil {
		return Authority{}
	}
	return a
}

// owner is the system that owns the field, "" for Dispatch.
func (a Authority) owner(field string) string {
	switch field {
	case FieldTitle:
		return a.Title
	case FieldDescription:
		return a.Description
	}
	return ""
}

// with is a with the field's owner set ("" for Dispatch).
func (a Authority) with(field, owner string) Authority {
	switch field {
	case FieldTitle:
		a.Title = owner
	case FieldDescription:
		a.Description = owner
	}
	return a
}

// Owners is a's owners as words name them.
func (a Authority) Owners() Owners {
	o := Owners{Title: OwnerDispatch, Description: OwnerDispatch}
	if a.Title != "" {
		o.Title = a.Title
	}
	if a.Description != "" {
		o.Description = a.Description
	}
	return o
}

// encode is a as its column holds it: "" for the zero value.
func (a Authority) encode() string {
	if a == (Authority{}) {
		return ""
	}
	v, _ := encodeValue(a)
	return v
}

// authorityKey is Authority.Of of the identity: the first 16 hex digits of
// its SHA-256.
func authorityKey(identity string) string { return sha256Hex(identity)[:16] }

// ExternalIdentity is a linked item's identity: <system>:<host>:<node id>,
// so two hosts that issue one node id hold two items.
func ExternalIdentity(system, host, nodeID string) string { return system + ":" + host + ":" + nodeID }

// systemOf is the system an identity names, "" for none.
func systemOf(identity string) string {
	system, _, _ := strings.Cut(identity, ":")
	return system
}

// checkSystem refuses a system the store was not opened with (invalid):
// the one door every local write that makes or keeps a system the owner of
// a ticket's field goes through (ticket.import, ticket.link_external,
// ticket.refresh, ticket.authority giving a field to the system). Taking a
// field for Dispatch never asks it, nor does an unlink.
func (t *tx) checkSystem(system string) error {
	if !slices.Contains(t.systems, system) {
		return refuse(RefusalInvalid, "the tracker system %q is not one this server links (%s)", system, strings.Join(t.systems, ", "))
	}
	return nil
}

// cleanText is s valid UTF-8 with every control character dropped but \t
// and, when lines, \n: the one cleaning a snapshot's strings and a read's
// words get.
func cleanText(s string, lines bool) string {
	s = strings.ToValidUTF8(s, "�")
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) && r != '\t' && !(lines && r == '\n') {
			return -1
		}
		return r
	}, s)
}

// cutLines is s cut to at most n bytes at a line's end, "…" said on a last
// line of its own when anything was cut, and whether it was.
func cutLines(s string, n int, more string) (string, bool) {
	if len(s) <= n {
		return s, false
	}
	keep := cutBytes(s, n-len(more))
	if i := strings.LastIndexByte(keep, '\n'); i >= 0 {
		keep = keep[:i+1]
	} else {
		keep = ""
	}
	return keep + more, true
}

// descriptionMore is the last line of a description FromExternal cut.
const descriptionMore = "\n\n… the rest of the description is on GitHub."

// reproMore is the last line of steps to reproduce cut to MaxRepro.
const reproMore = "\n…"

// FromExternal is the one normalisation of an outside item's title and
// body into a ticket's title and description, which an import, a refresh
// and a field given back all use, so a refresh compares its output with
// the stored field and never the system's raw text: the title cleaned of
// every control character and cut to MaxTicketTitle characters with "…";
// the body with \r\n made \n and every other control character but \t
// dropped, cut to MaxTicketDescription bytes at a line's end with a last
// line saying the rest is on GitHub. truncated says the body was cut. A
// title left empty is "(untitled)".
func FromExternal(title, body string) (string, string, bool) {
	t := strings.TrimSpace(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			if r == '\t' || r == '\n' || r == '\r' {
				return ' '
			}
			return -1
		}
		return r
	}, strings.ToValidUTF8(title, "�")))
	if t == "" {
		t = "(untitled)"
	}
	if rs := []rune(t); len(rs) > MaxTicketTitle {
		t = string(rs[:MaxTicketTitle-1]) + "…"
	}
	d := cleanText(strings.ReplaceAll(body, "\r\n", "\n"), true)
	d, cut := cutLines(d, MaxTicketDescription, descriptionMore)
	return t, d, cut
}

// address is an address as the store keeps it (admitAddress): its scheme,
// host and path alone, in one form, and what the address it was made of
// carried beside them, which the store never keeps.
type address struct {
	kept                  string
	query, fragment, user bool
	host                  string
}

// carries reports whether the address carried a query, a fragment or a
// user beside its scheme, host and path.
func (a address) carries() bool { return a.query || a.fragment || a.user }

// admitAddress is the one door an address passes into the store (Program
// item IN1a, review round 1 of #780): a linked item's page, an image's id,
// a check's page, an address in a read's failure or in a refusal's words.
// The address is at most MaxExternalURL bytes of UTF-8 with no control
// character, https, or http on a loopback host (the tests' fake), with a
// host; what the store keeps of it is its scheme, its host lower-cased and
// its path escaped one way (url.URL's EscapedPath), at most MaxExternalURL
// bytes, never its query, its fragment or its user, which a signed URL's
// credential and a person's password ride in. The caller says what becomes
// of an address that carried any (carries): refused, or kept without it.
// The refusal's words name the address's host at most, never the address,
// so a query never reaches a receipt or a log through them.
func admitAddress(what, s string) (address, error) {
	if len(s) > MaxExternalURL {
		return address{}, refuse(RefusalBound, "%s is more than %d bytes", what, MaxExternalURL)
	}
	u, err := url.Parse(s)
	if err != nil || u.Hostname() == "" || u.Opaque != "" || strings.IndexFunc(s, unicode.IsControl) >= 0 || !utf8.ValidString(s) {
		return address{}, refuse(RefusalInvalid, "%s is not an https URL", what)
	}
	host := strings.ToLower(u.Host)
	switch {
	case u.Scheme == "https":
	case u.Scheme == "http" && isLoopback(u.Hostname()):
	default:
		return address{}, refuse(RefusalInvalid, "%s on %s is not https", what, cleanText(host, false))
	}
	a := address{kept: u.Scheme + "://" + host + u.EscapedPath(), query: strings.Contains(s, "?"), fragment: strings.Contains(s, "#"), user: u.User != nil,
		host: host}
	if len(a.kept) > MaxExternalURL {
		return address{}, refuse(RefusalBound, "%s on %s is more than %d bytes", what, host, MaxExternalURL)
	}
	return a, nil
}

// itemPage admits a linked item's page: an address that carries a query, a
// fragment or a user is refused (invalid), since the page is the item's
// whole address and the tracker's own form of it has none; it gives the
// page as the store keeps it.
func itemPage(s string) (string, error) {
	a, err := admitAddress("the item's page", s)
	if err != nil {
		return "", err
	}
	if a.carries() {
		return "", refuse(RefusalInvalid, "the item's page on %s carries a query, a fragment or a user name, which the store never keeps", a.host)
	}
	return a.kept, nil
}

// manifestImageID is an image's id as a manifest keeps it: its address without its
// query or fragment (a signed URL's query is a credential, the plan's
// ImageRef), ok false for an address the store does not admit, one that
// carries a user, or one past MaxImageID bytes, which is left out of the
// manifest.
func manifestImageID(s string) (string, bool) {
	a, err := admitAddress("an image", s)
	if err != nil || a.user || len(a.kept) > MaxImageID {
		return "", false
	}
	return a.kept, true
}

// addressWords is an address as words may name it (a refusal's, a read's
// failure's): what the store keeps of it, or none when it admits none.
func addressWords(s string) (string, bool) {
	a, err := admitAddress("an address", s)
	if err != nil {
		return "", false
	}
	return a.kept, true
}

// isLoopback reports whether host is a loopback address or localhost.
func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// checkRemoteID checks an identity the remote system gave: opaque text of
// 1 to MaxRemoteID bytes of printable ASCII, or "" when what names it is
// optional.
func checkRemoteID(what, id string) error {
	if id == "" {
		return nil
	}
	if len(id) > MaxRemoteID {
		return refuse(RefusalBound, "%s is more than %d bytes", what, MaxRemoteID)
	}
	for i := 0; i < len(id); i++ {
		if id[i] < 0x21 || id[i] > 0x7e {
			return refuse(RefusalInvalid, "%s is not printable ASCII", what)
		}
	}
	if holdsAddress(id) {
		return refuse(RefusalInvalid, "%s holds an address, which an id never does", what)
	}
	return nil
}

// normalSnapshot is a read's snapshot as the store keeps it, made on a
// copy (the read is never changed): its identity's parts and its place
// checked (invalid or bound past their shapes), its page admitted
// (itemPage: one that carries a query, a fragment or a user is invalid);
// every string cleaned (cleanText) and cut to its bound, the labels and
// assignees to their counts (the rest in labelsTotal), the manifest to
// MaxImportImages images in the body's order, each by its id as a
// manifest keeps it (manifestImageID: its address without query or
// fragment; one the store does not admit, with a user, or past MaxImageID
// left out), each once and none done (an import's), the steps to
// reproduce to MaxRepro at a line's end; and the digests the store
// derives: the body's, the steps', and the description's FromExternal
// makes of the body. It also gives that title and description.
func normalSnapshot(read ExternalRead) (sn ExternalSnapshot, title, description string, err error) {
	sn = read.Snapshot
	if sn.V != SnapshotVersion {
		return ExternalSnapshot{}, "", "", refuse(RefusalInvalid, "the snapshot is version %d, not %d", sn.V, SnapshotVersion)
	}
	switch {
	case !SystemPattern.MatchString(sn.System):
		return ExternalSnapshot{}, "", "", refuse(RefusalInvalid, "the system %q is not a tracker system's name", sn.System)
	case len(sn.Host) > MaxExternalHost || !hostPattern.MatchString(sn.Host):
		return ExternalSnapshot{}, "", "", refuse(RefusalInvalid, "the host %q is not a lower-case host name", sn.Host)
	case !nodeIDPattern.MatchString(sn.NodeID):
		return ExternalSnapshot{}, "", "", refuse(RefusalInvalid, "the item's node id is not 1 to %d letters, digits, '_', '=' or '-'", MaxNodeID)
	case sn.Kind != ExternalIssue && sn.Kind != ExternalChange:
		return ExternalSnapshot{}, "", "", refuse(RefusalInvalid, "the item's kind %q is neither issue nor change", sn.Kind)
	case !slices.Contains(externalStates, sn.State):
		return ExternalSnapshot{}, "", "", refuse(RefusalInvalid, "the item's state %q is none of open, closed and merged", sn.State)
	case !slices.Contains(externalReasons, sn.StateReason):
		return ExternalSnapshot{}, "", "", refuse(RefusalInvalid, "the item's state reason %q is none of completed, not_planned and reopened", sn.StateReason)
	case sn.UpdatedAt.IsZero():
		return ExternalSnapshot{}, "", "", refuse(RefusalInvalid, "the read names no time the item was updated")
	case len(ExternalIdentity(sn.System, sn.Host, sn.NodeID)) > MaxExternalID:
		return ExternalSnapshot{}, "", "", refuse(RefusalBound, "the item's identity is more than %d bytes", MaxExternalID)
	case len(sn.Space) > MaxSnapshotSpace || len(sn.ID) > MaxSnapshotItemID:
		return ExternalSnapshot{}, "", "", refuse(RefusalBound, "the item's place is past its bound")
	case holdsAddress(sn.Space) || holdsAddress(sn.ID):
		return ExternalSnapshot{}, "", "", refuse(RefusalInvalid, "the item's place holds an address, which a repository's name or an item's number never does")
	}
	page, err := itemPage(sn.URL)
	if err != nil {
		return ExternalSnapshot{}, "", "", err
	}
	sn.URL = page
	for _, id := range []struct{ what, id string }{{"the item's database id", sn.DatabaseID}, {"the author's id", sn.AuthorID}} {
		if err := checkRemoteID(id.what, id.id); err != nil {
			return ExternalSnapshot{}, "", "", err
		}
	}
	for _, sha := range []struct{ what, v string }{{"the merge commit", sn.MergeSha}, {"the head commit", sn.HeadSha}} {
		if sha.v != "" && !objectIDPattern.MatchString(sha.v) {
			return ExternalSnapshot{}, "", "", refuse(RefusalInvalid, "%s is not a full commit id", sha.what)
		}
	}
	line := func(s string, n int) string { return strings.TrimSpace(cutBytes(cleanText(s, false), n)) }
	sn.Space, sn.ID = cleanText(sn.Space, false), cleanText(sn.ID, false)
	sn.Title = cutBytes(cleanText(sn.Title, true), MaxSnapshotTitle)
	sn.BaseRef = line(sn.BaseRef, MaxSnapshotName)
	sn.Milestone = line(sn.Milestone, MaxSnapshotName)
	sn.Author = line(sn.Author, MaxSnapshotLogin)
	total := max(sn.LabelsTotal, len(sn.Labels))
	labels := []string{}
	for _, l := range sn.Labels {
		if l = line(l, MaxSnapshotLabel); l != "" && len(labels) < MaxSnapshotLabels {
			labels = append(labels, l)
		}
	}
	sn.Labels, sn.LabelsTotal = labels, total
	assignees := []string{}
	for _, a := range sn.Assignees {
		if a = line(a, MaxSnapshotLogin); a != "" && len(assignees) < MaxSnapshotAssignees {
			assignees = append(assignees, a)
		}
	}
	sn.Assignees = assignees
	sn.MergedAt, sn.CreatedAt, sn.UpdatedAt, sn.ClosedAt = sn.MergedAt.UTC(), sn.CreatedAt.UTC(), sn.UpdatedAt.UTC(), sn.ClosedAt.UTC()
	sn.Repro, _ = cutLines(cleanText(strings.ReplaceAll(sn.Repro, "\r\n", "\n"), true), MaxRepro, reproMore)
	sn.ReproSha256 = ""
	if sn.Repro != "" {
		sn.ReproSha256 = sha256Hex(sn.Repro)
	}
	images := []SnapshotImage{}
	for _, im := range sn.Images {
		id, ok := manifestImageID(im.ID)
		if !ok || slices.ContainsFunc(images, func(o SnapshotImage) bool { return o.ID == id }) {
			continue
		}
		if len(images) == MaxImportImages {
			break
		}
		images = append(images, SnapshotImage{ID: id})
	}
	sn.Images = images
	var cut bool
	title, description, cut = FromExternal(sn.Title, read.Body)
	sn.BodySha256, sn.BodyBytes = sha256Hex(read.Body), len(read.Body)
	sn.BodyTruncated = sn.BodyTruncated || cut
	sn.DescriptionSha256 = sha256Hex(description)
	for _, v := range []string{read.IssueETag, read.PullETag} {
		if !validator(v) {
			return ExternalSnapshot{}, "", "", refuse(RefusalInvalid, "a read's validator is not at most %d bytes of printable ASCII", MaxValidator)
		}
	}
	return sn, title, description, nil
}

// keepImages is the manifest a read of an item already linked makes:
// prior's entries as they are, done or pending, in their order (an entry
// whose image the body no longer has stays, and is fetched no more), then
// the body's new images, by id, without an attachment, while the manifest
// holds fewer than MaxImportImages. So a manifest only grows and its
// entries only become done, on one replica as in a merge of two
// (ReconcileCounterpart).
func keepImages(next, prior []SnapshotImage) []SnapshotImage {
	out := slices.Clone(prior)
	for _, im := range next {
		if len(out) == MaxImportImages {
			break
		}
		if !slices.ContainsFunc(out, func(p SnapshotImage) bool { return p.ID == im.ID }) {
			out = append(out, SnapshotImage{ID: im.ID})
		}
	}
	if out == nil {
		out = []SnapshotImage{}
	}
	return out
}

// encodeSnapshot is a snapshot as its column holds it: bound past
// MaxExternalSnapshot.
func encodeSnapshot(sn ExternalSnapshot) (string, error) {
	if sn.Labels == nil {
		sn.Labels = []string{}
	}
	if sn.Assignees == nil {
		sn.Assignees = []string{}
	}
	if sn.Images == nil {
		sn.Images = []SnapshotImage{}
	}
	v, err := encodeValue(sn)
	if err != nil {
		return "", err
	}
	if len(v) > MaxExternalSnapshot {
		return "", refuse(RefusalBound, "the snapshot is %d bytes, more than %d", len(v), MaxExternalSnapshot)
	}
	return v, nil
}

// ParseSnapshot reads a snapshot column; ok is false for an empty or
// unreadable one.
func ParseSnapshot(raw string) (ExternalSnapshot, bool) {
	var sn ExternalSnapshot
	if raw == "" || json.Unmarshal([]byte(raw), &sn) != nil {
		return ExternalSnapshot{}, false
	}
	return sn, true
}

// ImportArgs is ticket.import's content, its fingerprint: the client's
// request alone (its project by key, the item's URL as given, and the
// ticket's own fields, which are Dispatch's whatever the item says),
// never the read, which differs between two tries of one request.
type ImportArgs struct {
	Project   string   `json:"project"`
	URL       string   `json:"url"`
	Status    string   `json:"status,omitempty"`
	Priority  int      `json:"priority,omitempty"`
	Horizon   string   `json:"horizon,omitempty"`
	Owner     string   `json:"owner,omitempty"`
	Assignees []string `json:"assignees,omitempty"`
	Labels    []string `json:"labels,omitempty"`
	Milestone string   `json:"milestone,omitempty"`
}

// IntentTicketImport is an import's intent, whose resend the API replays
// before it reads the item (Replay).
const IntentTicketImport = "ticket.import"

// The intents of a ticket's linked issue's other operations.
const (
	IntentLinkExternal   = "ticket.link_external"
	IntentUnlinkExternal = "ticket.unlink_external"
	IntentRefresh        = "ticket.refresh"
	IntentAuthority      = "ticket.authority"
	IntentImportImage    = "ticket.import_image"
)

// Projection is what an import of read keeps (normalSnapshot): the
// snapshot, its manifest among it, and the ticket's title and
// description. A preview shows it.
func Projection(read ExternalRead) (ExternalSnapshot, string, string, error) {
	return normalSnapshot(read)
}

// LinkedTicket is the key of the live ticket whose linked issue is the
// item system, host and nodeID name, looked up as linkedTo refuses a
// second one; "" for none.
func (s *Store) LinkedTicket(ctx context.Context, system, host, nodeID string) (key string, err error) {
	err = s.read(ctx, func(q queryer) error {
		err := q.QueryRowContext(ctx, "SELECT key FROM tickets WHERE external_id = ? AND deleted_at = '' ORDER BY number LIMIT 1", ExternalIdentity(system, host, nodeID)).Scan(&key)
		if isNoRows(err) {
			return nil
		}
		return err
	})
	return key, err
}

// LinkedSince reports whether the linked issue of the ticket with the key
// was set or emptied (a change of its external_id) after op's own last
// change: the ticket unlinked, or linked to an item, since op.
func (s *Store) LinkedSince(ctx context.Context, op Op, key string) (since bool, err error) {
	err = s.read(ctx, func(q queryer) error {
		return q.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM changes c JOIN tickets t ON t.id = c.ticket_id WHERE t.key = ? AND c.field = 'external_id'
			AND c.seq > (SELECT COALESCE(MAX(seq), 0) FROM changes WHERE actor = ? AND op_id = ?))`, key, op.Actor, op.ID).Scan(&since)
	})
	return since, err
}

// linkedTo refuses an item another live ticket than except has as its
// linked issue (external_linked, the receipt's Key naming it).
func (t *tx) linkedTo(identity, except string) error {
	var key string
	err := t.tx.QueryRowContext(t.ctx, "SELECT key FROM tickets WHERE external_id = ? AND deleted_at = '' AND id != ? ORDER BY number LIMIT 1", identity, except).Scan(&key)
	switch {
	case isNoRows(err):
		return nil
	case err != nil:
		return err
	}
	return &Refusal{Code: RefusalExternalLinked, Key: key, Words: "the item is " + key + "'s linked issue already"}
}

// writeTicketRead writes the ticket's ticket_reads row for a read of the
// identity: its system's time, now, who read, the validators, no error.
func (t *tx) writeTicketRead(ticketID, identity string, sn ExternalSnapshot, read ExternalRead) error {
	_, err := t.tx.ExecContext(t.ctx, `INSERT INTO ticket_reads (ticket_id, external_id, updated_at, read_at, read_by, issue_etag, pull_etag, error, error_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, '', '') ON CONFLICT (ticket_id) DO UPDATE SET external_id = excluded.external_id, updated_at = excluded.updated_at,
		read_at = excluded.read_at, read_by = excluded.read_by, issue_etag = excluded.issue_etag, pull_etag = excluded.pull_etag, error = '', error_at = ''`,
		ticketID, identity, stamp(sn.UpdatedAt), stamp(t.now), t.op.author(), read.IssueETag, read.PullETag)
	return err
}

// staleRead refuses a read of the identity older, by its system's clock,
// than the one the ticket's ticket_reads row holds for it (stale_read): a
// newer read is kept. A read with no time (a 304) is never older.
func staleRead(ctx context.Context, q queryer, ticketID, identity string, updated time.Time) error {
	var held, at string
	err := q.QueryRowContext(ctx, "SELECT external_id, updated_at FROM ticket_reads WHERE ticket_id = ?", ticketID).Scan(&held, &at)
	switch {
	case isNoRows(err):
		return nil
	case err != nil:
		return err
	}
	if held == identity && at != "" && !updated.IsZero() && updated.Before(unstamp(at)) {
		return refuse(RefusalStaleRead, "the item was read as updated at %s, which is older than the read kept (%s)", stamp(updated), at)
	}
	return nil
}

// linkedRead is the snapshot a read of the ticket's linked item makes,
// checked against the item the ticket holds (invalid when the read is of
// another: the API checked it, this is the store's own guard) and against
// the read kept (stale_read), its manifest keeping each done entry by
// image id, with the title and description FromExternal makes of it.
func (t *tx) linkedRead(tk Ticket, read ExternalRead) (ExternalSnapshot, string, string, error) {
	sn, title, description, err := normalSnapshot(read)
	if err != nil {
		return ExternalSnapshot{}, "", "", err
	}
	if sn.Identity() != tk.ExternalID {
		return ExternalSnapshot{}, "", "", refuse(RefusalInvalid, "the read is of %s, not %s's linked issue", sn.Identity(), tk.Key)
	}
	if err := staleRead(t.ctx, t.tx, tk.ID, tk.ExternalID, sn.UpdatedAt); err != nil {
		return ExternalSnapshot{}, "", "", err
	}
	if prior, ok := ParseSnapshot(string(tk.ExternalSnapshot)); ok {
		sn.Images = keepImages(sn.Images, prior.Images)
	}
	return sn, title, description, nil
}

// ImportTicket makes a ticket of the outside item a read gives
// (ticket.import) in the project args name: its source the item's system,
// which must be one the store was opened with (invalid otherwise); its
// title and description FromExternal's of the item's (observe rows, the
// system's text); its linked issue (external_id, external_url,
// external_snapshot with the image manifest, none of whose images has an
// attachment yet) and its field owners, both the system's; its status,
// priority, horizon, owner, assignees, labels and milestone Dispatch's
// defaults or what args names (the item's labels and assignees are in the
// snapshot, never mapped); its ticket_reads row written with the read's
// validators. One operation with one changes row per field, as every
// create writes. Refused external_linked when a live ticket holds the
// item already, archived for an archived project; every other rule is
// CreateTicket's. The read is an argument apart from args: the fingerprint
// is args alone, so a resend is a replay, and the create row keeps it, so a
// resend whose receipt is pruned is answered with the ticket's id and key
// (prunedAnswer) and never imports the item twice. The receipt carries
// the ticket's id and key.
func (s *Store) ImportTicket(ctx context.Context, op Op, args ImportArgs, read ExternalRead) (Receipt, error) {
	return s.Apply(ctx, op, IntentTicketImport, args, func(t *tx) error {
		sn, title, description, err := normalSnapshot(read)
		if err != nil {
			return err
		}
		if err := t.checkSystem(sn.System); err != nil {
			return err
		}
		identity := sn.Identity()
		if err := t.linkedTo(identity, ""); err != nil {
			return err
		}
		snapshot, err := encodeSnapshot(sn)
		if err != nil {
			return err
		}
		authority := Authority{Of: authorityKey(identity), Title: sn.System, Description: sn.System}
		n := NewTicket{Project: args.Project, Title: title, Description: description, Status: args.Status, Priority: args.Priority,
			Horizon: args.Horizon, Owner: args.Owner, Assignees: args.Assignees, Labels: args.Labels, Milestone: args.Milestone}
		extra := []fieldValue{{"source", sn.System}, {"external_id", identity}, {"external_url", sn.URL}, {"external_snapshot", snapshot},
			{"authority", authority.encode()}}
		id, err := t.createTicket(n, extra, map[string]string{"title": OpObserve, "description": OpObserve}, t.fp)
		if err != nil {
			return err
		}
		return t.writeTicketRead(id, identity, sn, read)
	})
}

// LinkArgs is ticket.link_external's content: the ticket's key and the
// item's URL as the client gave it.
type LinkArgs struct {
	Key string `json:"key"`
	URL string `json:"url"`
}

// LinkExternal sets the live ticket's linked issue to the item a read
// gives (ticket.link_external) without changing a field: external_id,
// external_url, external_snapshot and authority, one set row each, the
// owners every field Dispatch's (the ticket was written here) as an
// authority that names the item and no field ({"of": …}, so a relink of an
// item is an owner change of it a merge orders), its ticket_reads row
// written. The system must be one the store was opened with
// (invalid); a ticket that has a linked issue is invalid (unlink it
// first); external_linked when another live ticket holds the item. An
// expected revision is the ticket's; a deleted ticket is not_found; an
// archived project refuses it (archived).
func (s *Store) LinkExternal(ctx context.Context, op Op, args LinkArgs, read ExternalRead) (Receipt, error) {
	return s.Apply(ctx, op, IntentLinkExternal, args, func(t *tx) error {
		tk, _, err := t.ticket(args.Key, false)
		if err != nil {
			return err
		}
		if tk.ExternalID != "" {
			return refuse(RefusalInvalid, "%s is linked to %s: unlink it first", tk.Key, tk.ExternalURL)
		}
		sn, _, _, err := normalSnapshot(read)
		if err != nil {
			return err
		}
		if err := t.checkSystem(sn.System); err != nil {
			return err
		}
		identity := sn.Identity()
		if err := t.linkedTo(identity, tk.ID); err != nil {
			return err
		}
		snapshot, err := encodeSnapshot(sn)
		if err != nil {
			return err
		}
		cols := []string{"external_id", "external_url", "external_snapshot", "authority"}
		before, err := t.scalars("tickets", tk.ID, cols)
		if err != nil {
			return err
		}
		// The owners every field Dispatch's, for this item: {"of": …} with no
		// field named, a row although an unlinked ticket's authority is ''
		// already, so a merge reads the link as the last owner change of the
		// item and an owner change another replica made of it before the
		// relink hands nothing back to the system ("The counterpart group",
		// rule 4).
		owners := Authority{Of: authorityKey(identity)}.encode()
		after := []fieldValue{{"external_id", identity}, {"external_url", sn.URL}, {"external_snapshot", snapshot}, {"authority", owners}}
		if err := t.setScalars("tickets", ticketBase(tk), before, after); err != nil {
			return err
		}
		return t.writeTicketRead(tk.ID, identity, sn, read)
	})
}

// ticketBase is a change row of the ticket's own, its field and op unset.
func ticketBase(tk Ticket) ChangeRow {
	return ChangeRow{Entity: EntityTicket, EntityID: tk.ID, TicketID: tk.ID, ProjectID: tk.ProjectID}
}

// adopt records the field made Dispatch's keeping the text it holds: one
// adopt row, its value the text and its prior the system that owned it,
// written although the text does not change, so the record says which
// text Dispatch took over.
func (t *tx) adopt(tk Ticket, field, text, system string) error {
	row := ticketBase(tk)
	row.Field, row.Op, row.Value, row.Prior = field, OpAdopt, text, system
	return t.record(row)
}

// observe records the field set to what a read of the linked item made
// it: its column written and one observe row, its prior the text it
// replaces, written although the text does not change.
func (t *tx) observe(tk Ticket, field, prior, text string) error {
	if _, err := t.tx.ExecContext(t.ctx, "UPDATE tickets SET "+field+" = ? WHERE id = ?", text, tk.ID); err != nil {
		return err
	}
	row := ticketBase(tk)
	row.Field, row.Op, row.Value, row.Prior = field, OpObserve, text, prior
	return t.record(row)
}

// textOf is the ticket's title or description.
func textOf(tk Ticket, field string) string {
	if field == FieldTitle {
		return tk.Title
	}
	return tk.Description
}

// UnlinkExternal empties the live ticket's linked issue
// (ticket.unlink_external): each field its system owned made Dispatch's
// with the text it holds (one adopt row each), then external_id,
// external_url, external_snapshot and authority emptied, one set row each,
// and its ticket_reads row dropped. Its source stays what it was, where the
// ticket was made; the snapshot's last value stays in the history. A
// ticket with no linked issue is invalid. An expected revision is the
// ticket's; a deleted ticket is not_found; an archived project refuses it
// (archived).
func (s *Store) UnlinkExternal(ctx context.Context, op Op, key string) (Receipt, error) {
	args := struct {
		Key string `json:"key"`
	}{key}
	return s.Apply(ctx, op, IntentUnlinkExternal, args, func(t *tx) error {
		tk, _, err := t.ticket(key, false)
		if err != nil {
			return err
		}
		if tk.ExternalID == "" {
			return refuse(RefusalInvalid, "%s has no linked issue", tk.Key)
		}
		a := ParseAuthority(string(tk.Authority))
		for _, field := range []string{FieldTitle, FieldDescription} {
			if system := a.owner(field); system != "" {
				if err := t.adopt(tk, field, textOf(tk, field), system); err != nil {
					return err
				}
			}
		}
		cols := []string{"external_id", "external_url", "external_snapshot", "authority"}
		before, err := t.scalars("tickets", tk.ID, cols)
		if err != nil {
			return err
		}
		after := []fieldValue{{"external_id", ""}, {"external_url", ""}, {"external_snapshot", ""}, {"authority", ""}}
		if err := t.setScalars("tickets", ticketBase(tk), before, after); err != nil {
			return err
		}
		_, err = t.tx.ExecContext(t.ctx, "DELETE FROM ticket_reads WHERE ticket_id = ?", tk.ID)
		return err
	})
}

// RefreshArgs is ticket.refresh's content: the ticket's key alone.
type RefreshArgs struct {
	Key string `json:"key"`
}

// RefreshExternal writes what a read of the live ticket's linked item
// found new (ticket.refresh): the read must be of the item the ticket
// holds (invalid otherwise) and not older by the system's clock than the
// read kept (stale_read, nothing written); external_snapshot set when its
// content changed (its manifest keeping each done entry by image id and
// adding the body's new images without one), external_url when the item
// moved, each field the owners give the system set to FromExternal's
// output where that differs from the stored field (one observe row each:
// a CRLF body is the same description), and ticket_reads in the same
// transaction. A field Dispatch owns is never written. The ticket's
// system must be one the store was opened with (invalid otherwise), as on
// every local write that makes or keeps a system an owner. The API runs it
// only when RefreshNeeded says the snapshot or a system-owned field would
// change; a read that finds nothing new is RecordTicketRead's alone. A
// change of a system-owned field moves the ticket's intent digest, so a
// revision approved before goes stale. A deleted ticket is not_found; an
// archived project refuses it (archived); a ticket with no linked issue
// is invalid.
func (s *Store) RefreshExternal(ctx context.Context, op Op, key string, read ExternalRead) (Receipt, error) {
	return s.Apply(ctx, op, IntentRefresh, RefreshArgs{Key: key}, func(t *tx) error {
		tk, _, err := t.ticket(key, false)
		if err != nil {
			return err
		}
		if tk.ExternalID == "" {
			return refuse(RefusalInvalid, "%s has no linked issue", tk.Key)
		}
		// A refresh keeps the system the writer of the fields it owns.
		if err := t.checkSystem(systemOf(tk.ExternalID)); err != nil {
			return err
		}
		sn, title, description, err := t.linkedRead(tk, read)
		if err != nil {
			return err
		}
		if err := t.writeRead(tk, sn, ParseAuthority(string(tk.Authority)), title, description); err != nil {
			return err
		}
		return t.writeTicketRead(tk.ID, tk.ExternalID, sn, read)
	})
}

// writeRead writes what a read of the ticket's linked item makes of it
// under the owners a: the snapshot and the URL where they changed, and
// each field a gives the system as FromExternal made it, observe rows where
// it differs.
func (t *tx) writeRead(tk Ticket, sn ExternalSnapshot, a Authority, title, description string) error {
	snapshot, err := encodeSnapshot(sn)
	if err != nil {
		return err
	}
	before, err := t.scalars("tickets", tk.ID, []string{"external_url", "external_snapshot"})
	if err != nil {
		return err
	}
	if err := t.setScalars("tickets", ticketBase(tk), before, []fieldValue{{"external_url", sn.URL}, {"external_snapshot", snapshot}}); err != nil {
		return err
	}
	for _, f := range []struct{ field, text string }{{FieldTitle, title}, {FieldDescription, description}} {
		if a.owner(f.field) != "" && textOf(tk, f.field) != f.text {
			if err := t.observe(tk, f.field, textOf(tk, f.field), f.text); err != nil {
				return err
			}
		}
	}
	return nil
}

// RefreshNeeded reports whether a read of the ticket's linked item would
// change the ticket (RefreshExternal), the ticket as it was read (Ticket):
// its snapshot or URL, or a field its system owns. false is a read that
// finds nothing new, which RecordTicketRead records alone. An error is the
// read's shape, or a read of another item than the ticket's.
func RefreshNeeded(tk Ticket, read ExternalRead) (bool, error) {
	sn, title, description, err := normalSnapshot(read)
	if err != nil {
		return false, err
	}
	if sn.Identity() != tk.ExternalID {
		return false, refuse(RefusalInvalid, "the read is of %s, not %s's linked issue", sn.Identity(), tk.Key)
	}
	if prior, ok := ParseSnapshot(string(tk.ExternalSnapshot)); ok {
		sn.Images = keepImages(sn.Images, prior.Images)
	}
	snapshot, err := encodeSnapshot(sn)
	if err != nil {
		return false, err
	}
	a := ParseAuthority(string(tk.Authority))
	return snapshot != string(tk.ExternalSnapshot) || sn.URL != tk.ExternalURL ||
		(a.Title != "" && title != tk.Title) || (a.Description != "" && description != tk.Description), nil
}

// AuthorityArgs is ticket.authority's content: the ticket's key, the field
// and its new owner.
type AuthorityArgs struct {
	Key   string `json:"key"`
	Field string `json:"field"`
	Owner string `json:"owner"`
}

// SetAuthority moves a field of the live ticket's between Dispatch and the
// linked item's system (ticket.authority): the field title or description,
// the owner OwnerDispatch or the system the ticket's linked issue is of,
// anything else invalid, as is a ticket with no linked issue or a field
// its owner holds already. Giving a field to the system asks the
// registry first (invalid for a system the store was not opened with);
// taking one for Dispatch is always allowed. To Dispatch it sets the owners and writes one
// adopt row of the field with the text it holds, although the text does
// not change. To the system it takes the read the API made a moment
// before (invalid with none), the read checked as a refresh's (invalid for
// another item, stale_read for an older one), and in the one operation
// writes the snapshot and URL where they changed, the owners, and the
// field set to FromExternal's output of that read (an observe row), so
// giving a field back works whatever the system changed while Dispatch
// owned it; its ticket_reads row with it. An expected revision is the
// ticket's; a deleted ticket is not_found; an archived project refuses it
// (archived).
func (s *Store) SetAuthority(ctx context.Context, op Op, args AuthorityArgs, read *ExternalRead) (Receipt, error) {
	return s.Apply(ctx, op, IntentAuthority, args, func(t *tx) error {
		tk, _, err := t.ticket(args.Key, false)
		if err != nil {
			return err
		}
		if args.Field != FieldTitle && args.Field != FieldDescription {
			return refuse(RefusalInvalid, "the field %q is neither title nor description", args.Field)
		}
		if tk.ExternalID == "" {
			return refuse(RefusalInvalid, "%s has no linked issue", tk.Key)
		}
		system := systemOf(tk.ExternalID)
		if args.Owner != OwnerDispatch && args.Owner != system {
			return refuse(RefusalInvalid, "the owner %q is neither dispatch nor %s, the system %s's linked issue is of", args.Owner, system, tk.Key)
		}
		a := ParseAuthority(string(tk.Authority))
		owner := a.owner(args.Field)
		next := a
		next.Of = authorityKey(tk.ExternalID)
		before, err := t.scalars("tickets", tk.ID, []string{"authority"})
		if err != nil {
			return err
		}
		if args.Owner == OwnerDispatch {
			if owner == "" {
				return refuse(RefusalInvalid, "the %s of %s is Dispatch's already", args.Field, tk.Key)
			}
			next = next.with(args.Field, "")
			if err := t.setScalars("tickets", ticketBase(tk), before, []fieldValue{{"authority", next.encode()}}); err != nil {
				return err
			}
			return t.adopt(tk, args.Field, textOf(tk, args.Field), owner)
		}
		if err := t.checkSystem(system); err != nil {
			return err
		}
		if owner != "" {
			return refuse(RefusalInvalid, "the %s of %s is %s's already", args.Field, tk.Key, owner)
		}
		if read == nil {
			return refuse(RefusalInvalid, "giving the %s back to %s takes a read of the item", args.Field, system)
		}
		sn, title, description, err := t.linkedRead(tk, *read)
		if err != nil {
			return err
		}
		next = next.with(args.Field, system)
		if err := t.setScalars("tickets", ticketBase(tk), before, []fieldValue{{"authority", next.encode()}}); err != nil {
			return err
		}
		// The snapshot and the URL; the field given back is written below,
		// always, and the other as the owners it had keep it.
		if err := t.writeRead(tk, sn, a, title, description); err != nil {
			return err
		}
		text := title
		if args.Field == FieldDescription {
			text = description
		}
		if err := t.observe(tk, args.Field, textOf(tk, args.Field), text); err != nil {
			return err
		}
		return t.writeTicketRead(tk.ID, tk.ExternalID, sn, *read)
	})
}

// ownedRefusal refuses an edit that sets a field the ticket's owners give
// its linked item's system (external_owned).
func ownedRefusal(tk Ticket, field string) error {
	if system := ParseAuthority(string(tk.Authority)).owner(field); system != "" {
		return refuse(RefusalExternalOwned, "the %s of %s is %s's: edit it on %s and refresh, or take it over", field, tk.Key, system, system)
	}
	return nil
}

// ImageUpload is one image of an import's manifest as ticket.import_image
// takes it: its content (Body, at most MaxAttachmentSize) with the name and
// content type its attachment is made with, or, with no Body, why it is
// skipped (SkippedType, SkippedSize), recorded so that no later read
// fetches it again.
type ImageUpload struct {
	Name        string
	ContentType string
	Body        io.Reader
	Skipped     string
}

// ImportImage makes one image of the live ticket's import manifest done
// (ticket.import_image), in one transaction: with a body, DS1's attachment
// add (the file published first by its SHA-256, as AddAttachment publishes
// it, then the attachment's row and its add row) and the manifest's entry
// for the image set to that attachment and its SHA-256 (one set row of the
// snapshot), committed together, so no attachment of an import exists that
// its manifest does not name and no entry names an attachment that was not
// made; with none, the entry's skip recorded (one set row). An image not in
// the manifest, or whose entry is done already, is invalid; the ticket's
// attachments' bound is DS1's; a refusal keeps neither the row nor the
// entry. The image is named by its id as the manifest keeps it
// (manifestImageID): an address with a query or a fragment names the
// entry without them, so the API may name it by the URL it fetched, and
// the query is kept nowhere. The fingerprint is the key, that id and what
// it carries (the name, type, size and SHA-256 of its content, or its
// skip), so the API's derived op id for it (DerivedOpID) replays one
// input.
func (s *Store) ImportImage(ctx context.Context, op Op, key, imageID string, img ImageUpload) (Receipt, error) {
	// The image by its id as the manifest keeps it: the API may name it by
	// the address it fetched, whose query is left behind here.
	id, admitted := manifestImageID(imageID)
	if img.Body != nil && !slices.Contains(ImageTypes, img.ContentType) {
		return Receipt{}, fmt.Errorf("dispatch: an image's attachment is one of the types %v", ImageTypes)
	}
	mark := func(t *tx, tk Ticket, entry SnapshotImage) error {
		if !admitted {
			return refuse(RefusalInvalid, "the image is not an address an import manifest holds")
		}
		sn, ok := ParseSnapshot(string(tk.ExternalSnapshot))
		i := -1
		if ok {
			i = slices.IndexFunc(sn.Images, func(im SnapshotImage) bool { return im.ID == id })
		}
		switch {
		case i < 0:
			return refuse(RefusalInvalid, "the image %q is not in %s's import manifest", id, tk.Key)
		case sn.Images[i].done():
			return refuse(RefusalInvalid, "the image %q of %s is done already", id, tk.Key)
		}
		sn.Images[i] = entry
		snapshot, err := encodeSnapshot(sn)
		if err != nil {
			return err
		}
		return t.setScalars("tickets", ticketBase(tk), []fieldValue{{"external_snapshot", string(tk.ExternalSnapshot)}}, []fieldValue{{"external_snapshot", snapshot}})
	}
	if img.Body == nil {
		if img.Skipped != SkippedType && img.Skipped != SkippedSize {
			return Receipt{}, fmt.Errorf("dispatch: an image is uploaded or skipped for its type or its size")
		}
		args := struct {
			Key     string `json:"key"`
			Image   string `json:"image"`
			Skipped string `json:"skipped"`
		}{key, id, img.Skipped}
		return s.Apply(ctx, op, IntentImportImage, args, func(t *tx) error {
			tk, _, err := t.ticket(key, false)
			if err != nil {
				return err
			}
			return mark(t, tk, SnapshotImage{ID: id, Skipped: img.Skipped})
		})
	}
	args := func(size int64, sum string) any {
		return struct {
			Key         string `json:"key"`
			Image       string `json:"image"`
			Name        string `json:"name"`
			ContentType string `json:"contentType"`
			Size        int64  `json:"size"`
			SHA256      string `json:"sha256"`
		}{key, id, img.Name, img.ContentType, size, sum}
	}
	rc, _, err := s.addAttachment(ctx, op, IntentImportImage, key, img.Name, img.ContentType, img.Body, args, func(t *tx, tk Ticket, a Attachment) error {
		return mark(t, tk, SnapshotImage{ID: id, AttachmentID: a.ID, SHA256: a.SHA256})
	})
	return rc, err
}

// TicketRead is a ticket's ticket_reads row: the identity its linked item
// was read for, the item's update time by its system's clock, when it was
// read and by whom, the read's validators, and the last read's failure in
// words, with when.
type TicketRead struct {
	ExternalID string    `json:"externalId"`
	UpdatedAt  time.Time `json:"updatedAt,omitzero"`
	ReadAt     time.Time `json:"readAt,omitzero"`
	ReadBy     string    `json:"readBy,omitempty"`
	IssueETag  string    `json:"issueEtag,omitempty"`
	PullETag   string    `json:"pullEtag,omitempty"`
	Error      string    `json:"error,omitempty"`
	ErrorAt    time.Time `json:"errorAt,omitzero"`
}

// ticketReadOf reads the ticket's ticket_reads row; nil for none, and for
// a database before version 4, read as it was found.
func ticketReadOf(ctx context.Context, q queryer, ticketID string) (*TicketRead, error) {
	if schemaOf(q) < 4 {
		return nil, nil
	}
	var r TicketRead
	var updated, read, errAt string
	err := q.QueryRowContext(ctx, "SELECT external_id, updated_at, read_at, read_by, issue_etag, pull_etag, error, error_at FROM ticket_reads WHERE ticket_id = ?",
		ticketID).Scan(&r.ExternalID, &updated, &read, &r.ReadBy, &r.IssueETag, &r.PullETag, &r.Error, &errAt)
	switch {
	case isNoRows(err):
		return nil, nil
	case err != nil:
		return nil, err
	}
	r.UpdatedAt, r.ReadAt, r.ErrorAt = unstamp(updated), unstamp(read), unstamp(errAt)
	return &r, nil
}

var (
	// wordsAddress is an address in words: a scheme and what follows it up
	// to a space.
	wordsAddress = regexp.MustCompile(`[A-Za-z][A-Za-z0-9+.-]*://\S*`)
	// wordsPathQuery is a path's query in words (an API path the adapter
	// named): from the '?' after a path to a space. A '#' after a path is
	// left, since "owner/repo#612" names an item.
	wordsPathQuery = regexp.MustCompile(`(/[^\s?]*)\?\S*`)
)

// addressFree is the one door free text about a tracker passes into the
// store (Program item IN1a, review round 2 of #780): a read's failure, a
// reason its checks are incomplete, a check's name and its app's, a
// reader's name, and a refusal's words, every receipt's among them
// (refuse, words). Each address in the text is what the store keeps of an
// address (addressWords: its scheme, host and path; "an address" for one
// it does not admit) and each path's query is left out, so no such text
// keeps an address's query, fragment or user, which a signed URL's
// credential and a person's password ride in. A text the store holds as
// opaque (an id, a validator, a check's state, an item's place) is
// refused instead where this would change it (holdsAddress); the item's
// own words (its title, body, steps, labels, logins, milestone and
// branch) are the person's, kept as the tracker has them, as a ticket's
// description keeps a person's link.
func addressFree(s string) string {
	s = wordsAddress.ReplaceAllStringFunc(s, func(a string) string {
		if kept, ok := addressWords(a); ok {
			return kept
		}
		return "an address"
	})
	return wordsPathQuery.ReplaceAllString(s, "$1")
}

// holdsAddress reports whether the text holds what addressFree changes:
// an address, or a path's query.
func holdsAddress(s string) bool { return addressFree(s) != s }

// validator reports whether v is a read's validator as the store keeps
// one: printable ASCII, at most MaxValidator bytes, holding no address.
func validator(v string) bool { return validatorPattern.MatchString(v) && !holdsAddress(v) }

// readWords is a read's failure as a row keeps it: cleaned of control
// characters, made address-free (addressFree), so the words never keep a
// signed URL's credential, and cut to MaxReadError characters.
func readWords(s string) string {
	s = addressFree(strings.TrimSpace(cleanText(s, false)))
	if rs := []rune(s); len(rs) > MaxReadError {
		s = string(rs[:MaxReadError-1]) + "…"
	}
	return s
}

// RecordTicketRead records a read of the ticket's linked item that found
// nothing new (a 304, or an unchanged answer) in its ticket_reads row, in a
// transaction of its own outside Apply (no changes row, no receipt): r's
// item update time by its system's clock (zero for a 304, which keeps the
// time held), who read and the read's validators; the read's time is the
// store's. The ticket must be live (a *Refusal not_found otherwise) and
// hold the identity still (a *Refusal invalid otherwise, the read
// dropped), and a read older by the system's clock
// than the one kept is a *Refusal stale_read, nothing written. Then
// OnRead's listeners are told, never OnChange's. ErrNotFound when there is
// no such ticket.
func (s *Store) RecordTicketRead(ctx context.Context, key, identity string, r TicketRead) error {
	if !validator(r.IssueETag) || !validator(r.PullETag) {
		return refuse(RefusalInvalid, "a read's validator is not at most %d bytes of printable ASCII", MaxValidator)
	}
	by := []rune(strings.TrimSpace(addressFree(cleanText(r.ReadBy, false))))
	by = by[:min(len(by), MaxByName)]
	return s.recordTicketRead(ctx, key, identity, func(dbtx *sql.Tx, tk Ticket, now time.Time) error {
		if err := staleRead(ctx, dbtx, tk.ID, identity, r.UpdatedAt); err != nil {
			return err
		}
		_, err := dbtx.ExecContext(ctx, `INSERT INTO ticket_reads (ticket_id, external_id, updated_at, read_at, read_by, issue_etag, pull_etag, error, error_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, '', '') ON CONFLICT (ticket_id) DO UPDATE SET external_id = excluded.external_id,
			updated_at = CASE WHEN excluded.updated_at = '' THEN ticket_reads.updated_at ELSE excluded.updated_at END,
			read_at = excluded.read_at, read_by = excluded.read_by, issue_etag = excluded.issue_etag, pull_etag = excluded.pull_etag, error = '', error_at = ''`,
			tk.ID, identity, stamp(r.UpdatedAt), stamp(now), string(by), r.IssueETag, r.PullETag)
		return err
	})
}

// RecordTicketReadError records a read of the ticket's linked item that
// failed, its words in its ticket_reads row (cut to MaxReadError
// characters), the read kept otherwise as it was, as RecordTicketRead
// records a read.
func (s *Store) RecordTicketReadError(ctx context.Context, key, identity, words string) error {
	return s.recordTicketRead(ctx, key, identity, func(dbtx *sql.Tx, tk Ticket, now time.Time) error {
		_, err := dbtx.ExecContext(ctx, `INSERT INTO ticket_reads (ticket_id, external_id, error, error_at) VALUES (?, ?, ?, ?)
			ON CONFLICT (ticket_id) DO UPDATE SET error = excluded.error, error_at = excluded.error_at`, tk.ID, identity, readWords(words), stamp(now))
		return err
	})
}

// recordTicketRead runs write on the ticket with the key, which holds the
// identity, in a transaction of its own under the store's lock, then tells
// OnRead's listeners.
func (s *Store) recordTicketRead(ctx context.Context, key, identity string, write func(*sql.Tx, Ticket, time.Time) error) error {
	var notice ReadNotice
	err := s.derived(ctx, func(dbtx *sql.Tx, now time.Time) error {
		tk, err := ticketByKey(ctx, dbtx, key)
		switch {
		case isNoRows(err):
			return ErrNotFound
		case err != nil:
			return err
		case !tk.DeletedAt.IsZero():
			return refuse(RefusalNotFound, "%s is deleted: the read is dropped", tk.Key)
		case tk.ExternalID != identity || identity == "":
			return refuse(RefusalInvalid, "%s's linked issue is not the item read: the read is dropped", tk.Key)
		}
		if err := write(dbtx, tk, now); err != nil {
			return err
		}
		p, err := projectByID(ctx, dbtx, tk.ProjectID)
		if err != nil {
			return err
		}
		notice = ReadNotice{Ticket: tk.ID, Project: p.Key, ReadAt: now}
		return nil
	})
	if err != nil {
		return err
	}
	s.told(notice)
	return nil
}

// derived runs fn in a write transaction of its own under the store's
// lock, for a derived table's write (a read's), which keeps no receipt and
// no changes row; fn's time is the transaction's. A Refusal fn returns is
// the caller's answer, nothing written.
func (s *Store) derived(ctx context.Context, fn func(*sql.Tx, time.Time) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case s.closed:
		return ErrClosed
	case s.readOnly || s.db == nil:
		return fmt.Errorf("%w: %s", ErrReadOnly, s.why)
	}
	dbtx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return cancelledAs(ctx, err)
	}
	defer dbtx.Rollback()
	if err := fn(dbtx, time.Now().UTC()); err != nil {
		var ref *Refusal
		if errors.As(err, &ref) || errors.Is(err, ErrNotFound) {
			return err
		}
		return cancelledAs(ctx, err)
	}
	return cancelledAs(ctx, dbtx.Commit())
}

// DerivedOpID is the id of an operation the server makes on behalf of
// another (an import's images, a publication's settles and close): "~" and
// the first 31 hex digits of the SHA-256 of the parent's id and each part,
// each written with its length before it, so no two lists of parts join to
// one string and the id is 32 characters, OpIDPattern's bound, whatever
// the parent's length. Its parts are the phase, the revision of the row it
// was built from where there is one, and the digest of what it carries, so
// one id is one input at one revision.
func DerivedOpID(parent string, parts ...string) string {
	h := sha256.New()
	var n [8]byte
	for _, p := range append([]string{parent}, parts...) {
		binary.BigEndian.PutUint64(n[:], uint64(len(p)))
		h.Write(n[:])
		h.Write([]byte(p))
	}
	return "~" + hex.EncodeToString(h.Sum(nil))[:31]
}

// newInstance is a new store instance: 32 hex digits from crypto/rand.
func newInstance() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("dispatch: crypto/rand unavailable: " + err.Error())
	}
	return hex.EncodeToString(b[:])
}

// writeInstance writes the store_instance's one row, a new instance, in
// the schema's transaction: the create's, and version 4's migration.
func writeInstance(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, "DELETE FROM store_instance"); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, "INSERT INTO store_instance (id) VALUES (?)", newInstance())
	return err
}

// Instance reads the store's instance (store_instance), "" for a database
// before version 4, read as it was found.
func (s *Store) Instance(ctx context.Context) (string, error) {
	var id string
	err := s.read(ctx, func(q queryer) error {
		if schemaOf(q) < 4 {
			return nil
		}
		err := q.QueryRowContext(ctx, "SELECT id FROM store_instance").Scan(&id)
		if isNoRows(err) {
			return nil
		}
		return err
	})
	return answer(id, err)
}

// Reissue writes a new random instance in the Dispatch database at path,
// a restore's staged copy no server has open: one write in a BEGIN
// IMMEDIATE transaction of its own, nothing else of the database touched,
// so a restored store is never the instance its rows were exported from.
// A database before version 4 has no instance and is left as it is (its
// migration writes one). It refuses a path that is a symbolic link or not
// there.
func Reissue(ctx context.Context, path string) error {
	path, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	fi, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("dispatch: %s is not a regular file", path)
	}
	db, err := sql.Open("sqlite", dsnMode(path, false, false))
	if err != nil {
		return err
	}
	db.SetMaxOpenConns(1)
	defer db.Close()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("dispatch: %w", err)
	}
	defer tx.Rollback()
	var version int
	if err := tx.QueryRowContext(ctx, "SELECT version FROM schema_version").Scan(&version); err != nil {
		return fmt.Errorf("dispatch: schema_version: %w", err)
	}
	if version < 4 {
		return nil
	}
	if err := writeInstance(ctx, tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("dispatch: %w", err)
	}
	return db.Close()
}
