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
