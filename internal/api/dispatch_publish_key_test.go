package api

import (
	"net/http"
	"strings"
	"testing"
)

// The key part writes the ticket's title as one code span, so a title an
// outside person wrote on GitHub takes no effect in a comment posted
// under the person's account: no mention, no team's, no link, no image,
// whatever backticks it holds.

// hostileTitle holds a team's mention, a person's, a link, an image, an
// issue's reference and a run of two backticks, and ends with a backtick.
const hostileTitle = "Ping @octo-org/team and @alice: see [here](https://e.example/x) ![i](https://e.example/i.png) #7, run ``make`` or `x`"

func TestPublishKeyPartTitleIsInert(t *testing.T) {
	e, g := externalEnv(t)
	p := newPubGH(t, g)
	g.set(12, ghItem{Node: pubNodeID, Title: hostileTitle, Body: "It breaks.", Updated: t0, Labels: []string{}})
	p.node(pubNodeID, "Issue", "octo/app", 12, 4000001)
	e.newDispatchProject(t, "PUB", nil)
	e.importItem(t, "PUB", "https://github.com/octo/app/issues/12", opID(), http.StatusCreated, map[string]any{"images": false})
	offer := e.expect(t, "GET", "/api/dispatch/tickets/PUB-1/publish/offer", nil, http.StatusOK, "")
	var key string
	for _, x := range offer["parts"].([]any) {
		if part := x.(map[string]any); part["name"] == "key" {
			key = part["markdown"].(string)
		}
	}
	// The longest run inside is two backticks, so the fence is three, with a
	// space inside each since the title ends with a backtick.
	want := "Conductor ticket PUB-1: ``` " + hostileTitle + " ```"
	if key != want {
		t.Fatalf("the key part:\n got %q\nwant %q", key, want)
	}
	// What is posted is the preview's bytes, the span among them whole.
	out := e.prepare(t, "PUB-1", map[string]any{"body": "Fixed.\n\n" + key, "parts": []string{"key"}}, http.StatusCreated, "")
	e.sendPub(t, pubOf(t, out)["id"].(string), opID(), out["postSha256"].(string), http.StatusOK, "")
	posted := p.commented(pubNodeID)[0].body
	if posted != out["preview"] || !strings.Contains(posted, want) {
		t.Fatalf("posted %q", posted)
	}
}

func TestInertSpan(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"Fix the loop", "`Fix the loop`"},
		{"@alice [x](https://e.example) ![i](https://e.example/i.png)", "`@alice [x](https://e.example) ![i](https://e.example/i.png)`"},
		{"a `b` c", "``a `b` c``"},
		{"a ```b``` c", "````a ```b``` c````"},
		{"`start", "`` `start ``"},
		{"end`", "`` end` ``"},
		{" both ", "`  both  `"},
		{" one", "` one`"},
		{"   ", "`   `"},
		{"two\nlines\r\nthree\rfour", "`two lines three four`"},
		{"", ""},
	} {
		if got := inertSpan(c.in); got != c.want {
			t.Errorf("inertSpan(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
