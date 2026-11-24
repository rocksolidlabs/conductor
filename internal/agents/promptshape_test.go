package agents

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/rocksolidlabs/conductor/internal/catalog"
	"github.com/rocksolidlabs/conductor/internal/proto"
	"github.com/rocksolidlabs/conductor/internal/session"
)

// A recipe's prompt shape, as a host reads it off the wire (strict
// decoding) and takes it (Check, then CompileRecipe), is taken exactly
// where the catalog takes an agent's promptShape: within the bound, RE2
// that compiles and matches no empty text; anything else is refused in
// fixed words (the bound's, or ErrRecipePromptShape). What it compiles to
// is what the catalog compiles, so a hosted session's shape is the
// server's own. The built-in shapes are among those taken.
func TestARecipesPromptShapeIsTakenAsTheCatalogTakesIt(t *testing.T) {
	var shapes []string
	for _, id := range []string{"claude", "codex"} {
		a, ok := catalog.Default().Get(id)
		if !ok || a.PromptShape == "" {
			t.Fatalf("no %s with a prompt shape", id)
		}
		shapes = append(shapes, a.PromptShape)
	}
	shapes = append(shapes, `(?m)^> \z`, strings.Repeat("x", proto.MaxRecipePattern), strings.Repeat("x", proto.MaxRecipePattern+1),
		"(", "a*", ".*", `\z`, "\xff", "(?P<x", `\p{Nope}`, "x{2000}")
	taken := 0
	for _, shape := range shapes {
		b, _ := json.Marshal(proto.HostRecipe{PromptShape: shape})
		var r proto.HostRecipe
		dec := json.NewDecoder(bytes.NewReader(b))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&r); err != nil {
			t.Fatalf("%q: %v", shape, err)
		}
		// The catalog judges what the host was sent, as it would hold it.
		_, catErr := catalog.Load(catalog.File{Agents: []catalog.Agent{{ID: "shaped", Name: "shaped", Command: []string{"/bin/sh"}, PromptShape: r.PromptShape}}})
		hostErr := r.Check()
		var w Watch
		if hostErr == nil {
			w, hostErr = CompileRecipe(&r)
		}
		if (catErr == nil) != (hostErr == nil) {
			t.Errorf("%q: the catalog says %v, the host %v", shape, catErr, hostErr)
			continue
		}
		if hostErr != nil {
			if !errors.Is(hostErr, proto.ErrRecipePromptShapeBound) && !errors.Is(hostErr, ErrRecipePromptShape) {
				t.Errorf("%q: refused in words of its own: %v", shape, hostErr)
			}
			continue
		}
		taken++
		want, err := catalog.CompilePattern(r.PromptShape)
		if err != nil || w.PromptShape == nil || w.PromptShape.String() != want.String() {
			t.Errorf("%q: compiled to %v, the catalog's %v (%v)", shape, w.PromptShape, want, err)
		}
	}
	if taken < 4 {
		t.Fatalf("only %d shapes taken", taken)
	}
}

// Apply sets every field a Watch holds on a session's options: the
// server's own session and a hosted one are started through it.
func TestApplySetsTheWholeWatch(t *testing.T) {
	codex, _ := catalog.Default().Get("codex")
	w, err := CompileRecipe(Recipe(codex))
	if err != nil {
		t.Fatal(err)
	}
	var o session.Options
	w.Apply(&o)
	if o.TrustPattern != w.TrustPattern || len(o.TrustAnswers) != len(w.TrustAnswers) || len(o.Questions) != len(w.Questions) ||
		o.WorkingTitle != w.WorkingTitle || o.ConfirmSubmit != w.ConfirmSubmit || o.PromptShape != w.PromptShape || o.PromptShape == nil {
		t.Fatalf("applied %+v from %+v", o, w)
	}
}
