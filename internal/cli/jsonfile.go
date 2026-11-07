package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
)

// readOneJSON reads a request file into v: the file named, or stdin for
// "-". Every command that reads a JSON file or stdin goes through it
// (task result, task decide, crew create and add), and the rule is the
// server's for a body:
//
//   - at most limit bytes: limit+1 are read, and a file that has more,
//     one that grew past the limit while it was read among them, is
//     refused whole, never cut at the limit and decoded;
//   - exactly one JSON value, whole: an empty file and a value cut short
//     are refused;
//   - nothing after it but white space: a second value, or anything else,
//     is refused;
//   - a field v does not have is refused.
func readOneJSON(name string, stdin io.Reader, limit int, v any) error {
	r := stdin
	if name != "-" {
		f, err := os.Open(name)
		if err != nil {
			return err
		}
		defer f.Close()
		r = f
	}
	raw, err := io.ReadAll(io.LimitReader(r, int64(limit)+1))
	if err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	if len(raw) > limit {
		return fmt.Errorf("%s: larger than %d bytes", name, limit)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		if errors.Is(err, io.EOF) {
			return fmt.Errorf("%s: no JSON value", name)
		}
		return fmt.Errorf("%s: %w", name, err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return fmt.Errorf("%s: more after its one JSON value", name)
	}
	return nil
}
