package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
)

// apiError is the JSON error envelope: {"error":{"code":..,"message":..}}.
// A function that answers for a handler also sets status, the HTTP status to
// answer with; as an error (the crew launcher returns one) it is its message.
type apiError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	status  int
}

func (e *apiError) Error() string { return e.Message }

// newAPIError is an apiError to answer with status.
func newAPIError(status int, code, message string) *apiError {
	return &apiError{Code: code, Message: message, status: status}
}

// writeAPIError answers with e.
func writeAPIError(w http.ResponseWriter, e *apiError) {
	writeError(w, e.status, e.Code, e.Message)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]apiError{"error": {Code: code, Message: message}})
}

// maxBody bounds JSON request bodies. A route that takes more passes its own
// bound to decodeJSONLimit.
const maxBody = 64 << 10

// decodeJSON reads a body of at most maxBody bytes into v, rejecting unknown
// fields.
func decodeJSON(w http.ResponseWriter, r *http.Request, v any) error {
	return decodeJSONLimit(w, r, v, maxBody)
}

// decodeOptionalJSON is decodeJSONLimit for a body that may be absent: an
// empty body leaves v as it is.
func decodeOptionalJSON(w http.ResponseWriter, r *http.Request, v any, limit int64) error {
	err := decodeJSONLimit(w, r, v, limit)
	if errors.Is(err, io.EOF) {
		return nil
	}
	return err
}

// errBodyTooLarge is a body past its route's bound, as decodeJSONLimit
// reports it: a route that answers 413 for it tells it from the rest.
var errBodyTooLarge = errors.New("request body too large")

// decodeJSONLimit is decodeJSON with the body bounded to limit bytes: past
// it, errBodyTooLarge, whether the object or what follows it passes it.
func decodeJSONLimit(w http.ResponseWriter, r *http.Request, v any, limit int64) error {
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	var mbe *http.MaxBytesError
	if err := dec.Decode(v); err != nil {
		if errors.As(err, &mbe) {
			return errBodyTooLarge
		}
		return err
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		if errors.As(err, &mbe) {
			return errBodyTooLarge
		}
		return errors.New("request body must contain a single JSON object")
	}
	return nil
}
