package cli

import (
	"errors"
	"fmt"
	"sync"
)

// MaxNotices bounds the third-party notices text a main hands in, in
// bytes. The server holds the text in memory and writes it whole to every
// request; the embedded THIRD_PARTY_NOTICES is about 1.5 MB, so 8 MiB is
// room for a build that links several times as many modules.
const MaxNotices = 8 << 20

// errNoticesTaken refuses a text handed in after the composition root read
// the holder: the server serves what it read at start, nothing later.
var errNoticesTaken = errors.New("the third-party notices were read when the server started; hand them in before app.Run")

// noticesHolder keeps the text a main hands in until the composition root
// reads it, once, at start, which closes it.
type noticesHolder struct {
	mu    sync.Mutex
	text  string
	taken bool
}

// set keeps text, or refuses it: empty, over MaxNotices, a second text, or
// after take. A refused text changes nothing.
func (h *noticesHolder) set(text string) error {
	switch {
	case text == "":
		return errors.New("the third-party notices text is empty: hand in none to serve the embedded notices")
	case len(text) > MaxNotices:
		return fmt.Errorf("the third-party notices text is %d bytes, over the limit of %d", len(text), MaxNotices)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	switch {
	case h.taken:
		return errNoticesTaken
	case h.text != "":
		return errors.New("the third-party notices were handed in already")
	}
	h.text = text
	return nil
}

// take answers the text handed in ("" when none: the server serves the
// embedded notices) and closes the holder; every call after the first
// answers the same.
func (h *noticesHolder) take() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.taken = true
	return h.text
}

// notices is the process's one holder: SetNotices fills it, runServe reads
// it. A test replaces it with a holder of its own.
var notices = &noticesHolder{}

// SetNotices hands in the third-party notices text the server serves at
// /third-party-notices.txt in place of the embedded THIRD_PARTY_NOTICES:
// for a main that links more modules than this one does. It refuses an
// empty text, one over MaxNotices, a second text, and a text handed in
// after a server of this process read the holder at its start.
func SetNotices(text string) error { return notices.set(text) }
