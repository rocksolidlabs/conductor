package github

import (
	"encoding/json"
	"io"
	"math"
	"strings"
	"testing"

	"github.com/rocksolidlabs/conductor/internal/tracker"
)

// A write's answer is sorted error by error and path by path: nothing
// written only when the mutation's data is null and every error listed is
// of a refusing type at no path or at the mutation's own field; any other
// error beside it, a refusal deeper in the payload, or data beside any
// error is not known. The walk: six kinds, three paths, one, two and three
// errors in every order, the data null and present, for both writes.
func TestWriteErrorsSortedOneByOne(t *testing.T) {
	a := New(Options{Logger: nil})
	kinds := []string{"NOT_FOUND", "FORBIDDEN", "UNPROCESSABLE", "RATE_LIMITED", "INTERNAL", ""}
	type variant struct {
		json      string
		refusing  bool
		kind, pth string
	}
	for _, field := range []string{"addComment", "closeIssue"} {
		paths := map[string]string{"none": "", "the field": `,"path":["` + field + `"]`, "deeper": `,"path":["` + field + `","commentEdge","node",0]`}
		var vs []variant
		for _, k := range kinds {
			for name, p := range paths {
				typ := `"type":"` + k + `",`
				if k == "" {
					typ = ""
				}
				refusing := k != "INTERNAL" && k != "" && name != "deeper"
				vs = append(vs, variant{`{` + typ + `"message":"m"` + p + `}`, refusing, k, name})
			}
		}
		var lists [][]variant
		for _, x := range vs {
			lists = append(lists, []variant{x})
			for _, y := range vs {
				lists = append(lists, []variant{x, y})
				for _, z := range vs {
					lists = append(lists, []variant{x, y, z})
				}
			}
		}
		ep := endpoint{graphqlPath: "/graphql"}
		r := request{method: "POST"}
		n := 0
		for _, list := range lists {
			var errs []string
			proven := true
			for _, v := range list {
				errs = append(errs, v.json)
				proven = proven && v.refusing
			}
			for _, data := range []string{"null", `{"x":1}`} {
				raw := `{"data":{"` + field + `":` + data + `},"errors":[` + strings.Join(errs, ",") + `]}`
				var ans gqlAnswer
				if err := json.Unmarshal([]byte(raw), &ans); err != nil {
					t.Fatal(err)
				}
				_, err := a.sortWrite(ep, r, 200, ans, nil, field)
				want := proven && data == "null"
				if err == nil || tracker.NothingWritten(err) != want {
					t.Fatalf("%s %s: nothing written %v, want %v: %v", field, raw, err != nil && tracker.NothingWritten(err), want, err)
				}
				n++
			}
		}
		if n != 2*(18+18*18+18*18*18) {
			t.Fatalf("%d answers walked", n)
		}
	}
}

// The bounded reader's arithmetic holds for every bound the type allows.
func TestBoundedReaderAtTheTypesEdge(t *testing.T) {
	for _, left := range []int64{math.MaxInt64, math.MaxInt64 - 1, 1 << 40, 100, 99, 1, 0} {
		b := &bounded{rc: io.NopCloser(strings.NewReader(strings.Repeat("x", 100))), left: left, done: func() {}}
		data, err := io.ReadAll(b)
		switch {
		case left >= 100 && (err != nil || len(data) != 100):
			t.Errorf("left %d: %d bytes, %v", left, len(data), err)
		case left < 100 && (err == nil || int64(len(data)) != left):
			t.Errorf("left %d: %d bytes, %v", left, len(data), err)
		}
	}
}
