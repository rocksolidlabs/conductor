package brakeman

import (
	"encoding/json"
	"strings"
	"testing"
)

// validRefs is a valid form of each kind, some at an edge of their parts.
var validRefs = []string{
	"run:" + runA,
	"run:a-0123abcd",
	"task:" + runA + "/build",
	"task:" + runA + "/a_b-c",
	"attempt:" + runA + "/build/1",
	"attempt:" + runA + "/build/12",
	"check:" + runA + "/build/2/vet/1",
	"command:" + runA + "/retry.build~1",
	"event:" + runA + "/1",
	"event:" + runA + "/900719925474099",
	"message:" + runA + "/7",
	"session:" + sessA,
	"question:" + sessA + "/" + questA,
	"ticket:CND-12",
	"ticket:AB-1",
	"comment:CND-12/" + commentA,
	"ticket:CND-999999999999999999", // Dispatch's largest number: 18 digits
	"comment:CND-999999999999999999/" + commentA,
	"op:admin/op-1",
	"op:session/abc/op-1",
	"commit:" + runA + "/" + strings.Repeat("a", 40),
	"commit:" + runA + "/" + strings.Repeat("b", 64),
	"file:" + sessA + "//home/me/repo/main.go",
	"file:" + sessA + "//",
	"brief:br-1/0",
	"brief:br-12/99",
}

// TestRefGrammar holds each kind to its form: the valid ones parse, each
// near miss is refused, and the 512-byte bound holds.
func TestRefGrammar(t *testing.T) {
	for _, s := range validRefs {
		if _, err := ParseRef(s); err != nil {
			t.Errorf("%s: %v", s, err)
		}
	}
	invalid := []string{
		"",
		"run",
		"nothing:" + runA,
		"Run:" + runA,
		"run:" + runA + "/x",
		"run:team-0000000",   // seven hex digits
		"run:team-0000000A",  // upper case
		"run:-team-0000000a", // a crew id begins with a letter or digit
		"task:" + runA,
		"task:" + runA + "/Build",
		"task:" + runA + "/",
		"attempt:" + runA + "/build/0",
		"attempt:" + runA + "/build/01",
		"attempt:" + runA + "/build/-1",
		"attempt:" + runA + "/build/+1",
		"attempt:" + runA + "/build",
		"check:" + runA + "/build/1/vet",
		"check:" + runA + "/build/1/vet/0",
		"check:" + runA + "/build/1/Vet/1",
		"command:" + runA + "/a b",
		"command:" + runA + "/" + strings.Repeat("c", 33),
		"event:" + runA + "/0",
		"event:" + runA + "/x",
		"message:" + runA,
		"session:0123456789ABCDEF",
		"session:0123456789abcde",
		"question:" + sessA,
		"question:" + sessA + "/" + questA + "/x",
		"ticket:cnd-12",
		"ticket:CND-0",
		"ticket:CND12",
		"ticket:C-1",
		"ticket:CND-1000000000000000000", // 19 digits
		"comment:CND-1000000000000000000/" + commentA,
		"comment:CND-12",
		"comment:CND-12/xyz",
		"op:admin",
		"op:/op-1",
		"op: admin/op-1",
		"op:admin/op 1",
		"op:" + strings.Repeat("a", 61) + "/op-1",
		"commit:" + runA + "/abc1234",
		"commit:" + runA + "/" + strings.Repeat("A", 40),
		"file:" + sessA + "/home/me/main.go", // the path is absolute: two slashes
		"file:" + sessA + "//home/me/../main.go",
		"file:" + sessA + "//home/me/",
		"file:" + sessA,
		"brief:br-0/1",
		"brief:br-1",
		"brief:br-1/100",
		"brief:br-1/01",
		"brief:1/1",
		"run:" + runA + "\n",
		"ticket:CND-12\x1b",
		"file:" + sessA + "//a\u0085b",
		"file:" + sessA + "//a\xffb",
	}
	for _, s := range invalid {
		if r, err := ParseRef(s); err == nil {
			t.Errorf("%q parsed as %+v", s, r)
		}
	}
	// The bound: a file ref of 512 bytes parses, one of 513 does not.
	head := "file:" + sessA + "//"
	at := head + strings.Repeat("p", MaxRef-len(head))
	if len(at) != MaxRef {
		t.Fatalf("built %d bytes", len(at))
	}
	if _, err := ParseRef(at); err != nil {
		t.Errorf("a ref of %d bytes: %v", MaxRef, err)
	}
	if _, err := ParseRef(at + "p"); err == nil || !strings.Contains(err.Error(), "512") {
		t.Errorf("a ref of %d bytes: %v", MaxRef+1, err)
	}
}

// TestARefPrintsAsParsed: every valid ref prints back as it was written,
// and goes through JSON as that string.
func TestARefPrintsAsParsed(t *testing.T) {
	for _, s := range validRefs {
		r, err := ParseRef(s)
		if err != nil {
			t.Fatal(err)
		}
		if got := r.String(); got != s {
			t.Errorf("%s prints as %s", s, got)
		}
		b, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		var want string
		json.Unmarshal(b, &want)
		if want != s {
			t.Errorf("%s is %s in JSON", s, b)
		}
		var back Ref
		if err := json.Unmarshal(b, &back); err != nil || back != r {
			t.Errorf("%s back from JSON: %+v %v", s, back, err)
		}
	}
	if _, err := json.Marshal(Ref{}); err == nil {
		t.Error("a ref of no kind was written")
	}
	var r Ref
	if err := json.Unmarshal([]byte(`"run:nope"`), &r); err == nil {
		t.Error("a bad ref was read")
	}
}

// TestAConstructedRefIsHeldToTheGrammar: Ref's fields are exported, so a
// ref built by hand is checked as a parsed one is: a part of the wrong
// shape, a part its kind does not take, or no such kind is refused by
// Valid, by JSON, by a brief and by a request, and nothing is written.
func TestAConstructedRefIsHeldToTheGrammar(t *testing.T) {
	for _, s := range validRefs {
		if err := ref(t, s).Valid(); err != nil {
			t.Errorf("%s: %v", s, err)
		}
	}
	bad := []Ref{
		{Kind: RefRun, Run: "invalid"},
		{Kind: RefRun, Run: runA, Task: "build"}, // a part a run ref does not take
		{Kind: RefTask, Run: runA, Task: "Build"},
		{Kind: RefAttempt, Run: runA, Task: "build"}, // attempt 0
		{Kind: RefCheck, Run: runA, Task: "build", N: 1, Check: "vet", K: -1},
		{Kind: RefEvent, Run: runA, Seq: 0},
		{Kind: RefSession, Session: "nope"},
		{Kind: RefTicket, Ticket: "cnd-1"},
		{Kind: RefTicket, Ticket: "CND-1", Session: sessA},
		{Kind: RefOp, Actor: "", Op: "op-1"},
		{Kind: RefCommit, Run: runA, Commit: "abc"},
		{Kind: RefFile, Session: sessA, Path: "relative"},
		{Kind: RefBrief, Request: "br-1", Item: MaxItems},
		{Kind: "nothing", Run: runA},
	}
	for _, r := range bad {
		if err := r.Valid(); err == nil {
			t.Errorf("%+v is valid", r)
		}
		if b, err := json.Marshal(r); err == nil {
			t.Errorf("%+v was written as %s", r, b)
		}
		b := goodBrief(t)
		b.Items[0].Fact.Evidence = []Ref{r}
		wantInvalid(t, check(b), "items[0].evidence[0]")

		f := newFixture(t)
		_, _, err := f.s.Create(Request{OpID: "op-1", Kind: KindAsk, Note: "why?", Subjects: []Ref{r}})
		wantInvalid(t, err, "subjects[0]")
		if exists(t, f.reqPath("br-1.json")) {
			t.Errorf("%+v: a request was written", r)
		}
	}
}
