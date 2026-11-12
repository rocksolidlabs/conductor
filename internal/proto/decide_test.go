package proto

import (
	"encoding/json"
	"math"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestDecideBounds holds each reply of the decide grant to its bound with
// every field at its widest, of characters encoding/json escapes to six
// bytes: GET /api/me's, a decide view whose request's fields fill the
// ledger's 16 KiB as kept, the respond route's reply and refusal, and a
// list of MaxDecideListEntries entries at their widest.
func TestDecideBounds(t *testing.T) {
	size := func(v any) int {
		t.Helper()
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return len(b)
	}
	esc := func(n int) string { return strings.Repeat("<", n) } // six bytes each as encoded
	if n := size(MeReply{Subject: esc(48), Name: strings.Repeat(" ", 40), Grants: []string{"admin", "host", "decide"}}); n > MaxMeReply {
		t.Errorf("the widest MeReply is %d bytes, past %d", n, MaxMeReply)
	}
	choice := 1<<31 - 1
	widest := DecideReceipt{K: 32, ResponseID: strings.Repeat("r", 32), Status: "accepted", Code: "response_conflict", Rev: 1<<31 - 1,
		At: time.Date(2026, 12, 31, 23, 59, 59, 999999999, time.FixedZone("x", -12*3600)), Choice: &choice}
	if n := size(widest); n > 256 {
		t.Errorf("the widest receipt is %d bytes, past 256", n)
	}
	// The request's text at the ledger's bound as kept (16 KiB encoded):
	// the question, six options and the context, every character escaped.
	const request = 16 << 10
	v := DecideView{ID: "dr-9223372036854775807", Rev: 1<<31 - 1, State: "open", Type: "clarification", Question: esc(500), Recommendation: &choice}
	used := 6 * 500
	for range 6 {
		v.Options = append(v.Options, DecideOption{Label: esc(80), Tradeoffs: esc(100)})
		used += 6 * 180
	}
	v.Context = esc((request - used) / 6)
	created := widest.At
	v.CreatedAt = &created
	for range MaxDecideOwn {
		v.Own = append(v.Own, widest)
	}
	v.Outcome, v.OutcomeCode = OutcomeRefused, "response_conflict"
	if n := size(v); n > MaxDecideView {
		t.Errorf("the widest decide view is %d bytes, past %d", n, MaxDecideView)
	}
	if n := size(DecideReply{Receipt: widest, Decision: &v}); n > MaxDecideReply {
		t.Errorf("the widest decide reply is %d bytes, past %d", n, MaxDecideReply)
	}
	if n := size(DecideRefusal{Error: DecideError{Code: "response_conflict", Message: esc(MaxDecideRefusalWords)}, Receipt: &widest}); n > MaxDecideReply {
		t.Errorf("the widest refusal is %d bytes, past %d", n, MaxDecideReply)
	}
	list := DecideList{Cut: true}
	for range MaxDecideListEntries {
		list.Decisions = append(list.Decisions, DecideEntry{ID: "dr-" + strconv.FormatInt(math.MaxInt64, 10), Rev: math.MaxInt})
	}
	if n := size(list); n > MaxDecideList || strconv.IntSize == 64 && n != 11626 {
		t.Errorf("the widest list is %d bytes, want 11,626 within %d", n, MaxDecideList)
	}
}
