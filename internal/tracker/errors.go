package tracker

import (
	"errors"
	"strconv"
	"time"
)

// The errors a call answers, each a typed value the API maps to one HTTP
// answer. A call's error is a *RequestError wrapping one of them, so
// errors.Is and errors.As find it.
var (
	// ErrNotFound: the item is not there, or the token cannot see it (a
	// system may answer the same for both).
	ErrNotFound = errors.New("not found, or the token cannot see it")
	// ErrUnauthorized: the token is refused (or none was given).
	ErrUnauthorized = errors.New("the token is refused")
	// ErrForbidden: the token lacks a permission.
	ErrForbidden = errors.New("the token lacks a permission")
	// ErrUnavailable: a network failure, a timeout, a 5xx.
	ErrUnavailable = errors.New("the tracker could not be reached")
	// ErrUnsupported: a request the adapter does not make, or one the
	// system refused as invalid.
	ErrUnsupported = errors.New("not supported")
	// ErrBound: an answer past its bound.
	ErrBound = errors.New("the answer is past its bound")
	// ErrTargetChanged: the item a write was prepared for is no longer
	// of that kind or in that repository.
	ErrTargetChanged = errors.New("the item is no longer where the write was prepared for")
	// ErrNothingWritten is also wrapped by a write's error when its
	// answer proves nothing was written; a write's error without it is
	// ErrWriteUnknown's: the write may have been made.
	ErrNothingWritten = errors.New("nothing was written")
	// ErrWriteUnknown: a write's answer does not say whether it was made.
	ErrWriteUnknown = errors.New("not known whether the write was made")
)

// ErrRateLimited is a request refused for the rate limit, until Reset
// (zero when the system did not say).
type ErrRateLimited struct{ Reset time.Time }

func (e *ErrRateLimited) Error() string {
	if e.Reset.IsZero() {
		return "rate limited"
	}
	return "rate limited until " + e.Reset.UTC().Format(time.RFC3339)
}

// ErrMoved is an item answered from a redirect whose node id is not the
// one the caller holds; To is the place of the item answered.
type ErrMoved struct{ To Ref }

func (e *ErrMoved) Error() string {
	return "the reference now names another item (" + e.To.String() + ")"
}

// RequestError is a call's error: the request's method, its path without
// the query, the status (0 when there was no answer), the typed error and
// words of the adapter's own. It never carries a header, a query or a
// body the system sent.
type RequestError struct {
	System string
	Method string
	Path   string
	Status int
	Err    error
	Words  string
	// Cause is a context's error when the call's context ended.
	Cause error
}

func (e *RequestError) Error() string {
	s := e.System + ": " + e.Method + " " + e.Path
	if e.Status != 0 {
		s += ": " + strconv.Itoa(e.Status)
	}
	s += ": " + e.Err.Error()
	if e.Words != "" {
		s += ": " + e.Words
	}
	return s
}

// Unwrap gives the typed error and the context's, when it ended.
func (e *RequestError) Unwrap() []error {
	if e.Cause != nil {
		return []error{e.Err, e.Cause}
	}
	return []error{e.Err}
}

// NothingWritten reports a write's error that proves nothing was written.
func NothingWritten(err error) bool { return errors.Is(err, ErrNothingWritten) }

// joined is a write's error: the call's, with what its answer proves.
type joined struct{ errs []error }

func (j joined) Error() string   { return j.errs[0].Error() }
func (j joined) Unwrap() []error { return j.errs }

// Proven wraps err as a write's error whose answer proves nothing was
// written.
func Proven(err error) error { return joined{[]error{err, ErrNothingWritten}} }

// Unknown wraps err as a write's error whose answer does not say whether
// the write was made.
func Unknown(err error) error { return joined{[]error{err, ErrWriteUnknown}} }
