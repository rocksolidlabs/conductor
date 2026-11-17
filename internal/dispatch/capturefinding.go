package dispatch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"
)

// A capture as a review's finding (docs/plans/browser-evidence.md, "After
// RV1 and DR1", PR-10, #798). The client names the capture, a send of it
// to a review, and the annotation's place (NewFinding.Capture); the store
// binds it inside the review's transaction (captureFinding): the send is
// that capture's and a send to a review, the document it froze is the
// review's own candidate's capture (its origin a candidate's preview
// checkout, of the candidate's run, task, attempt and commit), and the
// annotation is one of that document's. The finding's words, path and line
// are then the store's projection of the annotation (ProjectAnnotation),
// never the client's: an annotation holds a note of 4,000 characters and a
// report of 32 KiB, a finding's words 1,000. Every part of the page's text
// in the words is folded onto its line and cut to its bound; the person's
// note keeps its lines, and the repair's task file fences it.

// Bounds of a capture finding's projection, in characters.
const (
	projectedNote     = 600
	projectedName     = 80
	projectedSelector = 160
)

// CaptureProjection is a capture finding as a review keeps it: its words,
// and the path and line of the annotation's first tracked source, found in
// the tree at the save ("" and 0 for none).
type CaptureProjection struct {
	Message string `json:"message"`
	Path    string `json:"path,omitempty"`
	Line    int    `json:"line,omitempty"`
}

// ProjectAnnotation is the finding a capture's annotation k (from 1) of the
// document doc makes, within MaxFindingMessage characters: the person's
// note cut to projectedNote characters on a word with "…" after it; the
// element's tag, name and selector, or the region's place; what a click at
// its centre reaches when that is another element; and its first tracked
// source as the finding's path and line. Invalid for an annotation the
// document has not.
func ProjectAnnotation(doc CaptureDocument, k int) (CaptureProjection, error) {
	if k < 1 || k > len(doc.Annotations) {
		return CaptureProjection{}, refuse(RefusalInvalid, "the capture's document has %d annotations, not an annotation %d", len(doc.Annotations), k)
	}
	a := doc.Annotations[k-1]
	var lines []string
	if e := a.Element; e != nil {
		at := "Element " + oneLine(e.Tag, 32)
		if name := oneLine(e.Name, projectedName); name != "" {
			at += " named " + quoteWords(name)
		}
		lines = append(lines, at+" at "+quoteWords(oneLine(e.Selector, projectedSelector))+".")
		if !e.Hit.Self && e.Hit.Selector != "" {
			lines = append(lines, "A click at its centre reaches "+quoteWords(oneLine(e.Hit.Selector, projectedSelector))+" instead.")
		}
	} else {
		r := a.Rect
		lines = append(lines, fmt.Sprintf("A region of the screenshot at %d, %d, %d by %d pixels.", int(r.X), int(r.Y), int(r.Width), int(r.Height)))
	}
	p := CaptureProjection{}
	for _, s := range a.Sources {
		if s.Class == "tracked" && s.Found && !s.Outside && s.Path != "" && checkField("finding.path", "a source's path", s.Path) == nil && oneLineText(s.Path) {
			p.Path = s.Path
			if s.Line > 0 && s.Line <= MaxFindingLine {
				p.Line = s.Line
			}
			at := s.Path
			if p.Line > 0 {
				at += fmt.Sprintf(":%d", p.Line)
			}
			lines = append(lines, "Drawn by "+quoteWords(oneLine(at, projectedSelector))+".")
			break
		}
	}
	rest := strings.Join(lines, "\n")
	room := MaxFindingMessage - utf8.RuneCountInString(rest) - 2
	note := cutOnWord(strings.TrimSpace(a.Note), min(projectedNote, room))
	switch {
	case note == "":
		p.Message = "(No note.)\n\n" + rest
	default:
		p.Message = note + "\n\n" + rest
	}
	if err := checkField("finding.message", "the capture finding's words", p.Message); err != nil {
		return CaptureProjection{}, err
	}
	return p, nil
}

// oneLine is page text on one line, its runs of spaces, line breaks and
// control characters one space, cut to n characters with "…".
func oneLine(s string, n int) string {
	s = strings.Join(strings.FieldsFunc(s, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) || r == ' ' || r == ' ' }), " ")
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n-1]) + "…"
}

// oneLineText says s holds no line break and no control character.
func oneLineText(s string) bool {
	return !strings.ContainsFunc(s, func(r rune) bool { return unicode.IsControl(r) || r == ' ' || r == ' ' || r == '\u0085' })
}

// quoteWords is a value of the page's in the words: in quotation marks.
func quoteWords(s string) string { return "“" + s + "”" }

// cutOnWord is s within n characters: whole when it fits, else cut at the
// last space within its first n-1 characters (or at n-1 when there is
// none in their second half) with "…" after it.
func cutOnWord(s string, n int) string {
	if n < 2 {
		return ""
	}
	rs := []rune(s)
	if len(rs) <= n {
		return s
	}
	cut := rs[:n-1]
	for i := len(cut) - 1; i >= len(cut)/2; i-- {
		if unicode.IsSpace(cut[i]) {
			cut = cut[:i]
			break
		}
	}
	return strings.TrimRightFunc(string(cut), unicode.IsSpace) + "…"
}

// captureFinding binds a capture finding's capture to the review's
// candidate c, read through q with the content files under root (the
// attachments' directory), and gives its projection. Refused invalid: no
// such send, a send of another capture, a send not to a review, a document
// the send does not hold, a document not of the candidate's capture (its
// origin not that candidate's preview checkout: its run, task, attempt and
// commit), and an annotation the document has not.
func captureFinding(ctx context.Context, q queryer, root string, c Candidate, ref CaptureRef) (CaptureProjection, error) {
	var capture, kind, digest string
	err := q.QueryRowContext(ctx, "SELECT capture_id, kind, digest FROM capture_sends WHERE id = ?", ref.Send).Scan(&capture, &kind, &digest)
	switch {
	case isNoRows(err):
		return CaptureProjection{}, refuse(RefusalInvalid, "the send %s is no send of the capture %s", ref.Send, ref.Capture)
	case err != nil:
		return CaptureProjection{}, err
	case capture != ref.Capture:
		return CaptureProjection{}, refuse(RefusalInvalid, "the send %s is no send of the capture %s", ref.Send, ref.Capture)
	case kind != SendReview:
		return CaptureProjection{}, refuse(RefusalInvalid, "the send %s went to %s, not to a review: a capture finding stands on a send to a review", ref.Send, kind)
	}
	var held bool
	if err := q.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM capture_files WHERE capture_id = ? AND sha256 = ? AND holder = ?)", capture, digest, ref.Send).Scan(&held); err != nil {
		return CaptureProjection{}, err
	}
	if !held || !hashPattern.MatchString(digest) {
		return CaptureProjection{}, refuse(RefusalInvalid, "the send %s holds no document", ref.Send)
	}
	doc, err := readDocument(root, digest)
	if err != nil {
		return CaptureProjection{}, err
	}
	o := doc.Origin
	if doc.ID != ref.Capture || o.Kind != OriginCandidate || o.Candidate != c.String() || o.Run != c.Run || o.Task != c.Task || o.Attempt != c.Attempt {
		return CaptureProjection{}, refuse(RefusalInvalid, "the capture %s is not of the candidate reviewed (%s): a review's capture finding is a capture of that candidate's preview", ref.Capture, c.Name())
	}
	return ProjectAnnotation(doc, ref.K)
}

// readDocument reads the frozen document with the SHA-256 sum from the
// content files under root, at most MaxCaptureDocument bytes, its hash
// checked.
func readDocument(root, sum string) (CaptureDocument, error) {
	f, err := os.Open(contentPath(root, sum))
	if errors.Is(err, fs.ErrNotExist) {
		return CaptureDocument{}, refuse(RefusalInvalid, "the document %s is gone from the store", sum)
	}
	if err != nil {
		return CaptureDocument{}, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, MaxCaptureDocument+1))
	if err != nil {
		return CaptureDocument{}, err
	}
	if len(b) > MaxCaptureDocument || sha256Hex(string(b)) != sum {
		return CaptureDocument{}, fmt.Errorf("dispatch: the document %s is not the content its name says", sum)
	}
	var doc CaptureDocument
	if err := json.Unmarshal(b, &doc); err != nil {
		return CaptureDocument{}, fmt.Errorf("dispatch: the document %s: %w", sum, err)
	}
	return doc, nil
}

// CaptureFinding is the projection a capture finding naming ref makes on a
// review of the candidate (ParseCandidate's form), read as the review's
// transaction binds it (captureFinding): the API measures the request it
// makes before anything is kept. A *Refusal for one the store refuses.
func (s *Store) CaptureFinding(ctx context.Context, candidate string, ref CaptureRef) (CaptureProjection, error) {
	c, err := ParseCandidate(candidate)
	if err != nil {
		return CaptureProjection{}, err
	}
	if !ValidID(ref.Capture) || !ValidID(ref.Send) {
		return CaptureProjection{}, refuse(RefusalInvalid, "the capture %q and the send %q are not ids of the store's", ref.Capture, ref.Send)
	}
	var p CaptureProjection
	err = s.read(ctx, func(q queryer) error {
		if schemaOf(q) < findingCaptureSchema {
			return refuse(RefusalInvalid, "this store keeps no capture finding")
		}
		p, err = captureFinding(ctx, q, filepath.Join(s.dir, attachmentsDir), c, ref)
		return err
	})
	return p, err
}

// EvidenceFile is one file a capture send holds, as a repair's request
// names it: its name (the capture's id and the revision sent), its
// SHA-256, its size and the send.
type EvidenceFile struct {
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
	Bytes  int64  `json:"bytes"`
	Send   string `json:"send"`
}

// SendEvidence is the files the send with the id holds, the document it
// froze then its screenshot when it carried one, each named as a ticket's
// attachment is (fileName), its size read from its content file:
// ErrNotFound for no such send.
func (s *Store) SendEvidence(ctx context.Context, id string) ([]EvidenceFile, error) {
	sv, err := s.CaptureSend(ctx, id)
	if err != nil {
		return nil, err
	}
	f, err := s.OpenSendFile(ctx, id, sv.Digest)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, MaxCaptureDocument+1))
	if err != nil {
		return nil, err
	}
	var doc CaptureDocument
	if len(b) > MaxCaptureDocument || sha256Hex(string(b)) != sv.Digest || json.Unmarshal(b, &doc) != nil {
		return nil, fmt.Errorf("dispatch: the document of the send %s is not the content its name says", id)
	}
	named := Capture{CaptureDocument: CaptureDocument{ID: sv.Capture, Rev: sv.Rev}}
	out := []EvidenceFile{{Name: fileName(named, "json"), SHA256: sv.Digest, Bytes: int64(len(b)), Send: id}}
	if img := doc.Image; img.SHA256 != "" {
		out = append(out, EvidenceFile{Name: fileName(named, "png"), SHA256: img.SHA256, Bytes: img.Bytes, Send: id})
	}
	return out, nil
}
