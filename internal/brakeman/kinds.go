package brakeman

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

// The kind table: a brief's fields and each item kind's, with their
// bounds, the rule each kind keeps and an example of it, in one place.
// brief_submit's input schema (BriefJSONSchema) and the guide's briefs section
// (Words) are both made from it, so an agent learns a brief's shape from
// the running server and never from Conductor's source; and
// TestTheKindTableIsWhatCheckBriefTakes holds every field of it, at its
// bounds and one past them, to what DecodeBrief and CheckBrief take, so
// the table cannot drift from the check.

// The types of a field.
const (
	FieldText    = "text"    // a string; Max bytes, or characters when Chars
	FieldName    = "name"    // a string matching Pattern, at most Max characters
	FieldInteger = "integer" // a whole number from Min to Max
	FieldObject  = "object"  // a JSON object, at most Max bytes
	FieldEntry   = "entry"   // a list's object entry, of Fields
	FieldRef     = "ref"     // a ref (RefForms), of a kind in Refs when Refs is set
	FieldIndex   = "index"   // the index of an item of the same brief, from 0
	FieldList    = "list"    // Min to Max entries, each an Of
	FieldItem    = "item"    // an item of a brief: a kind beside its fields
)

// Field is one field of a brief, of an item kind or of a list's entry.
type Field struct {
	Name string `json:"name,omitempty"`
	Type string `json:"type"`
	// Required says the field must be given, and a text not be empty.
	Required bool `json:"required,omitempty"`
	// Min and Max bound a list's entries and an integer's value; Max
	// bounds a text's bytes (characters when Chars), a name's characters
	// and an object's bytes. A list's Max 0 is a count set by another
	// field (a row's cells, one a column).
	Min   int  `json:"min,omitempty"`
	Max   int  `json:"max,omitempty"`
	Chars bool `json:"chars,omitempty"`
	// Pattern is what a name matches.
	Pattern string `json:"pattern,omitempty"`
	// Refs are the kinds a ref takes, none for any.
	Refs []string `json:"refs,omitempty"`
	// Of is a list's entry.
	Of *Field `json:"of,omitempty"`
	// Fields are an entry's own.
	Fields []Field `json:"fields,omitempty"`
	About  string  `json:"about,omitempty"`
}

// KindSpec is one item kind: its fields, the rule it keeps, the
// permission it needs (draft or propose, none for observe) and an
// example item CheckBrief takes in a brief whose first item is a fact.
type KindSpec struct {
	Kind    string          `json:"kind"`
	Needs   string          `json:"needs,omitempty"`
	Rule    string          `json:"rule"`
	Fields  []Field         `json:"fields"`
	Example json.RawMessage `json:"example"`
}

// RefForm is one kind of ref: its form, what it names and an example.
type RefForm struct {
	Kind    string `json:"kind"`
	Form    string `json:"form"`
	Names   string `json:"names"`
	Example string `json:"example"`
}

// The examples' parts: a run, a session, a question, a commit.
const (
	exRun      = "fix-login-3f2a91c0"
	exSession  = "0a1b2c3d4e5f6071"
	exQuestion = "8192a3b4c5d6e7f8"
	exCommit   = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"
)

// refForms is every ref ParseRef reads, in Evidence references' order.
var refForms = []RefForm{
	{RefRun, "run:<run>", "a run", "run:" + exRun},
	{RefTask, "task:<run>/<task>", "a managed task", "task:" + exRun + "/build"},
	{RefAttempt, "attempt:<run>/<task>/<n>", "an attempt, n from 1", "attempt:" + exRun + "/build/3"},
	{RefCheck, "check:<run>/<task>/<n>/<id>/<k>", "a check receipt: attempt n's k-th run of check id", "check:" + exRun + "/build/3/tests/1"},
	{RefCommand, "command:<run>/<command>", "a run command's receipt", "command:" + exRun + "/retry-1"},
	{RefEvent, "event:<run>/<seq>", "a run's ledger event", "event:" + exRun + "/42"},
	{RefMessage, "message:<run>/<seq>", "a message between a run's members", "message:" + exRun + "/7"},
	{RefSession, "session:<id>", "a session, 16 hex digits", "session:" + exSession},
	{RefQuestion, "question:<session>/<question>", "a question a session asks or asked", "question:" + exSession + "/" + exQuestion},
	{RefTicket, "ticket:<KEY-n>", "a Dispatch ticket", "ticket:CND-12"},
	{RefComment, "comment:<KEY-n>/<id>", "a ticket's comment, 16 hex digits", "comment:CND-12/" + exQuestion},
	{RefOp, "op:<actor>/<opId>", "a Dispatch operation's receipt", "op:admin/bk-1"},
	{RefCommit, "commit:<run>/<sha>", "a commit on a run's branches, its full id", "commit:" + exRun + "/" + exCommit},
	{RefFile, "file:<session>/<path>", "a touched file, its path absolute", "file:" + exSession + "//home/me/app/cache.go"},
	{RefBrief, "brief:<br-n>/<item>", "an earlier brief's item, from 0", "brief:br-1/0"},
}

// RefForms is every ref a brief may cite, as Evidence references names
// them. The caller must not change what it holds.
func RefForms() []RefForm { return slices.Clone(refForms) }

// refFormsWords is the forms, one after another, as a refusal names them.
func refFormsWords() string {
	forms := make([]string, len(refForms))
	for i, f := range refForms {
		forms[i] = f.Form
	}
	return strings.Join(forms, ", ")
}

// Field builders, for the table below.
func fText(name string, max int, required bool, about string) Field {
	return Field{Name: name, Type: FieldText, Max: max, Required: required, About: about}
}

func fChars(name string, max int, required bool, about string) Field {
	return Field{Name: name, Type: FieldText, Max: max, Chars: true, Required: required, About: about}
}

func fRefs(name string, min, max int, about string, kinds ...string) Field {
	return Field{Name: name, Type: FieldList, Min: min, Max: max, Required: min > 0, Of: &Field{Type: FieldRef, Required: true, Refs: kinds}, About: about}
}

func fIndexes(name string, min, max int, about string) Field {
	return Field{Name: name, Type: FieldList, Min: min, Max: max, Required: min > 0, Of: &Field{Type: FieldIndex, Required: true}, About: about}
}

func fTexts(name string, max, each int, about string) Field {
	return Field{Name: name, Type: FieldList, Max: max, Of: &Field{Type: FieldText, Max: each, Required: true}, About: about}
}

// revisionPattern is what revision takes.
const revisionPattern = `^[A-Za-z0-9._:-]*$`

// briefFields are a brief's own fields.
var briefFields = []Field{
	{Name: "schema", Type: FieldName, Required: true, Pattern: "^" + strings.ReplaceAll(BriefSchema, ".", `\.`) + "$", Max: len(BriefSchema)},
	fChars("title", MaxTitle, true, "what the brief is about"),
	fText("summary", MaxSummary, true, "your reading, Markdown: marked as your interpretation, not a fact"),
	fText("uncertainty", MaxUncertainty, false, "what you could not read or are unsure of"),
	{Name: "items", Type: FieldList, Min: 1, Max: MaxItems, Required: true, Of: &Field{Type: FieldItem, Required: true},
		About: "the facts, interpretations, proposals, outcomes, decision cards and drafts, at least one; a briefing at least one fact"},
}

// kinds is the table, in Item's order.
var kinds = []KindSpec{
	{Kind: ItemFact, Rule: "Something true, with the evidence it rests on: a fact without evidence is refused.",
		Fields: []Field{
			fText("text", 1<<10, true, "what is true"),
			fRefs("evidence", 1, 10, "the refs it rests on"),
		},
		Example: json.RawMessage(`{"kind":"fact/1","text":"Task build failed its tests check on attempts 1 to 3 and has no attempt left.","evidence":["task:` + exRun + `/build","check:` + exRun + `/build/3/tests/1"]}`)},
	{Kind: ItemInterpretation, Rule: "Your reading of facts of the same brief.",
		Fields: []Field{
			fText("text", 2<<10, true, "your reading"),
			fIndexes("facts", 1, MaxItems, "the indexes, from 0, of the fact/1 items of this brief it reads"),
		},
		Example: json.RawMessage(`{"kind":"interpretation/1","text":"The failures look like the cache, not the change.","facts":[0]}`)},
	{Kind: ItemProposal, Needs: "propose", Rule: "An operation Conductor can run, for a person to do: shown as a control only when the operation's route checks the revision (the guide's operations section, revision).",
		Fields: []Field{
			fText("text", 2<<10, true, "what and why"),
			{Name: "op", Type: FieldName, Required: true, Pattern: operationPattern.String(), Max: 64, About: "an operation the guide's operations section lists"},
			{Name: "target", Type: FieldRef, Required: true, About: "what it acts on"},
			{Name: "args", Type: FieldObject, Max: MaxArgs, About: "the operation's arguments"},
			{Name: "expectedRevision", Type: FieldName, Pattern: revisionPattern, Max: MaxRevision, About: "the target's revision as you read it, above 0: required when the operation's route checks one"},
			fIndexes("facts", 0, MaxItems, "the indexes, from 0, of the fact/1 items of this brief it rests on"),
		},
		Example: json.RawMessage(`{"kind":"proposal/1","text":"Retry build: its last failure was the network test.","op":"task_retry","target":"task:` + exRun + `/build","expectedRevision":"30","facts":[0]}`)},
	{Kind: ItemOutcome, Rule: "A verified outcome: it names a receipt, or it is a fact or an interpretation.",
		Fields: []Field{
			fText("text", 1<<10, true, "what was done and verified"),
			{Name: "receipt", Type: FieldRef, Required: true, Refs: []string{RefCheck, RefCommand, RefOp}, About: "the receipt that verifies it"},
		},
		Example: json.RawMessage(`{"kind":"outcome/1","text":"The retry passed its tests.","receipt":"check:` + exRun + `/build/4/tests/1"}`)},
	{Kind: ItemDecision, Needs: "propose", Rule: "A decision card: Conductor draws its controls from the live record of about, never from these fields.",
		Fields: []Field{
			{Name: "about", Type: FieldRef, Required: true, Refs: []string{RefQuestion, RefTask, RefTicket}, About: "what is to be decided"},
			fText("happened", 2<<10, true, "what work occurred"),
			fText("whyHuman", 1<<10, true, "why a person is needed"),
			fText("question", 500, true, "the precise question"),
			{Name: "alternatives", Type: FieldList, Min: 2, Max: 6, Required: true, Of: &Field{Type: FieldEntry, Required: true, Fields: []Field{
				fChars("label", MaxLabel, true, "the answer"),
				fText("tradeoffs", 1<<10, true, "what it costs and gives"),
			}}, About: "the answers"},
			{Name: "recommendation", Type: FieldInteger, Required: true, Min: -1, Max: 5, About: "an alternative's index, or -1 for none"},
			fText("uncertainty", 1<<10, false, "how sure the recommendation is"),
			fRefs("blocked", 0, 20, "what waits on the answer"),
			fText("authorizes", 1<<10, true, "what answering authorizes"),
			fRefs("evidence", 1, 20, "the refs it rests on"),
			{Name: "subjectRevision", Type: FieldName, Pattern: revisionPattern, Max: MaxRevision, About: "the revision of about you read"},
		},
		Example: json.RawMessage(`{"kind":"decision/1","about":"task:` + exRun + `/build","happened":"build failed its tests three times; each failure is the same network test.","whyHuman":"Its attempts are spent: only a person retries it.","question":"Retry build once more?","alternatives":[{"label":"Retry","tradeoffs":"A fourth attempt; the network test may fail again."},{"label":"Leave it failed","tradeoffs":"The run stays failed until the test is fixed."}],"recommendation":0,"authorizes":"A fourth attempt of build.","evidence":["check:` + exRun + `/build/3/tests/1"],"subjectRevision":"30"}`)},
	{Kind: ItemComparison, Rule: "A table: each row a label and one cell a column.",
		Fields: []Field{
			fChars("title", MaxTitle, true, "what is compared"),
			{Name: "columns", Type: FieldList, Min: 2, Max: 6, Required: true, Of: &Field{Type: FieldText, Max: MaxLabel, Chars: true, Required: true}, About: "the columns' labels"},
			{Name: "rows", Type: FieldList, Min: 1, Max: 20, Required: true, Of: &Field{Type: FieldEntry, Required: true, Fields: []Field{
				fChars("label", MaxTitle, true, "the row's label"),
				{Name: "cells", Type: FieldList, Required: true, Of: &Field{Type: FieldText, Max: 500}, About: "one cell a column"},
			}}, About: "the rows"},
			fRefs("evidence", 0, 20, "the refs it rests on"),
		},
		Example: json.RawMessage(`{"kind":"comparison/1","title":"Two ways on","columns":["Retry","Revert"],"rows":[{"label":"Risk","cells":["the test may fail again","none"]}],"evidence":["run:` + exRun + `"]}`)},
	{Kind: ItemEvidence, Rule: "A panel of evidence.",
		Fields: []Field{
			fChars("title", MaxTitle, true, "what the refs show"),
			fRefs("refs", 1, 50, "the refs"),
		},
		Example: json.RawMessage(`{"kind":"evidence/1","title":"The failing checks","refs":["check:` + exRun + `/build/1/tests/1","check:` + exRun + `/build/2/tests/1"]}`)},
	{Kind: ItemDraftTicket, Needs: "draft", Rule: fmt.Sprintf("A ticket a person may make: at most %d a brief, blocking other draft tickets of the brief in no cycle.", MaxDraftTicket),
		Fields: []Field{
			{Name: "project", Type: FieldName, Required: true, Pattern: projectKeyPattern.String(), Max: 6, About: "a Dispatch project's key"},
			fChars("title", MaxTitle, true, "the ticket's title"),
			fText("description", 16<<10, false, "Markdown"),
			fText("acceptance", 8<<10, false, "the acceptance criteria, Markdown"),
			{Name: "priority", Type: FieldInteger, Min: 0, Max: 4, About: "0 for none, 4 the most urgent"},
			fIndexes("blocks", 0, MaxDraftTicket, "the indexes, from 0, of the other draft_ticket/1 items of this brief it blocks"),
			fRefs("from", 0, 10, "what it was drafted from"),
		},
		Example: json.RawMessage(`{"kind":"draft_ticket/1","project":"CND","title":"Key the cache by locale","description":"The cache key ignores the locale.","acceptance":"A cached page in one locale is never served in another.","priority":2,"from":["comment:CND-12/` + exQuestion + `"]}`)},
	{Kind: ItemDraftCrew, Needs: "draft", Rule: "A crew a person may save (never launched): the crew must pass the checks POST /api/crews makes.",
		Fields: []Field{
			{Name: "crew", Type: FieldObject, Required: true, Max: MaxCrewBody, About: "the body POST /api/crews takes"},
			{Name: "gates", Type: FieldList, Max: 10, Of: &Field{Type: FieldEntry, Required: true, Fields: []Field{
				{Name: "task", Type: FieldName, Required: true, Pattern: taskIDPattern.String(), Max: 40, About: "a task's id"},
				fText("decision", 500, true, "what a person decides before it"),
			}}, About: "the decisions a person makes before a task"},
			fRefs("from", 0, 10, "what it was drafted from"),
		},
		Example: json.RawMessage(`{"kind":"draft_crew/1","crew":{"name":"Locale fix","goal":"Key the cache by locale.","cwd":"/home/me/app","members":[{"name":"writer","agentId":"claude","prompt":"Key the cache by locale and add a test."}]},"from":["ticket:CND-12"]}`)},
	{Kind: ItemHandoff, Needs: "draft", Rule: "A handoff package, from Conductor's records: never an agent's conversation.",
		Fields: []Field{
			fText("objective", 2<<10, true, "what the work is for"),
			fText("constraints", 4<<10, false, "what it must keep"),
			fTexts("decisions", 20, 500, "the decisions made"),
			{Name: "revision", Type: FieldRef, Refs: []string{RefCommit}, About: "the commit the work stands at"},
			fTexts("questions", 20, 500, "the questions open"),
			fRefs("evidence", 0, 20, "the refs it rests on"),
		},
		Example: json.RawMessage(`{"kind":"handoff/1","objective":"Key the cache by locale.","decisions":["keep the cache's file format"],"revision":"commit:` + exRun + `/` + exCommit + `","questions":["Is the locale always set?"],"evidence":["attempt:` + exRun + `/build/3"]}`)},
	{Kind: ItemOverlap, Rule: "Paths changed in two or more trees.",
		Fields: []Field{
			{Name: "paths", Type: FieldList, Min: 1, Max: 50, Required: true, Of: &Field{Type: FieldText, Max: MaxPathRef, Required: true, About: "a path, no control character"}, About: "the paths"},
			fRefs("between", 2, 8, "the attempts or sessions whose trees changed them", RefAttempt, RefSession),
			fRefs("evidence", 0, 20, "the refs it rests on"),
		},
		Example: json.RawMessage(`{"kind":"overlap/1","paths":["cache.go"],"between":["attempt:` + exRun + `/build/3","session:` + exSession + `"]}`)},
}

// ItemKinds is the table of item kinds, in Item's order. The caller must not
// change what it holds.
func ItemKinds() []KindSpec { return slices.Clone(kinds) }

// BriefFields is a brief's own fields. The caller must not change what
// it holds.
func BriefFields() []Field { return slices.Clone(briefFields) }

// kindSpec is kind's entry of the table.
func kindSpec(kind string) (KindSpec, bool) {
	i := slices.IndexFunc(kinds, func(k KindSpec) bool { return k.Kind == kind })
	if i < 0 {
		return KindSpec{}, false
	}
	return kinds[i], true
}

// kindNames is every kind, as a refusal names them.
func kindNames() string {
	names := make([]string, len(kinds))
	for i, k := range kinds {
		names[i] = k.Kind
	}
	return strings.Join(names, ", ")
}

// fieldNames is the fields' names, as a refusal names them.
func fieldNames(fields []Field) string {
	names := make([]string, len(fields))
	for i, f := range fields {
		names[i] = f.Name
	}
	return strings.Join(names, ", ")
}

// ExampleBrief is a brief CheckBrief takes, as the schema and the guide
// give it: two facts, an interpretation of both, a decision card and two
// draft tickets, the first blocking the second.
func ExampleBrief() json.RawMessage {
	return json.RawMessage(`{"schema":"` + BriefSchema + `","title":"Overnight: one task spent, one question open","summary":"build failed the same network test three times and its attempts are spent; the cache question waits for a person.","uncertainty":"The tests' logs were not read.","items":[` +
		string(kinds[0].Example) + `,` +
		`{"kind":"fact/1","text":"The member asked whether to drop the cache; nobody has answered.","evidence":["question:` + exSession + `/` + exQuestion + `"]},` +
		`{"kind":"interpretation/1","text":"The failures look like the cache, not the change.","facts":[0,1]},` +
		string(kinds[4].Example) + `,` +
		`{"kind":"draft_ticket/1","project":"CND","title":"Key the cache by locale","priority":2,"blocks":[5],"from":["question:` + exSession + `/` + exQuestion + `"]},` +
		`{"kind":"draft_ticket/1","project":"CND","title":"Test a cached page in two locales"}]}`)
}

// BriefJSONSchema is a brief's JSON Schema, made from the table: every kind with
// its fields, their bounds and an example, the ref forms, and an example
// brief. crew is the schema of the body POST /api/crews takes (the API's;
// internal/brakeman knows no crew), given as a draft crew's crew; nil
// leaves it an object.
func BriefJSONSchema(crew map[string]any) map[string]any {
	branches := make([]any, len(kinds))
	for i, k := range kinds {
		props := map[string]any{"kind": map[string]any{"type": "string", "const": k.Kind}}
		required := []string{"kind"}
		for _, f := range k.Fields {
			s := fieldSchema(f)
			if f.Name == "crew" && crew != nil {
				s = crewSchema(crew, s)
			}
			props[f.Name] = s
			if f.Required {
				required = append(required, f.Name)
			}
		}
		about := k.Rule
		if k.Needs != "" {
			about += " Needs Brakeman's " + k.Needs + " permission."
		}
		// The example as written, its kind first.
		branches[i] = map[string]any{"type": "object", "description": k.Kind + ": " + about, "properties": props, "required": required, "additionalProperties": false, "examples": []any{k.Example}}
	}
	props := map[string]any{}
	var required []string
	for _, f := range briefFields {
		s := fieldSchema(f)
		if f.Name == "schema" {
			s = map[string]any{"type": "string", "const": BriefSchema}
		}
		if f.Name == "items" {
			s["items"] = map[string]any{"type": "object", "description": "an item: one of the kinds " + kindNames() + ", the kind beside its own fields and no other's", "oneOf": branches}
		}
		props[f.Name] = s
		if f.Required {
			required = append(required, f.Name)
		}
	}
	return map[string]any{"type": "object",
		"description": BriefSchema + ", a brief: " + strings.Join(rulesWords(), " ") + " A ref is a string, one of " + refFormsWords() + ".",
		"properties":  props, "required": required, "additionalProperties": false, "examples": []any{ExampleBrief()}}
}

// crewSchema is the crew's own schema with the field's bound and words.
func crewSchema(crew, field map[string]any) map[string]any {
	out := make(map[string]any, len(crew)+1)
	for k, v := range crew {
		out[k] = v
	}
	out["description"] = field["description"]
	return out
}

// fieldSchema is f's JSON Schema, its words its description.
func fieldSchema(f Field) map[string]any {
	s := map[string]any{}
	switch f.Type {
	case FieldText:
		s["type"] = "string"
		if f.Chars {
			s["maxLength"] = f.Max
		}
		if f.Required {
			s["minLength"] = 1
		}
	case FieldName:
		s["type"], s["pattern"], s["maxLength"] = "string", f.Pattern, f.Max
	case FieldInteger:
		s["type"], s["minimum"], s["maximum"] = "integer", f.Min, f.Max
	case FieldObject:
		s["type"] = "object"
	case FieldEntry:
		props := map[string]any{}
		var required []string
		for _, sub := range f.Fields {
			props[sub.Name] = fieldSchema(sub)
			if sub.Required {
				required = append(required, sub.Name)
			}
		}
		s["type"], s["properties"], s["additionalProperties"] = "object", props, false
		if len(required) > 0 {
			s["required"] = required
		}
	case FieldRef:
		s["type"] = "string"
	case FieldIndex:
		s["type"], s["minimum"] = "integer", 0
	case FieldItem:
		s["type"] = "object"
	case FieldList:
		s["type"], s["items"] = "array", fieldSchema(*f.Of)
		if f.Min > 0 {
			s["minItems"] = f.Min
		}
		if f.Max > 0 {
			s["maxItems"] = f.Max
		}
	}
	if w := fieldWords(f); w != "" {
		s["description"] = w
	}
	return s
}

// rulesWords are the rules every brief keeps, in words.
func rulesWords() []string {
	return []string{
		"A fact names its evidence, an interpretation names facts of the brief, a proposal names an operation, an outcome names a receipt.",
		"Every ref must resolve inside your scope and visibility; a revision you name must be the one you read.",
		fmt.Sprintf("At most %d items, %d distinct refs and %d bytes as kept; a field past its bound is refused, never cut.", MaxItems, MaxBriefRefs, MaxBrief),
	}
}

// Words is the brief's shape in words, as the guide's briefs section
// gives it: the brief's fields, each kind with its fields, bounds and
// rule, and the ref forms; made from the same table as BriefJSONSchema.
func Words() []string {
	out := []string{"A brief, schema " + BriefSchema + ", is one JSON object: " + fieldsWords(briefFields) + "."}
	out = append(out, rulesWords()...)
	out = append(out, "An item is one JSON object: its kind (one of "+kindNames()+") beside that kind's fields, and no other field.")
	for _, k := range kinds {
		line := k.Kind + ": " + fieldsWords(k.Fields) + ". " + k.Rule
		if k.Needs != "" {
			line += " Needs Brakeman's " + k.Needs + " permission."
		}
		out = append(out, line)
	}
	forms := make([]string, len(refForms))
	for i, f := range refForms {
		forms[i] = f.Form + " (" + f.Names + ")"
	}
	return append(out, "A ref is a string, one of: "+strings.Join(forms, ", ")+".")
}

// refKindsWords is ref kinds as a field names them: "check:, command: or op:".
func refKindsWords(refs []string) string {
	k := make([]string, len(refs))
	for i, r := range refs {
		k[i] = r + ":"
	}
	if len(k) == 1 {
		return k[0]
	}
	return strings.Join(k[:len(k)-1], ", ") + " or " + k[len(k)-1]
}

// fieldsWords is fields in words, one after another.
func fieldsWords(fields []Field) string {
	parts := make([]string, len(fields))
	for i, f := range fields {
		parts[i] = f.Name + " (" + fieldWords(f) + ")"
	}
	return strings.Join(parts, ", ")
}

// fieldWords is what f takes, in words: whether it is required, its type
// and its bounds, then what it is.
func fieldWords(f Field) string {
	var w []string
	if f.Required && f.Name != "" {
		w = append(w, "required")
	}
	if t := typeWords(f); t != "" {
		w = append(w, t)
	}
	if f.About != "" && f.Type != FieldEntry {
		w = append(w, f.About)
	}
	return strings.Join(w, "; ")
}

// typeWords is f's type and bounds in words.
func typeWords(f Field) string {
	switch f.Type {
	case FieldText:
		unit := "bytes"
		if f.Chars {
			unit = "characters"
		}
		return fmt.Sprintf("text, at most %d %s", f.Max, unit)
	case FieldName:
		if f.Name == "schema" {
			return fmt.Sprintf("%q", BriefSchema)
		}
		return fmt.Sprintf("text matching %s", f.Pattern)
	case FieldInteger:
		return fmt.Sprintf("a whole number from %d to %d", f.Min, f.Max)
	case FieldObject:
		return fmt.Sprintf("a JSON object, at most %d bytes", f.Max)
	case FieldEntry:
		return "{" + fieldsWords(f.Fields) + "}"
	case FieldRef:
		if len(f.Refs) == 0 {
			return "a ref"
		}
		return "a " + refKindsWords(f.Refs) + " ref"
	case FieldIndex:
		return "an item's index, from 0"
	case FieldItem:
		return "an item"
	case FieldList:
		n := "a list"
		switch {
		case f.Min > 0 && f.Max > 0:
			n = fmt.Sprintf("%d to %d", f.Min, f.Max)
		case f.Max > 0:
			n = fmt.Sprintf("at most %d", f.Max)
		}
		of := typeWords(*f.Of)
		switch f.Of.Type {
		case FieldRef:
			of = strings.TrimPrefix(strings.TrimPrefix(of, "a "), "an ") + "s"
		case FieldIndex:
			of = "indexes"
		case FieldItem:
			of = "items"
		case FieldText:
			of = "texts, each " + strings.TrimPrefix(of, "text, ")
		case FieldEntry:
			of = "entries, each " + of
		}
		if n == "a list" {
			return "a list of " + of
		}
		return n + " " + of
	}
	return ""
}
