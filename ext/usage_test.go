package ext_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/ext"
)

// TestUsageRecord: a Usage round-trips through JSON under
// DisallowUnknownFields, with camelCase tags, Final omitted when false and
// the times when zero.
func TestUsageRecord(t *testing.T) {
	from := time.Date(2026, 11, 18, 10, 0, 0, 0, time.UTC)
	u := ext.Usage{
		Connection:     strings.Repeat("ab", ext.UsageConnectionLen/2),
		Credential:     sha256.Sum256([]byte("example-credential")),
		Subject:        "u-example",
		Sessions:       1,
		SessionSeconds: 60,
		RelayedBytes:   4096,
		Final:          true,
		From:           from,
		To:             from.Add(ext.UsageEvery),
	}
	b, err := json.Marshal(u)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	want := []string{"connection", "credential", "subject", "sessions", "sessionSeconds", "relayedBytes", "final", "from", "to"}
	if len(m) != len(want) {
		t.Fatalf("fields %v, want %v", m, want)
	}
	for _, k := range want {
		if _, ok := m[k]; !ok {
			t.Fatalf("no field %q in %s", k, b)
		}
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	var back ext.Usage
	if err := dec.Decode(&back); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(back, u) {
		t.Fatalf("round trip: %+v, want %+v", back, u)
	}

	b, err = json.Marshal(ext.Usage{Connection: u.Connection, Subject: u.Subject})
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{`"final"`, `"from"`, `"to"`} {
		if bytes.Contains(b, []byte(k)) {
			t.Fatalf("%s written for its zero value: %s", k, b)
		}
	}
}
