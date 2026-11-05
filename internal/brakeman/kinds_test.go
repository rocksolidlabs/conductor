package brakeman

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// harness is a brief CheckBrief takes, as JSON values: a fact first, then
// every kind's example in the table's order, then another draft ticket,
// which a draft ticket's blocks may name. at is each kind's example's
// index, other the last draft ticket's.
func harness(t *testing.T) (brief map[string]any, at map[string]int, other int) {
	t.Helper()
	items := []any{jsonValue(t, kinds[0].Example)}
	at = map[string]int{}
	for _, k := range kinds {
		at[k.Kind] = len(items)
		items = append(items, jsonValue(t, k.Example))
	}
	other = len(items)
	items = append(items, map[string]any{"kind": ItemDraftTicket, "project": "CND", "title": "Another"})
	return map[string]any{"schema": BriefSchema, "title": "Overnight", "summary": "What ran.", "items": items}, at, other
}

func jsonValue(t *testing.T, raw json.RawMessage) any {
	t.Helper()
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("%s: %v", raw, err)
	}
	return v
}

// decodeValue is DecodeBrief of the brief v, with every permission.
func decodeValue(t *testing.T, v any) error {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	_, err = DecodeBrief(b, allowAll, goodFacts())
	return err
}

// refusedAt says why err is not an *Invalid of field, "" when it is.
func refusedAt(err error, field string) string {
	var inv *Invalid
	switch {
	case err == nil:
		return "taken"
	case !errors.As(err, &inv):
		return "refused, not as invalid: " + err.Error()
	case inv.Field != field:
		return "refused at " + inv.Field + ": " + inv.Why
	}
	return ""
}

// step is one element of a path into a brief's JSON: a key or an index.
type step any

// pathName is a path as a refusal names its field: items[3].alternatives[0].label.
func pathName(path []step) string {
	var b strings.Builder
	for _, s := range path {
		switch s := s.(type) {
		case string:
			if b.Len() > 0 {
				b.WriteByte('.')
			}
			b.WriteString(s)
		case int:
			fmt.Fprintf(&b, "[%d]", s)
		}
	}
	return b.String()
}

// deepCopy is a deep copy of a JSON value.
func deepCopy(t *testing.T, v any) any {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var out any
	_ = json.Unmarshal(b, &out)
	return out
}

// get answers the value at path in v.
func get(v any, path []step) (any, bool) {
	for _, s := range path {
		switch s := s.(type) {
		case string:
			m, ok := v.(map[string]any)
			if !ok {
				return nil, false
			}
			if v, ok = m[s]; !ok {
				return nil, false
			}
		case int:
			l, ok := v.([]any)
			if !ok || s >= len(l) {
				return nil, false
			}
			v = l[s]
		}
	}
	return v, true
}

// with is a copy of brief with the value at path set to val, or removed
// when del.
func with(t *testing.T, brief any, path []step, val any, del bool) any {
	t.Helper()
	out := deepCopy(t, brief)
	parent, ok := get(out, path[:len(path)-1])
	if !ok {
		t.Fatalf("no %s", pathName(path[:len(path)-1]))
	}
	switch last := path[len(path)-1].(type) {
	case string:
		m := parent.(map[string]any)
		if del {
			delete(m, last)
		} else {
			m[last] = val
		}
	case int:
		parent.([]any)[last] = val
	}
	return out
}

// refOf is the i-th of distinct refs of kind.
func refOf(kind string, i int) string {
	switch kind {
	case RefSession:
		return fmt.Sprintf("session:%016x", i+1)
	case RefAttempt:
		return fmt.Sprintf("attempt:%s/build/%d", exRun, i+1)
	case RefTask:
		return fmt.Sprintf("task:%s/t%d", exRun, i+1)
	case RefCheck:
		return fmt.Sprintf("check:%s/build/%d/tests/1", exRun, i+1)
	case RefCommand:
		return fmt.Sprintf("command:%s/c-%d", exRun, i+1)
	case RefOp:
		return fmt.Sprintf("op:admin/op-%d", i+1)
	case RefQuestion:
		return fmt.Sprintf("question:%s/%016x", exSession, i+1)
	case RefTicket:
		return fmt.Sprintf("ticket:CND-%d", i+1)
	case RefCommit:
		return fmt.Sprintf("commit:%s/%040x", exRun, i+1)
	}
	return fmt.Sprintf("run:fix-%08x", i+1)
}

// refKind is a ref kind f takes, and one it does not (none when f takes
// any).
func refKinds(f Field) (takes, not string) {
	if len(f.Refs) == 0 {
		return RefRun, ""
	}
	if !slices.Contains(f.Refs, RefRun) {
		return f.Refs[0], RefRun
	}
	return f.Refs[0], RefSession
}

// names is a valid name of each name field, for one an example leaves out.
var names = map[string]string{"op": "task_retry", "project": "CND", "task": "build", "expectedRevision": "30", "subjectRevision": "30"}

// fieldCase holds one field of the table to CheckBrief at path in brief:
// given, left out, at its bounds and one past them, and each list's
// entry by its own field. index is a valid index for an index entry.
type fieldCase struct {
	t     *testing.T
	index int
}

// sample is a valid value of f.
func (c fieldCase) sample(f Field) any {
	switch f.Type {
	case FieldText:
		return "x"
	case FieldName:
		return names[f.Name]
	case FieldInteger:
		return max(f.Min, 0)
	case FieldObject:
		return map[string]any{}
	case FieldEntry:
		e := map[string]any{}
		for _, sub := range f.Fields {
			e[sub.Name] = c.sample(sub)
		}
		return e
	case FieldRef:
		k, _ := refKinds(f)
		return refOf(k, 0)
	case FieldIndex:
		return c.index
	case FieldList:
		n := max(f.Min, 1)
		return c.entries(f, n, nil)
	}
	c.t.Fatalf("no sample of %s", f.Type)
	return nil
}

// entries is n entries of list f: distinct refs, else first's copies (a
// sample's when first is nil).
func (c fieldCase) entries(f Field, n int, first any) []any {
	out := make([]any, n)
	for i := range out {
		switch {
		case f.Of.Type == FieldRef:
			k, _ := refKinds(*f.Of)
			out[i] = refOf(k, i)
		case first != nil:
			out[i] = deepCopy(c.t, first)
		default:
			out[i] = c.sample(*f.Of)
		}
	}
	return out
}

// expect checks brief: taken when want is "", else refused at want.
func (c fieldCase) expect(what string, brief any, want string) {
	c.t.Helper()
	err := decodeValue(c.t, brief)
	if want == "" {
		if err != nil {
			c.t.Errorf("%s: refused: %v", what, err)
		}
		return
	}
	if why := refusedAt(err, want); why != "" {
		c.t.Errorf("%s: want refused at %s, %s", what, want, why)
	}
}

// field runs every case of f at path in brief.
func (c fieldCase) field(brief any, path []step, f Field) {
	c.t.Helper()
	name := pathName(path)
	if _, ok := get(brief, path); !ok {
		brief = with(c.t, brief, path, c.sample(f), false)
		c.expect(name+" given a sample", brief, "")
	}
	set := func(what string, v any, want string) {
		c.t.Helper()
		c.expect(name+" "+what, with(c.t, brief, path, v, false), want)
	}
	if f.Name != "" {
		want := ""
		if f.Required {
			want = name
		}
		c.expect(name+" left out", with(c.t, brief, path, nil, true), want)
	}
	switch f.Type {
	case FieldText:
		unit := "a"
		if f.Chars {
			unit = "é"
		} else {
			// Bytes, not characters: half as many two-byte runes and one
			// more is past the bound.
			set("past its bytes in two-byte runes", strings.Repeat("é", f.Max/2+1), name)
		}
		set("at its bound", strings.Repeat(unit, f.Max), "")
		set("one past", strings.Repeat(unit, f.Max+1), name)
		if f.Required {
			set("blank", " \t", name)
		} else {
			set("empty", "", "")
		}
	case FieldName:
		if f.Name == "schema" {
			set("another schema", "brakeman.brief/2", name)
			return
		}
		cur, _ := get(brief, path)
		first := cur.(string)[:1]
		set("at its bound", strings.Repeat(first, f.Max), "")
		set("one past", strings.Repeat(first, f.Max+1), name)
		set("outside its pattern", "no way!", name)
	case FieldInteger:
		// A recommendation's bound is its card's alternatives, two here.
		if f.Name != "recommendation" {
			set("at its most", f.Max, "")
		}
		set("at its least", f.Min, "")
		set("one past", f.Max+1, name)
		set("one under", f.Min-1, name)
	case FieldObject:
		head, tail := `{"note":"`, `"}`
		obj := func(n int) any {
			return json.RawMessage(head + strings.Repeat("n", n-len(head)-len(tail)) + tail)
		}
		set("at its bound", obj(f.Max), "")
		set("one past", obj(f.Max+1), name)
		set("a list", []any{1}, name)
	case FieldEntry:
		for _, sub := range f.Fields {
			c.field(brief, append(slices.Clone(path), sub.Name), sub)
		}
	case FieldRef:
		takes, not := refKinds(f)
		set("a ref it takes", refOf(takes, 7), "")
		if not != "" {
			set("a "+not+" ref", refOf(not, 7), name)
		}
		set("no ref", "nope:1", name)
	case FieldIndex:
		set("no item's index", -1, name)
		set("past the brief's items", 1000, name)
	case FieldList:
		c.list(brief, path, f)
	}
}

// list runs a list's cases: its counts, then its first entry's own.
func (c fieldCase) list(brief any, path []step, f Field) {
	c.t.Helper()
	name := pathName(path)
	cur, _ := get(brief, path)
	var first any
	if l, ok := cur.([]any); ok && len(l) > 0 {
		first = l[0]
	}
	count := func(what string, n int, want string) {
		c.t.Helper()
		b := with(c.t, brief, path, c.entries(f, n, first), false)
		if f.Name == "columns" {
			// A row holds one cell a column.
			for _, row := range b.(map[string]any)["items"].([]any)[path[1].(int)].(map[string]any)["rows"].([]any) {
				row.(map[string]any)["cells"] = c.entries(Field{Of: &Field{Type: FieldText}}, n, "c")
			}
		}
		c.expect(name+" "+what, b, want)
	}
	if f.Max > 0 {
		// The blocks of a draft ticket are distinct draft tickets, one here.
		if f.Name != "blocks" {
			count(fmt.Sprintf("at its most, %d", f.Max), f.Max, "")
		}
		count(fmt.Sprintf("one past, %d", f.Max+1), f.Max+1, name)
	}
	if f.Min > 0 {
		count(fmt.Sprintf("at its least, %d", f.Min), f.Min, "")
		count(fmt.Sprintf("one under, %d", f.Min-1), f.Min-1, name)
	}
	if f.Of.Type == FieldItem {
		return // each kind's fields are held on their own
	}
	if first == nil {
		brief = with(c.t, brief, path, c.entries(f, max(f.Min, 1), nil), false)
	}
	c.field(brief, append(slices.Clone(path), 0), *f.Of)
}

// TestTheKindTableIsWhatCheckBriefTakes holds every field of the table,
// the brief's own and each kind's, to DecodeBrief and CheckBrief: given
// and left out, at each bound and one past it, each list's entries by
// their own fields; so brief_submit's schema and the guide's words, made
// from the table, say what the check takes.
func TestTheKindTableIsWhatCheckBriefTakes(t *testing.T) {
	brief, at, other := harness(t)
	if err := decodeValue(t, brief); err != nil {
		t.Fatalf("the harness: %v", err)
	}
	for _, f := range briefFields {
		fieldCase{t: t}.field(brief, []step{f.Name}, f)
	}
	for _, k := range kinds {
		i := at[k.Kind]
		index := 0 // a fact
		if k.Kind == ItemDraftTicket {
			index = other
		}
		for _, f := range k.Fields {
			fieldCase{t: t, index: index}.field(brief, []step{"items", i, f.Name}, f)
		}
		// A field the kind does not take is refused, naming the ones it does.
		b := with(t, brief, []step{"items", i, "sources"}, []any{"run:" + exRun}, false)
		err := decodeValue(t, b)
		var inv *Invalid
		if !errors.As(err, &inv) || inv.Field != fmt.Sprintf("items[%d].sources", i) || !strings.Contains(inv.Why, "kind, "+fieldNames(k.Fields)) {
			t.Errorf("%s with refs: %v", k.Kind, err)
		}
	}
}

// Each kind's fields in the table are its type's JSON fields, in order,
// and the brief's are Brief's: nothing the decoder takes is left out of
// the schema, and nothing the schema names is refused as unknown.
func TestTheKindTableNamesEveryField(t *testing.T) {
	jsonNames := func(typ reflect.Type) []string {
		var out []string
		for i := range typ.NumField() {
			name, _, _ := strings.Cut(typ.Field(i).Tag.Get("json"), ",")
			out = append(out, name)
		}
		return out
	}
	tableNames := func(fields []Field) []string {
		var out []string
		for _, f := range fields {
			out = append(out, f.Name)
		}
		return out
	}
	if got, want := tableNames(briefFields), jsonNames(reflect.TypeFor[Brief]()); !slices.Equal(got, want) {
		t.Errorf("the brief's fields: the table %v, Brief %v", got, want)
	}
	if len(kinds) != 11 {
		t.Fatalf("%d kinds", len(kinds))
	}
	for _, k := range kinds {
		var it Item
		if err := json.Unmarshal(k.Example, &it); err != nil || it.Kind != k.Kind {
			t.Fatalf("%s's example: %v", k.Kind, err)
		}
		v, err := it.body()
		if err != nil {
			t.Fatal(err)
		}
		typ := reflect.TypeOf(v).Elem()
		if got, want := tableNames(k.Fields), jsonNames(typ); !slices.Equal(got, want) {
			t.Errorf("%s: the table %v, %s %v", k.Kind, got, typ.Name(), want)
		}
		// An entry's fields are its element type's.
		for i, f := range k.Fields {
			if f.Type != FieldList || f.Of.Type != FieldEntry {
				continue
			}
			elem := typ.Field(i).Type.Elem()
			if got, want := tableNames(f.Of.Fields), jsonNames(elem); !slices.Equal(got, want) {
				t.Errorf("%s.%s: the table %v, %s %v", k.Kind, f.Name, got, elem.Name(), want)
			}
		}
	}
}

// The schema is the table: each kind a branch of items' oneOf with its
// fields as properties, its required ones required, its example among
// its examples; and the example brief is one DecodeBrief takes.
func TestTheSchemaIsTheTable(t *testing.T) {
	s := BriefJSONSchema(nil)
	enc, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	var schema struct {
		Required   []string `json:"required"`
		Properties struct {
			Items struct {
				MinItems int `json:"minItems"`
				MaxItems int `json:"maxItems"`
				Items    struct {
					OneOf []struct {
						Properties map[string]map[string]any `json:"properties"`
						Required   []string                  `json:"required"`
						Examples   []json.RawMessage         `json:"examples"`
					} `json:"oneOf"`
				} `json:"items"`
			} `json:"items"`
		} `json:"properties"`
		Examples []json.RawMessage `json:"examples"`
	}
	if err := json.Unmarshal(enc, &schema); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(schema.Required, []string{"schema", "title", "summary", "items"}) || schema.Properties.Items.MinItems != 1 || schema.Properties.Items.MaxItems != MaxItems {
		t.Errorf("the brief's schema: %s", enc)
	}
	branches := schema.Properties.Items.Items.OneOf
	if len(branches) != len(kinds) {
		t.Fatalf("%d branches", len(branches))
	}
	for i, k := range kinds {
		br := branches[i]
		if br.Properties["kind"]["const"] != k.Kind {
			t.Errorf("branch %d is %v", i, br.Properties["kind"])
		}
		want := []string{"kind"}
		for _, f := range k.Fields {
			if br.Properties[f.Name] == nil {
				t.Errorf("%s: no property %s", k.Kind, f.Name)
			}
			if f.Required {
				want = append(want, f.Name)
			}
		}
		if len(br.Properties) != len(k.Fields)+1 || !slices.Equal(br.Required, want) {
			t.Errorf("%s: properties %v, required %v, want %v", k.Kind, br.Properties, br.Required, want)
		}
		if len(br.Examples) != 1 || string(br.Examples[0]) != compact(t, k.Example) {
			t.Errorf("%s: examples %s, want %s", k.Kind, br.Examples, k.Example)
		}
	}
	if len(schema.Examples) != 1 {
		t.Fatalf("examples %s", schema.Examples)
	}
	b, err := DecodeBrief(schema.Examples[0], allowAll, goodFacts())
	if err != nil {
		t.Fatalf("the example brief: %v", err)
	}
	if err := CheckAnswers(b, KindBriefing); err != nil {
		t.Errorf("the example brief as a briefing: %v", err)
	}
	// A crew's schema, when the caller has one, is a draft crew's crew.
	crew := map[string]any{"type": "object", "properties": map[string]any{"name": map[string]any{"type": "string"}}}
	enc, _ = json.Marshal(BriefJSONSchema(crew))
	if !strings.Contains(string(enc), `"crew":{"description":"required; a JSON object, at most 65536 bytes; the body POST /api/crews takes","properties":{"name":{"type":"string"}},"type":"object"}`) {
		t.Errorf("the crew's schema is not the draft crew's: %s", enc)
	}
}

func compact(t *testing.T, raw json.RawMessage) string {
	t.Helper()
	var b bytes.Buffer
	if err := json.Compact(&b, raw); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

// The words are the table's: every kind's line names each of its fields
// with its bounds, and the rule it keeps.
func TestTheWordsAreTheTable(t *testing.T) {
	words := Words()
	all := strings.Join(words, "\n")
	for _, k := range kinds {
		i := slices.IndexFunc(words, func(w string) bool { return strings.HasPrefix(w, k.Kind+": ") })
		if i < 0 {
			t.Errorf("no line for %s", k.Kind)
			continue
		}
		line := words[i]
		for _, f := range k.Fields {
			if !strings.Contains(line, f.Name+" ("+fieldWords(f)+")") {
				t.Errorf("%s's line does not say %s: %s", k.Kind, f.Name, line)
			}
		}
		if !strings.Contains(line, k.Rule) {
			t.Errorf("%s's line does not say its rule", k.Kind)
		}
	}
	for _, f := range refForms {
		if !strings.Contains(all, f.Form) {
			t.Errorf("the words do not say %s", f.Form)
		}
	}
	if !strings.Contains(all, "1 to 10 refs; the refs it rests on") || !strings.Contains(all, "text, at most 200 characters") || !strings.Contains(all, "a check:, command: or op: ref") {
		t.Errorf("the words: %s", all)
	}
}

// Every ref form's example parses as its kind, and the forms are every
// kind ParseRef reads.
func TestEveryRefFormIsParsed(t *testing.T) {
	seen := map[string]bool{}
	for _, f := range refForms {
		r, err := ParseRef(f.Example)
		if err != nil || r.Kind != f.Kind || !strings.HasPrefix(f.Form, f.Kind+":") {
			t.Errorf("%s: %v %v", f.Form, r, err)
		}
		seen[f.Kind] = true
	}
	for _, k := range []string{RefRun, RefTask, RefAttempt, RefCheck, RefCommand, RefEvent, RefMessage, RefSession, RefQuestion, RefTicket, RefComment, RefOp, RefCommit, RefFile, RefBrief} {
		if !seen[k] {
			t.Errorf("no form of %s", k)
		}
		if _, err := ParseRef(k + ":"); err == nil || strings.Contains(err.Error(), "no kind") {
			t.Errorf("%s: %v", k, err)
		}
	}
	if len(seen) != len(refForms) {
		t.Errorf("%d forms of %d kinds", len(refForms), len(seen))
	}
}

// TestARefusalNamesWhatIsAccepted: an unknown kind is refused with every
// kind, an unknown field with the fields its object takes, a value of
// another type with the type its field takes, a ref of no kind with the
// forms (the live check's refusals named only the wrong value).
func TestARefusalNamesWhatIsAccepted(t *testing.T) {
	brief := func(items string) []byte {
		return []byte(`{"schema":"brakeman.brief/1","title":"t","summary":"s","items":[` + items + `]}`)
	}
	fact := `{"kind":"fact/1","text":"x","evidence":["run:` + exRun + `"]}`
	cases := []struct {
		body  []byte
		field string
		says  []string
	}{
		{brief(`{"kind":"fact","text":"x","evidence":["run:` + exRun + `"]}`), "items[0].kind", []string{`no item kind "fact"`, kindNames()}},
		{brief(fact + `,{"text":"x"}`), "items[1].kind", []string{"is required", "fact/1, interpretation/1"}},
		{brief(`{"kind":"fact/1","text":"x","refs":["run:` + exRun + `"]}`), "items[0].refs", []string{"no such field", "a fact/1 item takes kind, text, evidence"}},
		{brief(fact + `,{"kind":"decision/1","about":"ticket:CND-1","alternatives":[{"label":"a","tradeoffs":"x"},{"label":"a","why":"b"}]}`), "items[1].alternatives[1].why", []string{"no such field: an alternatives entry takes label, tradeoffs"}},
		{brief(`{"kind":"fact/1","text":"x","evidence":"run:` + exRun + `"}`), "items[0].evidence", []string{"takes 1 to 10 refs, not a JSON string"}},
		{brief(`"a fact"`), "items[0]", []string{"an item is a JSON object", kindNames()}},
		{[]byte(`{"schema":"brakeman.brief/1","title":"t","summary":"s","refs":[],"items":[` + fact + `]}`), "refs", []string{"no such field: a brief takes schema, title, summary, uncertainty, items"}},
		{[]byte(`{"schema":"brakeman.brief/1","title":"t","summary":"s","items":{}}`), "items", []string{"takes 1 to 100 items"}},
		{brief(`{"kind":"fact/1","text":"x","evidence":["run:` + exRun + `","runs:` + exRun + `"]}`), "items[0].evidence[1]", []string{`no kind "runs"`, "run:<run>, task:<run>/<task>", "brief:<br-n>/<item>"}},
	}
	for _, c := range cases {
		_, err := DecodeBrief(c.body, allowAll, goodFacts())
		var inv *Invalid
		if !errors.As(err, &inv) || inv.Code != CodeInvalidBrief || inv.Field != c.field {
			t.Errorf("%s: %v, want refused at %s", c.body, err, c.field)
			continue
		}
		for _, s := range c.says {
			if !strings.Contains(inv.Why, s) {
				t.Errorf("%s: %q does not say %q", c.body, inv.Why, s)
			}
		}
	}
}

// TestAnEmptyBriefIsRefused: a brief holds at least one item, and a
// briefing at least one fact (the live check kept a brief of its summary
// alone, citing nothing).
func TestAnEmptyBriefIsRefused(t *testing.T) {
	for _, items := range []string{`"items":[]`, `"items":null`, ``} {
		body := `{"schema":"brakeman.brief/1","title":"t","summary":"s"`
		if items != "" {
			body += "," + items
		}
		_, err := DecodeBrief([]byte(body+"}"), allowAll, goodFacts())
		var inv *Invalid
		if !errors.As(err, &inv) || inv.Field != "items" || !strings.Contains(inv.Why, "at least one item") {
			t.Errorf("%s: %v", items, err)
		}
	}
	b := goodBrief(t)
	b.Items = nil
	wantInvalid(t, check(b), "items")

	// A briefing holds a fact; an explanation may hold an interpretation
	// of none, a panel of evidence alone.
	panel := Brief{Schema: BriefSchema, Title: "t", Summary: "s", Items: []Item{{Kind: ItemEvidence, Evidence: &EvidencePanel{Title: "Runs", Refs: []Ref{ref(t, "run:"+runA)}}}}}
	if err := check(panel); err != nil {
		t.Fatal(err)
	}
	if inv := wantInvalid(t, CheckAnswers(panel, KindBriefing), "items"); !strings.Contains(inv.Why, "at least one fact/1") {
		t.Errorf("why: %s", inv.Why)
	}
	for _, kind := range Kinds {
		if kind == KindBriefing {
			continue
		}
		if err := CheckAnswers(panel, kind); err != nil {
			t.Errorf("%s: %v", kind, err)
		}
	}
	if err := CheckAnswers(goodBrief(t), KindBriefing); err != nil {
		t.Errorf("a briefing with a fact: %v", err)
	}
}
