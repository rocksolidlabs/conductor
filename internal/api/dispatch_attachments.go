package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/rocksolidlabs/conductor/internal/dispatch"
)

// A ticket's attachments over HTTP (docs/plans/dispatch.md, PR-7): the
// upload, multipart/form-data, its file streamed into the store
// (dispatch.Store.AddAttachment), which writes it beside the database
// while it hashes it, never into the server's memory whole; the download,
// the content its row names served with the headers that make a browser
// save it rather than render it, unless it is one of a few types that
// cannot script a page; and the removal, a tombstone, the file left to the
// store's sweep. Every route is the admin's, as every Dispatch route is.

const (
	// maxAttachmentEnvelope bounds what an upload's body holds beside its
	// file: whatever comes before its first boundary and after its last,
	// the boundaries, the parts' headers and the operation's fields. A body
	// whose envelope is past it is 413 bound, the bound held while the body
	// is read (uploadBody).
	maxAttachmentEnvelope = 64 << 10
	// maxAttachmentBody bounds an upload's body: its file at the store's
	// bound, dispatch.MaxAttachmentSize, and the envelope. A body past it
	// is 413 bound, whatever it holds.
	maxAttachmentBody = dispatch.MaxAttachmentSize + maxAttachmentEnvelope
	// uploadReadAhead is how far an upload's multipart reader reads the
	// body ahead of what it has parsed: mime/multipart's buffer, 4 KiB.
	// uploadBody gives it that room past the envelope's bound, so a body
	// whose envelope is within the bound is never refused for bytes the
	// reader holds unparsed (the bodies at their bound of
	// TestDispatchAttachmentUploadIsBounded and
	// TestDispatchAttachmentEnvelopeIsBounded, their envelope before their
	// file, hold the room to that).
	uploadReadAhead = 4 << 10
)

// inlineTypes are the content types a download is served as, when the
// stored type is one of them exactly: images, plain text, a PDF and JSON,
// none of which runs a script in the page that shows it. Any other is
// served as application/octet-stream, an SVG among them, since it can.
var inlineTypes = map[string]bool{
	"image/png": true, "image/jpeg": true, "image/gif": true, "image/webp": true,
	"text/plain": true, "application/pdf": true, "application/json": true,
}

// uploadFields are the parts an upload's body takes before its file: the
// operation's.
var uploadFields = []string{"opId", "expectedRev", "byName"}

// errFileNotLast is an upload whose file part is followed by another.
var errFileNotLast = errors.New("the file is the upload's last part")

// uploadBody is an upload's body as its multipart reader reads it: the
// request's body under its bound, counted, and read no further than the
// envelope's bound allows. Its bytes are the file's or the envelope's,
// and the reader reads at most uploadReadAhead past what it has parsed, so
// a read that would take the body past the file's bytes given to the store
// (file), maxAttachmentEnvelope and that room is refused errEnvelope
// (over): an envelope within its bound is never refused, and a header, a
// field, a preamble or an epilogue past it is read no further than that,
// whatever its length, nor held in memory past it. Within that room the
// envelope is known exactly once the body's end is read (past), which
// uploadFile checks before the store's read can end.
type uploadBody struct {
	r    io.Reader
	read int64
	file int64
	over bool
}

func (b *uploadBody) Read(p []byte) (int, error) {
	room := b.file + maxAttachmentEnvelope + uploadReadAhead - b.read
	if room <= 0 {
		b.over = true
		return 0, errEnvelope
	}
	if int64(len(p)) > room {
		p = p[:room]
	}
	n, err := b.r.Read(p)
	b.read += int64(n)
	return n, err
}

// past reports whether the envelope is past its bound: a read was refused
// for it, or the bytes read less the file's are more than
// maxAttachmentEnvelope, which is the envelope itself once the body's end
// is read.
func (b *uploadBody) past() bool {
	return b.over || b.read-b.file > maxAttachmentEnvelope
}

// uploadFile is an upload's file part as the store reads it: the part's
// bytes, and the rest of the body read before the store's read can end,
// so that a body past its bound, or past its envelope's, is known before
// the operation can keep a receipt or an attachment. At the part's end the
// body's end is read, so that the file is known to be the body's last part
// (errFileNotLast otherwise), and then whatever follows its close, and the
// envelope, read whole, is held to its bound (errEnvelope); once the part
// has given more than the store takes (dispatch.MaxAttachmentSize, past
// which it refuses the file bound), whatever is left of the body is read
// before those bytes are given. A body past its bound is then the read's
// error, the http.MaxBytesError or errEnvelope the handler answers 413
// bound, the store keeping nothing. body is the request's body as the part
// is read from it, which counts the file's bytes. err keeps the first
// error the body gave, so the handler tells the body's failure (past its
// bound, malformed, the client gone) from the store's.
type uploadFile struct {
	part *multipart.Part
	mr   *multipart.Reader
	body *uploadBody
	err  error
	done bool
}

func (f *uploadFile) Read(p []byte) (int, error) {
	if f.err != nil {
		return 0, f.err
	}
	if f.done {
		return 0, io.EOF
	}
	n, err := f.part.Read(p)
	f.body.file += int64(n)
	switch {
	case err == io.EOF:
		switch _, next := f.mr.NextRawPart(); {
		case next == io.EOF:
			_, rest := io.Copy(io.Discard, f.body)
			switch {
			case rest != nil:
				err = rest
			case f.body.past():
				err = errEnvelope
			default:
				f.done = true
			}
		case next == nil:
			err = errFileNotLast
		default:
			err = next
		}
	case err == nil && f.body.file > dispatch.MaxAttachmentSize:
		if _, rest := io.Copy(io.Discard, f.body); rest != nil {
			err = rest
		}
	}
	if err != nil && err != io.EOF {
		if f.body.over {
			// The envelope's bound refused a read: that is why, whatever
			// the multipart reader made of the bytes it was given.
			err = errEnvelope
		}
		f.err = err
	}
	return n, err
}

// handleDispatchAddAttachment uploads a file to a live ticket
// (attachment.add): a multipart/form-data body whose parts are the
// operation's fields, opId, then expectedRev and byName when given, each
// once, and last the file, a part named file whose filename is the
// attachment's name and whose Content-Type is its type as given; at most
// maxAttachmentBody, the store's 16 MiB of file and 64 KiB of envelope, a
// body past it 413 bound whatever it holds, and one whose envelope is
// past maxAttachmentEnvelope 413 bound too (uploadBody). The body is read
// to its end before any answer, at most to its bound, so a client that
// sent it whole reads the answer, and before the store can keep anything
// of it (uploadFile), so one past its bound or its envelope's keeps no
// receipt and no attachment, wherever its excess lies. 201 {receipt,
// attachment}; 200 for the receipt given before (repeat); 404 not_found
// for a ticket missing or deleted; 409 expired when the upload's lease
// was swept before its row was written (the upload is sent again), bound
// for a file past 16 MiB in a body within its bound, 50 live attachments
// on the ticket or a type past 100 bytes, invalid for a name that is no
// file's (empty, '/', '\', a control character, "." or ".."), archived;
// 400 invalid_request for a body that is no upload's; 500 store_failed
// when the file could not be written or published.
func (s *Server) handleDispatchAddAttachment(w http.ResponseWriter, r *http.Request) {
	ds, ok := s.dispatchWriter(w)
	if !ok {
		return
	}
	key, ok := ticketKey(w, r)
	if !ok {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxAttachmentBody)
	op, file, name, contentType, err := readUpload(r)
	if err != nil {
		refuseUpload(w, r, err)
		return
	}
	// The operation runs whole once its file is read, a client gone or
	// not, as every write's does, its transactions each waiting at most
	// the store's busy timeout for a lock: dispatchWriteTimeout, counted
	// from the request's start, would end an upload over a slow link.
	ctx := context.WithoutCancel(r.Context())
	rc, _, err := ds.AddAttachment(ctx, op.op(r), key, name, contentType, file)
	switch {
	case file.err != nil:
		refuseUpload(w, r, file.err)
		return
	case err != nil:
		if pastBound(r, nil) {
			writeError(w, http.StatusRequestEntityTooLarge, "bound", boundWords)
			return
		}
		s.dispatchError(w, "ticket", err)
		return
	}
	// The store's read ended at the body's end, or past its own bound with
	// the rest of the body read (uploadFile), either within the body's
	// bound: nothing is left to read, and the receipt answers.
	s.writeOp(w, "ticket", rc, nil, true, "attachment", attachmentAfter(ctx, ds, ""))
}

// boundWords is the words of an upload past its bound.
var boundWords = fmt.Sprintf("an upload is at most %d MiB of file and %d KiB of envelope", dispatch.MaxAttachmentSize>>20, maxAttachmentEnvelope>>10)

// readUpload reads an upload's body up to its file: the operation's
// fields, checked as a JSON write's are, and the file's part, its name
// (the filename as sent: a name the store refuses is its refusal, never
// one cut down to its base) and its type, the envelope read no further
// than its bound allows (uploadBody). Any error is the body's, errEnvelope
// when that bound refused a read: the handler answers it after reading the
// rest (refuseUpload).
func readUpload(r *http.Request) (op dispatchOp, file *uploadFile, name, contentType string, err error) {
	mt, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mt != "multipart/form-data" || params["boundary"] == "" {
		return op, nil, "", "", errors.New("an upload is multipart/form-data")
	}
	body := &uploadBody{r: r.Body}
	defer func() {
		// The envelope's bound refused a read: that is why, whatever the
		// multipart reader made of the bytes it was given.
		if err != nil && body.over {
			err = errEnvelope
		}
	}()
	mr := multipart.NewReader(body, params["boundary"])
	seen := map[string]bool{}
	for {
		part, err := mr.NextRawPart()
		if err == io.EOF {
			return op, nil, "", "", errors.New("the upload has no part named file")
		}
		if err != nil {
			return op, nil, "", "", err
		}
		field := part.FormName()
		if seen[field] {
			return op, nil, "", "", fmt.Errorf("the upload names %q twice", field)
		}
		seen[field] = true
		if field == "file" {
			_, disposition, err := mime.ParseMediaType(part.Header.Get("Content-Disposition"))
			if err != nil {
				return op, nil, "", "", fmt.Errorf("the file part's Content-Disposition: %w", err)
			}
			if !dispatch.OpIDPattern.MatchString(op.OpID) {
				return op, nil, "", "", errors.New("opId is 1 to 32 letters, digits, '.', '_', '~' or '-', and comes before the file")
			}
			return op, &uploadFile{part: part, mr: mr, body: body}, disposition["filename"], part.Header.Get("Content-Type"), nil
		}
		if !slices.Contains(uploadFields, field) {
			return op, nil, "", "", fmt.Errorf("an upload's parts are %s and last file, not %q", strings.Join(uploadFields, ", "), field)
		}
		value, err := io.ReadAll(io.LimitReader(part, maxAttachmentEnvelope+1))
		if err != nil {
			return op, nil, "", "", err
		}
		if len(value) > maxAttachmentEnvelope {
			return op, nil, "", "", errEnvelope
		}
		switch field {
		case "opId":
			op.OpID = string(value)
		case "byName":
			op.ByName = string(value)
		case "expectedRev":
			n, err := strconv.ParseInt(string(value), 10, 64)
			if err != nil || n < 0 {
				return op, nil, "", "", errors.New("expectedRev is the ticket's revision, or 0 for none")
			}
			op.ExpectedRev = n
		}
	}
}

// errEnvelope is an upload whose envelope, its body beside its file, is
// past maxAttachmentEnvelope: a field, a header, a preamble or an epilogue
// too long, or all of them together.
var errEnvelope = errors.New("an upload's envelope is more than its bound beside its file")

// refuseUpload answers an upload whose body the store could not take,
// err saying why, once the rest of the body is read: 413 bound for a body
// past its bound, whatever else is wrong with it, as a JSON body past its
// route's is, and for one whose envelope is past its own (errEnvelope);
// 400 invalid_request otherwise.
func refuseUpload(w http.ResponseWriter, r *http.Request, err error) {
	if pastBound(r, err) || errors.Is(err, errEnvelope) {
		writeError(w, http.StatusRequestEntityTooLarge, "bound", boundWords)
		return
	}
	writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
}

// pastBound reports whether an upload's body passed its bound: err, the
// error a read of it gave, says so already, or reading what is left of it
// does. The rest is read either way, at most to the bound, so a client
// that sent the whole of a body within it reads the answer.
func pastBound(r *http.Request, err error) bool {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		return true
	}
	_, err = io.Copy(io.Discard, r.Body)
	return errors.As(err, &tooLarge)
}

// handleDispatchRemoveAttachment removes a live attachment from a live
// ticket (attachment.remove): body {opId, expectedRev?, byName?},
// expectedRev the ticket's; {receipt, attachment}, the attachment a
// tombstone, its file left to the store's sweep; 404 not_found for a
// ticket missing or deleted, or an attachment missing, removed or another
// ticket's.
func (s *Server) handleDispatchRemoveAttachment(w http.ResponseWriter, r *http.Request) {
	var body dispatchOp
	ds, key, id, ok := s.opAt(w, r, ticketKey, &body, &body)
	if !ok {
		return
	}
	ctx, cancel := opContext(r)
	defer cancel()
	rc, err := ds.RemoveAttachment(ctx, body.op(r), key, id)
	s.writeOp(w, "ticket", rc, err, false, "attachment", attachmentAfter(ctx, ds, id))
}

// attachmentAfter is an attachment as an operation's reply carries it
// (rowAfter): by the id the route names, else the one the receipt names.
func attachmentAfter(ctx context.Context, ds *dispatch.Store, id string) func(dispatch.Receipt) (any, error) {
	return rowAfter(id, func(id string) (dispatch.Attachment, error) { return ds.Attachment(ctx, id) })
}

// handleDispatchAttachment serves a live attachment's content, of a ticket
// deleted or not, from the file its row names: Content-Disposition
// attachment with its name as filename* (RFC 8187), so a browser saves it
// rather than shows it; X-Content-Type-Options nosniff; Content-Type the
// stored type when it is one of inlineTypes, else
// application/octet-stream, an SVG among them; Content-Length. A range is
// served as a range. 404 not_found for an attachment missing or removed;
// 400 invalid_request for an id that is not one and for any parameter.
func (s *Server) handleDispatchAttachment(w http.ResponseWriter, r *http.Request) {
	ds, ok := s.dispatchStore(w)
	if !ok {
		return
	}
	id, ok := pathID(w, r)
	if !ok || !noQuery(w, r) {
		return
	}
	a, f, err := ds.OpenAttachment(r.Context(), id)
	if err != nil {
		s.dispatchError(w, "attachment", err)
		return
	}
	defer f.Close()
	h := w.Header()
	h.Set("Content-Type", servedType(a.ContentType))
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Disposition", attachmentDisposition(a.Name))
	http.ServeContent(w, r, "", time.Time{}, f)
}

// servedType is the Content-Type a download with the stored type is
// served as: the type itself when it is one of inlineTypes, else
// application/octet-stream.
func servedType(stored string) string {
	if inlineTypes[stored] {
		return stored
	}
	return "application/octet-stream"
}

// attachmentDisposition is a download's Content-Disposition: attachment,
// its name as RFC 8187's filename*, UTF-8 with every byte but its
// attr-char percent-encoded.
func attachmentDisposition(name string) string {
	var b strings.Builder
	b.WriteString("attachment; filename*=UTF-8''")
	for i := 0; i < len(name); i++ {
		c := name[i]
		if 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9' || strings.IndexByte("!#$&+-.^_`|~", c) >= 0 {
			b.WriteByte(c)
			continue
		}
		fmt.Fprintf(&b, "%%%02X", c)
	}
	return b.String()
}
