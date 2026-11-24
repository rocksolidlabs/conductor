package proto

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// promptShape on a host's register: said, it is one bool on the wire under
// that name, and read back from it; not said (an earlier release's host),
// it is absent, and a register without it is read here as not saying it.
// A bool: the register's bound (MaxHostMessage) does not change.
func TestRegisterSaysItTakesThePromptShape(t *testing.T) {
	b, _ := json.Marshal(Register{T: HostRegister, Proto: ProtoVersion, Host: HostInfo{Name: "laptop", Forms: true, PromptShape: true}})
	if !strings.Contains(string(b), `"forms":true,"promptShape":true}`) {
		t.Fatalf("register %s", b)
	}
	b, _ = json.Marshal(Register{T: HostRegister, Proto: ProtoVersion, Host: HostInfo{Name: "laptop", Forms: true}})
	if strings.Contains(string(b), "promptShape") {
		t.Fatalf("a register that takes no shape %s", b)
	}
	var reg Register
	if err := json.Unmarshal([]byte(`{"t":"register","proto":1,"host":{"name":"laptop","promptShape":true},"session":{}}`), &reg); err != nil || !reg.Host.PromptShape {
		t.Fatalf("from the wire %v %+v", err, reg)
	}
	reg = Register{}
	if err := json.Unmarshal([]byte(`{"t":"register","proto":1,"host":{"name":"laptop","forms":true},"session":{}}`), &reg); err != nil || reg.Host.PromptShape || !reg.Host.Forms {
		t.Fatalf("an earlier release's register %v %+v", err, reg)
	}
}

// registered's recipe carries the agent's prompt shape as promptShape: one
// pattern, absent when there is none (an older server's, or a host that
// did not say it takes it), read back whole with unknown fields refused,
// and bounded as the catalog bounds a pattern: at MaxRecipePattern bytes
// it passes Check, past it Check refuses it in fixed words. A recipe of
// the shape alone watches something (Empty); one of nothing does not.
func TestARecipesPromptShapeRoundTripsWithinItsBound(t *testing.T) {
	shape := `(?m)^ {0,3}› +[^\n]*\z`
	r := HostRecipe{TrustPrompt: `Trust\s*this\s*folder\?`, PromptShape: shape}
	b, _ := json.Marshal(Registered{T: HostRegistered, Links: []string{}, Recipe: &r})
	if !strings.Contains(string(b), `"promptShape":"(?m)^ {0,3}› +[^\\n]*\\z"`) {
		t.Fatalf("registered %s", b)
	}
	var back Registered
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&back); err != nil || back.Recipe == nil || back.Recipe.PromptShape != shape {
		t.Fatalf("read back %+v %v", back.Recipe, err)
	}
	none, _ := json.Marshal(HostRecipe{TrustPrompt: "t"})
	if strings.Contains(string(none), "promptShape") {
		t.Fatalf("a recipe without a shape names one: %s", none)
	}

	at := HostRecipe{PromptShape: strings.Repeat("x", MaxRecipePattern)}
	if err := at.Check(); err != nil {
		t.Fatalf("a shape at its bound: %v", err)
	}
	past := HostRecipe{PromptShape: strings.Repeat("x", MaxRecipePattern+1)}
	if err := past.Check(); !errors.Is(err, ErrRecipePromptShapeBound) {
		t.Fatalf("a shape past its bound: %v", err)
	}
	if err := past.Check(); err.Error() != "recipe: the prompt shape is past 200 bytes" {
		t.Fatalf("the refusal's words: %v", err)
	}
	if (HostRecipe{PromptShape: shape}).Empty() || !(HostRecipe{}).Empty() {
		t.Fatal("Empty")
	}
}
