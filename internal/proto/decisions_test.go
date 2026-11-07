package proto

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
)

// The decision lane's messages and frames (docs/protocol.md, Decision
// links): each within its bound, each refused past it.

func TestDecisionFrameBounded(t *testing.T) {
	if _, err := Decode(Encode(TypeDecision, make([]byte, MaxDecision))); err != nil {
		t.Fatalf("a decision at its bound: %v", err)
	}
	if _, err := Decode(Encode(TypeDecision, make([]byte, MaxDecision+1))); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("a decision past its bound: %v", err)
	}
	big := json.RawMessage(`"` + strings.Repeat("x", MaxDecision) + `"`)
	if _, err := EncodeDecision(Decision{ReqID: "r1", Item: big}); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("encoding past the bound: %v", err)
	}
	f, err := EncodeDecision(Decision{ReqID: "r1", List: json.RawMessage(`{"items":[]}`)})
	if err != nil || f[0] != TypeDecision {
		t.Fatalf("a small decision: %v %x", err, f[:1])
	}
	// A relay does not carry the lane yet, either way: the switchyard's
	// side is PR-6's, and until then both frames are refused in an
	// envelope as any type it does not carry is.
	for _, typ := range []byte{TypeDecision, TypeDecide} {
		inner := Encode(typ, []byte(`{}`))
		env, _ := EncodeRelay(strings.Repeat("v", ViewerIDLen), inner)
		if _, _, err := DecodeRelay(env[1:]); !errors.Is(err, ErrBadEnvelope) {
			t.Fatalf("a relayed 0x%02x: %v", typ, err)
		}
	}
	// A DECIDE at its bound and one byte past it.
	if _, err := Decode(Encode(TypeDecide, make([]byte, MaxDecide))); err != nil {
		t.Fatalf("a decide at its bound: %v", err)
	}
	if _, err := Decode(Encode(TypeDecide, make([]byte, MaxDecide+1))); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("a decide past its bound: %v", err)
	}
}

// lane decodes payload as the lane control t, failing unless ok matches.
func lane(t *testing.T, ctl string, payload string, ok bool) {
	t.Helper()
	_, _, isLane, err := DecodeLane(ctl, []byte(payload))
	if !isLane {
		t.Fatalf("%s is not a lane message", ctl)
	}
	if (err == nil) != ok {
		t.Fatalf("%s %s: err %v, want ok %v", ctl, payload, err, ok)
	}
}

func TestDecisionsListValid(t *testing.T) {
	lane(t, CtlDecisionsList, `{"t":"decisions_list","reqId":"r1"}`, true)
	lane(t, CtlDecisionsList, `{"t":"decisions_list","reqId":"r1","after":"`+strings.Repeat("a", MaxDecisionCursor)+`"}`, true)
	lane(t, CtlDecisionsList, `{"t":"decisions_list","reqId":"r1","after":"`+strings.Repeat("a", MaxDecisionCursor+1)+`"}`, false)
	lane(t, CtlDecisionsList, `{"t":"decisions_list"}`, false)
	lane(t, CtlDecisionsList, `{"t":"decisions_list","reqId":"`+strings.Repeat("r", MaxDecisionReqID+1)+`"}`, false)
	lane(t, CtlDecisionsList, `{"t":"decisions_list","reqId":"r 1"}`, false)
	lane(t, CtlDecisionsList, `{"t":"decisions_list","reqId":"r1","extra":1}`, false)
	for _, after := range []string{"]", "}", "{}", " x"} {
		lane(t, CtlDecisionsList, `{"t":"decisions_list","reqId":"r1"}`+after, false)
	}
	lane(t, CtlDecisionsList, `{"t":"decisions_list","reqId":"r1"} `+"\n", true)
	if _, _, ok, _ := DecodeLane(CtlChat, []byte(`{}`)); ok {
		t.Fatal("chat is no lane message")
	}
	// A malformed request still names its reqId, so its refusal can.
	if _, id, _, err := DecodeLane(CtlDecisionsList, []byte(`{"t":"decisions_list","reqId":"r9","bogus":true}`)); err == nil || id != "r9" {
		t.Fatalf("a malformed list: %q %v", id, err)
	}
}

func TestDecisionReadValid(t *testing.T) {
	lane(t, CtlDecisionRead, `{"t":"decision_read","reqId":"r1","ref":"task:run-1/build"}`, true)
	lane(t, CtlDecisionRead, `{"t":"decision_read","reqId":"r1","ref":"`+strings.Repeat("q", MaxDecisionItemRef)+`"}`, true)
	lane(t, CtlDecisionRead, `{"t":"decision_read","reqId":"r1","ref":"`+strings.Repeat("q", MaxDecisionItemRef+1)+`"}`, false)
	lane(t, CtlDecisionRead, `{"t":"decision_read","reqId":"r1","ref":""}`, false)
}

func TestDecisionTrackValid(t *testing.T) {
	ok := `{"t":"decision_track","reqId":"r1","ref":"task:run-1/build","receipt":"c-1","attempt":"c-1"}`
	lane(t, CtlDecisionTrack, ok, true)
	lane(t, CtlDecisionTrack, `{"t":"decision_track","reqId":"r1","ref":"task:run-1/build","receipt":"`+strings.Repeat("c", MaxDecisionReceipt)+`","attempt":"`+strings.Repeat("a", MaxDecisionAttempt)+`"}`, true)
	lane(t, CtlDecisionTrack, `{"t":"decision_track","reqId":"r1","ref":"task:run-1/build","receipt":"`+strings.Repeat("c", MaxDecisionReceipt+1)+`","attempt":"a"}`, false)
	lane(t, CtlDecisionTrack, `{"t":"decision_track","reqId":"r1","ref":"task:run-1/build","receipt":"c","attempt":"`+strings.Repeat("a", MaxDecisionAttempt+1)+`"}`, false)
	lane(t, CtlDecisionTrack, `{"t":"decision_track","reqId":"r1","ref":"task:run-1/build","receipt":"c"}`, false)
	lane(t, CtlDecisionTrack, `{"t":"decision_track","reqId":"r1","ref":"task:run-1/build","receipt":"c","attempt":"a b"}`, false)
}

func TestDecisionRecordValid(t *testing.T) {
	lane(t, CtlDecisionRecord, `{"t":"decision_record","reqId":"r1","ref":"decision:d1","part":"head"}`, true)
	lane(t, CtlDecisionRecord, `{"t":"decision_record","reqId":"r1","ref":"decision:d1","part":"history","rev":"`+strings.Repeat("9", MaxDecisionRecordRev)+`","after":"`+strings.Repeat("a", MaxDecisionRecordAfter)+`"}`, true)
	lane(t, CtlDecisionRecord, `{"t":"decision_record","reqId":"r1","ref":"decision:d1","part":"responses"}`, false)
	lane(t, CtlDecisionRecord, `{"t":"decision_record","reqId":"r1","ref":"decision:d1","part":"history","rev":"`+strings.Repeat("9", MaxDecisionRecordRev+1)+`"}`, false)
	lane(t, CtlDecisionRecord, `{"t":"decision_record","reqId":"r1","ref":"decision:d1","part":"history","rev":"1x"}`, false)
	lane(t, CtlDecisionRecord, `{"t":"decision_record","reqId":"r1","ref":"decision:d1","part":"body"}`, false)
	lane(t, CtlDecisionRecord, `{"t":"decision_record","reqId":"r1","ref":"decision:d1","part":"head","after":"`+strings.Repeat("a", MaxDecisionRecordAfter+1)+`"}`, false)
}

func TestDecisionEvidenceValid(t *testing.T) {
	lane(t, CtlDecisionEvidence, `{"t":"decision_evidence","reqId":"r1","ref":"task:run-1/build","evidence":"check:run-1/build/1/test/1"}`, true)
	lane(t, CtlDecisionEvidence, `{"t":"decision_evidence","reqId":"r1","ref":"task:run-1/build","evidence":"`+strings.Repeat("e", MaxDecisionItemRef+1)+`"}`, false)
	lane(t, CtlDecisionEvidence, `{"t":"decision_evidence","reqId":"r1","ref":"task:run-1/build"}`, false)
}

// maxDecideValues are the fixture's values, built here: every field at
// its bound, its texts of characters that escape.
func maxDecideValues() Decide {
	five := 5
	return Decide{
		ReqID:     "r0123456789abcdef0123456789abcde",
		Ref:       "question:" + strings.Repeat("<", 503),
		Action:    strings.Repeat("a", 32),
		Revision:  strings.Repeat("&", 64),
		TicketRev: 9007199254740991,
		Input:     strings.Repeat("\x01", 47) + "\r",
		Choice:    &five,
		Text:      strings.Repeat(`"`, MaxSubmit),
		Rationale: strings.Repeat("<", 341),
		OpID:      "op-" + strings.Repeat("x", 28) + "~",
	}
}

func TestDecideValid(t *testing.T) {
	m := maxDecideValues()
	if !m.Valid() {
		t.Fatal("the fixture's values are not valid")
	}
	for name, mut := range map[string]func(*Decide){
		"reqId":     func(d *Decide) { d.ReqID += "x" },
		"no reqId":  func(d *Decide) { d.ReqID = "" },
		"opId":      func(d *Decide) { d.OpID += "x" },
		"opId char": func(d *Decide) { d.OpID = "a/b" },
		"ref":       func(d *Decide) { d.Ref += "x" },
		"action":    func(d *Decide) { d.Action += "x" },
		"revision":  func(d *Decide) { d.Revision += "x" },
		"input":     func(d *Decide) { d.Input += "x" },
		"text":      func(d *Decide) { d.Text += "x" },
		"rationale": func(d *Decide) { d.Rationale += "<" },
		"choice":    func(d *Decide) { six := 6; d.Choice = &six },
		"negative":  func(d *Decide) { neg := -1; d.Choice = &neg },
		"ticketRev": func(d *Decide) { d.TicketRev = -1 },
	} {
		d := maxDecideValues()
		mut(&d)
		if d.Valid() {
			t.Fatalf("%s past its bound is valid", name)
		}
		if _, err := EncodeDecide(d); !errors.Is(err, ErrBadDecide) {
			t.Fatalf("%s encoded: %v", name, err)
		}
	}
	// Strict: an unknown field, a second object, a bad type, a stray
	// closing delimiter or anything else after the object.
	for _, p := range []string{
		`{"reqId":"r","ref":"task:a/b","action":"retry","revision":"1","opId":"o","by":"me"}`,
		`{"reqId":"r","ref":"task:a/b","action":"retry","revision":"1","opId":"o"}{}`,
		`{"reqId":"r","ref":"task:a/b","action":"retry","revision":"1","opId":"o"}]`,
		`{"reqId":"r","ref":"task:a/b","action":"retry","revision":"1","opId":"o"}}`,
		`{"reqId":"r","ref":"task:a/b","action":"retry","revision":"1","opId":"o"} x`,
		`{"reqId":"r","ref":"task:a/b","action":"retry","revision":1,"opId":"o"}`,
	} {
		if _, err := DecodeDecide([]byte(p)); !errors.Is(err, ErrBadDecide) {
			t.Fatalf("%s: %v", p, err)
		}
		if DecideReqID([]byte(p)) != "r" && !strings.HasPrefix(p, `{"reqId":"r","ref":"task:a/b","action":"retry","revision":"1","opId":"o"}`) {
			t.Fatalf("the reqId of %s", p)
		}
	}
	if m, err := DecodeDecide([]byte(`{"reqId":"r","ref":"task:a/b","action":"retry","revision":"1","opId":"o"} ` + "\n")); err != nil || m.Action != "retry" {
		t.Fatalf("a retry: %+v %v", m, err)
	}
}

// The committed fixture, read by proto.Decode and the strict decoder,
// equals the values byte for byte; Go's own encoding of them, HTML
// escaping and all, and a text of MaxSubmit control characters, fit
// MaxDecide; one byte past it is refused. protocol.test.ts reads the same
// fixture.
func TestDecideFrameAtItsBounds(t *testing.T) {
	raw, err := os.ReadFile("testdata/decide-max.json")
	if err != nil {
		t.Fatal(err)
	}
	f, err := Decode(Encode(TypeDecide, raw))
	if err != nil {
		t.Fatalf("the fixture's frame: %v", err)
	}
	got, err := DecodeDecide(f.Payload)
	if err != nil {
		t.Fatalf("the fixture: %v", err)
	}
	want := maxDecideValues()
	if got.ReqID != want.ReqID || got.Ref != want.Ref || got.Action != want.Action || got.Revision != want.Revision || got.TicketRev != want.TicketRev ||
		got.Input != want.Input || got.Choice == nil || *got.Choice != *want.Choice || got.Text != want.Text || got.Rationale != want.Rationale || got.OpID != want.OpID {
		t.Fatalf("the fixture decoded: %+v", got)
	}
	frame, err := EncodeDecide(want)
	if err != nil || len(frame)-1 > MaxDecide {
		t.Fatalf("Go's encoding: %d bytes, %v", len(frame)-1, err)
	}
	worst := want
	worst.Text = strings.Repeat("\x01", MaxSubmit)
	worst.Ref = "question:" + strings.Repeat("\x01", 503)
	worst.Revision = strings.Repeat("\x01", 64)
	frame, err = EncodeDecide(worst)
	if err != nil || len(frame)-1 > MaxDecide {
		t.Fatalf("the worst encoding: %d bytes, %v", len(frame)-1, err)
	}
	if _, err := Decode(Encode(TypeDecide, bytes.Repeat([]byte(" "), MaxDecide+1))); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("one byte past: %v", err)
	}
}

// fitsControl fails t unless v encodes as a control message within MaxControl.
func fitsControl(t *testing.T, v any) map[string]any {
	t.Helper()
	b := MustControl(v)
	if _, err := Decode(b); err != nil {
		t.Fatalf("%T: %v", v, err)
	}
	var m map[string]any
	if err := json.Unmarshal(b[1:], &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestDecideOkValid(t *testing.T) {
	m := fitsControl(t, DecideOK{T: CtlDecideOK, ReqID: strings.Repeat("<", MaxDecisionReqID), Ref: strings.Repeat("<", MaxDecisionItemRef), Receipt: strings.Repeat("<", MaxDecisionReceipt), Repeat: true})
	if m["t"] != CtlDecideOK || m["repeat"] != true {
		t.Fatalf("%v", m)
	}
	if m := fitsControl(t, DecideOK{T: CtlDecideOK, ReqID: "r", Ref: "task:a/b", Receipt: "c"}); m["repeat"] != nil {
		t.Fatalf("repeat when not: %v", m)
	}
}

func TestDecisionsChangedValid(t *testing.T) {
	if m := fitsControl(t, DecisionsChanged{T: CtlDecisionsChanged, Seq: 1 << 62}); m["t"] != CtlDecisionsChanged || len(m) != 2 {
		t.Fatalf("%v", m)
	}
}

// Every code a refusal carries fits MaxControl with its words, its rev and
// its receipt at their bounds, escaping as much as they can.
func TestDecideRefusedCodes(t *testing.T) {
	for _, code := range []string{DecideStaleRevision, DecideNotInScope, DecideNotDecisionLink, DecideNotFound, DecideAnswered, DecideTooMany,
		DecideUnsupported, DecideInvalid, DecideRecordMoved, DecideRevoked, DecideExpired, DecideOpConflict} {
		m := fitsControl(t, DecideRefused{T: CtlDecideRefused, ReqID: strings.Repeat("r", MaxDecisionReqID), Code: code, Message: strings.Repeat("<", MaxDecideWords/6),
			Rev: strings.Repeat("9", MaxDecisionRecordRev), Ref: strings.Repeat("<", MaxDecisionItemRef), Receipt: strings.Repeat("<", MaxDecisionReceipt)})
		if m["code"] != code {
			t.Fatalf("%v", m)
		}
	}
}

func TestWelcomeSaysDecisions(t *testing.T) {
	m := fitsControl(t, Welcome{T: CtlWelcome, Decisions: true})
	if m["decisions"] != true {
		t.Fatalf("%v", m)
	}
	if m := fitsControl(t, Welcome{T: CtlWelcome}); m["decisions"] != nil {
		t.Fatalf("decisions on a welcome without: %v", m)
	}
}

// Every lane request at its bounds, its strings of characters Go's encoder
// escapes six bytes for one, fits MaxControl and decodes; an evidence ref
// at its bound is valid and one past it is not.
func TestLaneRequestsAtTheirBoundsFitMaxControl(t *testing.T) {
	esc := func(n int) string { return strings.Repeat("<", n) }
	for _, m := range []any{
		DecisionsList{T: CtlDecisionsList, ReqID: esc(MaxDecisionReqID), After: esc(MaxDecisionCursor)},
		DecisionRead{T: CtlDecisionRead, ReqID: esc(MaxDecisionReqID), Ref: esc(MaxDecisionItemRef)},
		DecisionTrack{T: CtlDecisionTrack, ReqID: esc(MaxDecisionReqID), Ref: esc(MaxDecisionItemRef), Receipt: esc(MaxDecisionReceipt), Attempt: strings.Repeat("a", MaxDecisionAttempt)},
		DecisionRecord{T: CtlDecisionRecord, ReqID: esc(MaxDecisionReqID), Ref: esc(MaxDecisionItemRef), Part: RecordHistory, Rev: strings.Repeat("9", MaxDecisionRecordRev), After: esc(MaxDecisionRecordAfter)},
		DecisionEvidence{T: CtlDecisionEvidence, ReqID: esc(MaxDecisionReqID), Ref: esc(MaxDecisionItemRef), Evidence: esc(MaxDecisionItemRef)},
	} {
		b, err := json.Marshal(m)
		if err != nil || len(b) > MaxControl {
			t.Fatalf("%T at its bounds: %d bytes (%v)", m, len(b), err)
		}
		if _, err := Decode(Encode(TypeControl, b)); err != nil {
			t.Fatalf("%T framed: %v", m, err)
		}
		h, _ := ParseHeader(b)
		if _, _, ok, err := DecodeLane(h, b); !ok || err != nil {
			t.Fatalf("%T decoded: %v %v", m, ok, err)
		}
	}
	lane(t, CtlDecisionEvidence, `{"t":"decision_evidence","reqId":"r1","ref":"task:run-1/build","evidence":"`+strings.Repeat("e", MaxDecisionItemRef)+`"}`, true)
}
