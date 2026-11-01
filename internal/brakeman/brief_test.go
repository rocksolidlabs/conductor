package brakeman

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func draftTicket(t *testing.T, project, title string, blocks ...int) Item {
	return Item{Kind: ItemDraftTicket, DraftTicket: &DraftTicket{Project: project, Title: title, Priority: 2, Blocks: blocks, From: []Ref{ref(t, "ticket:CND-1")}}}
}

func draftCrew() Item {
	return Item{Kind: ItemDraftCrew, DraftCrew: &DraftCrew{Crew: json.RawMessage(`{"name":"Parser","cwd":"/src/app","members":[]}`), Gates: []Gate{{Task: "design", Decision: "approve the design"}}}}
}

func handoff(t *testing.T) Item {
	rev := ref(t, "commit:"+runA+"/"+strings.Repeat("a", 40))
	return Item{Kind: ItemHandoff, Handoff: &Handoff{Objective: "Finish the parser", Decisions: []string{"keep the old API"}, Revision: &rev, Evidence: []Ref{ref(t, "task:"+runA+"/build")}}}
}

func proposal(t *testing.T) Item {
	return Item{Kind: ItemProposal, Proposal: &Proposal{Text: "Retry the build.", Op: "task_retry", Target: ref(t, "task:"+runA+"/build"), Args: json.RawMessage(`{"reason":"flake"}`), ExpectedRevision: "7", Facts: []int{0}}}
}

func decision(t *testing.T) Item {
	rec := 0
	return Item{Kind: ItemDecision, Decision: &Decision{
		About:          ref(t, "question:"+sessA+"/"+questA),
		Happened:       "The member asked whether to drop the cache.",
		WhyHuman:       "It deletes data.",
		Question:       "Drop the cache?",
		Alternatives:   []Alternative{{Label: "Yes", Tradeoffs: "Faster, loses the cache."}, {Label: "No", Tradeoffs: "Slower."}},
		Recommendation: &rec,
		Authorizes:     "The member deletes ~/.cache/app.",
		Evidence:       []Ref{ref(t, "session:"+sessA)},
	}}
}

func comparison(t *testing.T) Item {
	return Item{Kind: ItemComparison, Comparison: &Comparison{Title: "Options", Columns: []string{"Keep", "Drop"}, Rows: []Row{{Label: "Cost", Cells: []string{"low", "high"}}}, Evidence: []Ref{ref(t, "run:"+runA)}}}
}

func overlap(t *testing.T) Item {
	return Item{Kind: ItemOverlap, Overlap: &Overlap{Paths: []string{"main.go"}, Between: []Ref{ref(t, "session:"+sessA), ref(t, "attempt:"+runA+"/build/1")}}}
}

// columns gives the comparison n columns and each row a cell a column.
func columns(c *Comparison, n int) {
	c.Columns = make([]string, n)
	for i := range c.Columns {
		c.Columns[i] = fmt.Sprintf("c%d", i)
	}
	for j := range c.Rows {
		c.Rows[j].Cells = make([]string, n)
	}
}

// strs is n texts.
func strs(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("t%d", i)
	}
	return out
}

// sessions is n distinct session refs.
func sessions(t *testing.T, n int) []Ref {
	out := make([]Ref, n)
	for i := range out {
		out[i] = ref(t, fmt.Sprintf("session:%016x", i+1))
	}
	return out
}

// check runs CheckBrief with every permission and goodFacts.
func check(b Brief) error { return CheckBrief(&b, allowAll, goodFacts()) }

// TestBriefBounds holds each field and count at its bound and one past,
// and the whole at MaxBrief as it is kept.
func TestBriefBounds(t *testing.T) {
	type bound struct {
		field    string
		at, past func(*Brief)
	}
	evidence := func(n int) []Ref {
		out := make([]Ref, n)
		for i := range out {
			out[i] = ref(t, fmt.Sprintf("event:%s/%d", runA, i+1))
		}
		return out
	}
	// add appends the item mk makes, changed by mut, as items[1].
	add := func(mk func() Item, mut func(*Item)) func(*Brief) {
		return func(b *Brief) {
			it := mk()
			mut(&it)
			b.Items = append(b.Items, it)
		}
	}
	// at and past are one bound of the item mk makes: set(it, n) at n and
	// at n+1.
	item := func(field string, mk func() Item, n int, set func(it *Item, n int)) bound {
		return bound{field, add(mk, func(it *Item) { set(it, n) }), add(mk, func(it *Item) { set(it, n+1) })}
	}
	interp := func() Item {
		return Item{Kind: ItemInterpretation, Interpretation: &Interpretation{Text: "It looks stuck.", Facts: []int{0}}}
	}
	outcome := func() Item {
		return Item{Kind: ItemOutcome, Outcome: &Outcome{Text: "The retry passed.", Receipt: ref(t, "op:admin/op-9")}}
	}
	panel := func() Item {
		return Item{Kind: ItemEvidence, Evidence: &EvidencePanel{Title: "Runs", Refs: evidence(1)}}
	}
	prop := func() Item { return proposal(t) }
	dec := func() Item { return decision(t) }
	cmp := func() Item { return comparison(t) }
	ticket := func() Item { return draftTicket(t, "CND", "T") }
	hand := func() Item { return handoff(t) }
	over := func() Item { return overlap(t) }
	crewBody := func(n int) json.RawMessage {
		head, tail := `{"name":"Parser","cwd":"/src/app","members":[],"note":"`, `"}`
		return json.RawMessage(head + pad("n", n-len(head)-len(tail)) + tail)
	}
	cases := []bound{
		item("items[1].text", interp, 2<<10, func(it *Item, n int) { it.Interpretation.Text = pad("i", n) }),
		item("items[1].text", prop, 2<<10, func(it *Item, n int) { it.Proposal.Text = pad("p", n) }),
		item("items[1].expectedRevision", prop, MaxRevision, func(it *Item, n int) { it.Proposal.ExpectedRevision = pad("7", n) }),
		item("items[1].text", outcome, 1<<10, func(it *Item, n int) { it.Outcome.Text = pad("o", n) }),
		item("items[1].happened", dec, 2<<10, func(it *Item, n int) { it.Decision.Happened = pad("h", n) }),
		item("items[1].whyHuman", dec, 1<<10, func(it *Item, n int) { it.Decision.WhyHuman = pad("w", n) }),
		item("items[1].uncertainty", dec, 1<<10, func(it *Item, n int) { it.Decision.Uncertainty = pad("u", n) }),
		item("items[1].authorizes", dec, 1<<10, func(it *Item, n int) { it.Decision.Authorizes = pad("a", n) }),
		item("items[1].alternatives[0].tradeoffs", dec, 1<<10, func(it *Item, n int) { it.Decision.Alternatives[0].Tradeoffs = pad("t", n) }),
		item("items[1].blocked", dec, 20, func(it *Item, n int) { it.Decision.Blocked = evidence(n) }),
		item("items[1].title", cmp, MaxTitle, func(it *Item, n int) { it.Comparison.Title = strings.Repeat("é", n) }),
		item("items[1].columns", cmp, 6, func(it *Item, n int) { columns(it.Comparison, n) }),
		item("items[1].columns[0]", cmp, MaxLabel, func(it *Item, n int) { it.Comparison.Columns[0] = strings.Repeat("ü", n) }),
		item("items[1].rows", cmp, 20, func(it *Item, n int) {
			for len(it.Comparison.Rows) < n {
				it.Comparison.Rows = append(it.Comparison.Rows, Row{Label: "more", Cells: []string{"a", "b"}})
			}
		}),
		item("items[1].rows[0].label", cmp, MaxTitle, func(it *Item, n int) { it.Comparison.Rows[0].Label = strings.Repeat("é", n) }),
		item("items[1].rows[0].cells[1]", cmp, 500, func(it *Item, n int) { it.Comparison.Rows[0].Cells[1] = pad("c", n) }),
		item("items[1].evidence", cmp, 20, func(it *Item, n int) { it.Comparison.Evidence = evidence(n) }),
		item("items[1].title", panel, MaxTitle, func(it *Item, n int) { it.Evidence.Title = strings.Repeat("é", n) }),
		item("items[1].title", ticket, MaxTitle, func(it *Item, n int) { it.DraftTicket.Title = strings.Repeat("é", n) }),
		item("items[1].acceptance", ticket, 8<<10, func(it *Item, n int) { it.DraftTicket.Acceptance = pad("a", n) }),
		item("items[1].from", ticket, 10, func(it *Item, n int) { it.DraftTicket.From = evidence(n) }),
		item("items[1].crew", draftCrew, MaxCrewBody, func(it *Item, n int) { it.DraftCrew.Crew = crewBody(n) }),
		item("items[1].gates", draftCrew, 10, func(it *Item, n int) {
			for len(it.DraftCrew.Gates) < n {
				it.DraftCrew.Gates = append(it.DraftCrew.Gates, Gate{Task: "build", Decision: "approve"})
			}
		}),
		item("items[1].gates[0].decision", draftCrew, 500, func(it *Item, n int) { it.DraftCrew.Gates[0].Decision = pad("d", n) }),
		item("items[1].from", draftCrew, 10, func(it *Item, n int) { it.DraftCrew.From = evidence(n) }),
		item("items[1].objective", hand, 2<<10, func(it *Item, n int) { it.Handoff.Objective = pad("o", n) }),
		item("items[1].constraints", hand, 4<<10, func(it *Item, n int) { it.Handoff.Constraints = pad("c", n) }),
		item("items[1].decisions", hand, 20, func(it *Item, n int) { it.Handoff.Decisions = strs(n) }),
		item("items[1].decisions[0]", hand, 500, func(it *Item, n int) { it.Handoff.Decisions[0] = pad("d", n) }),
		item("items[1].questions", hand, 20, func(it *Item, n int) { it.Handoff.Questions = strs(n) }),
		item("items[1].questions[0]", hand, 500, func(it *Item, n int) { it.Handoff.Questions = []string{pad("q", n)} }),
		item("items[1].evidence", hand, 20, func(it *Item, n int) { it.Handoff.Evidence = evidence(n) }),
		item("items[1].between", over, 8, func(it *Item, n int) { it.Overlap.Between = sessions(t, n) }),
		item("items[1].paths[0]", over, MaxPathRef, func(it *Item, n int) { it.Overlap.Paths[0] = pad("p", n) }),
		item("items[1].evidence", over, 20, func(it *Item, n int) { it.Overlap.Evidence = evidence(n) }),
		{"title", func(b *Brief) { b.Title = strings.Repeat("é", MaxTitle) }, func(b *Brief) { b.Title = strings.Repeat("é", MaxTitle+1) }},
		{"summary", func(b *Brief) { b.Summary = pad("s", MaxSummary) }, func(b *Brief) { b.Summary = pad("s", MaxSummary+1) }},
		{"uncertainty", func(b *Brief) { b.Uncertainty = pad("u", MaxUncertainty) }, func(b *Brief) { b.Uncertainty = pad("u", MaxUncertainty+1) }},
		{"items", func(b *Brief) {
			for len(b.Items) < MaxItems {
				b.Items = append(b.Items, factItem(t, "more"))
			}
		}, func(b *Brief) {
			for len(b.Items) < MaxItems+1 {
				b.Items = append(b.Items, factItem(t, "more"))
			}
		}},
		{"items[0].text", func(b *Brief) { b.Items[0].Fact.Text = pad("f", 1<<10) }, func(b *Brief) { b.Items[0].Fact.Text = pad("f", 1<<10+1) }},
		{"items[0].evidence", func(b *Brief) { b.Items[0].Fact.Evidence = evidence(10) }, func(b *Brief) { b.Items[0].Fact.Evidence = evidence(11) }},
		{"items[1].alternatives", func(b *Brief) {
			b.Items = append(b.Items, decision(t))
			d := b.Items[1].Decision
			for len(d.Alternatives) < 6 {
				d.Alternatives = append(d.Alternatives, Alternative{Label: "more", Tradeoffs: "t"})
			}
		}, func(b *Brief) {
			b.Items = append(b.Items, decision(t))
			d := b.Items[1].Decision
			for len(d.Alternatives) < 7 {
				d.Alternatives = append(d.Alternatives, Alternative{Label: "more", Tradeoffs: "t"})
			}
		}},
		{"items[1].alternatives[0].label", func(b *Brief) {
			b.Items = append(b.Items, decision(t))
			b.Items[1].Decision.Alternatives[0].Label = strings.Repeat("ü", MaxLabel)
		}, func(b *Brief) {
			b.Items = append(b.Items, decision(t))
			b.Items[1].Decision.Alternatives[0].Label = strings.Repeat("ü", MaxLabel+1)
		}},
		{"items[1].question", func(b *Brief) {
			b.Items = append(b.Items, decision(t))
			b.Items[1].Decision.Question = pad("q", 500)
		}, func(b *Brief) {
			b.Items = append(b.Items, decision(t))
			b.Items[1].Decision.Question = pad("q", 501)
		}},
		{"items[1].evidence", func(b *Brief) {
			b.Items = append(b.Items, decision(t))
			b.Items[1].Decision.Evidence = evidence(20)
		}, func(b *Brief) {
			b.Items = append(b.Items, decision(t))
			b.Items[1].Decision.Evidence = evidence(21)
		}},
		{"items[1].refs", func(b *Brief) {
			b.Items = append(b.Items, Item{Kind: ItemEvidence, Evidence: &EvidencePanel{Title: "Runs", Refs: evidence(50)}})
		}, func(b *Brief) {
			b.Items = append(b.Items, Item{Kind: ItemEvidence, Evidence: &EvidencePanel{Title: "Runs", Refs: evidence(51)}})
		}},
		{"items[1].args", func(b *Brief) {
			b.Items = append(b.Items, proposal(t))
			b.Items[1].Proposal.Args = json.RawMessage(`{"a":"` + pad("x", MaxArgs-8) + `"}`)
		}, func(b *Brief) {
			b.Items = append(b.Items, proposal(t))
			b.Items[1].Proposal.Args = json.RawMessage(`{"a":"` + pad("x", MaxArgs-7) + `"}`)
		}},
		{"items[1].description", func(b *Brief) {
			b.Items = append(b.Items, draftTicket(t, "CND", "T"))
			b.Items[1].DraftTicket.Description = pad("d", 16<<10)
		}, func(b *Brief) {
			b.Items = append(b.Items, draftTicket(t, "CND", "T"))
			b.Items[1].DraftTicket.Description = pad("d", 16<<10+1)
		}},
		{"items[1].priority", func(b *Brief) {
			b.Items = append(b.Items, draftTicket(t, "CND", "T"))
			b.Items[1].DraftTicket.Priority = 4
		}, func(b *Brief) {
			b.Items = append(b.Items, draftTicket(t, "CND", "T"))
			b.Items[1].DraftTicket.Priority = 5
		}},
		{"items", func(b *Brief) {
			for range MaxDraftTicket {
				b.Items = append(b.Items, draftTicket(t, "CND", "T"))
			}
		}, func(b *Brief) {
			for range MaxDraftTicket + 1 {
				b.Items = append(b.Items, draftTicket(t, "CND", "T"))
			}
		}},
		{"items[1].paths", func(b *Brief) {
			b.Items = append(b.Items, Item{Kind: ItemOverlap, Overlap: &Overlap{Paths: make([]string, 50), Between: []Ref{ref(t, "session:"+sessA), ref(t, "attempt:"+runA+"/build/1")}}})
			for i := range b.Items[1].Overlap.Paths {
				b.Items[1].Overlap.Paths[i] = fmt.Sprintf("p%d.go", i)
			}
		}, func(b *Brief) {
			b.Items = append(b.Items, Item{Kind: ItemOverlap, Overlap: &Overlap{Paths: make([]string, 51), Between: []Ref{ref(t, "session:"+sessA), ref(t, "attempt:"+runA+"/build/1")}}})
			for i := range b.Items[1].Overlap.Paths {
				b.Items[1].Overlap.Paths[i] = fmt.Sprintf("p%d.go", i)
			}
		}},
		// Distinct refs across the brief: ten panels of fifty, then one more.
		{"items", func(b *Brief) {
			for p := range MaxBriefRefs / 50 {
				refs := make([]Ref, 50)
				for i := range refs {
					refs[i] = ref(t, fmt.Sprintf("event:%s/%d", runA, p*50+i+1))
				}
				b.Items = append(b.Items, Item{Kind: ItemEvidence, Evidence: &EvidencePanel{Title: "p", Refs: refs}})
			}
			b.Items[0].Fact.Evidence = []Ref{ref(t, "event:"+runA+"/1")}
		}, func(b *Brief) {
			for p := range MaxBriefRefs / 50 {
				refs := make([]Ref, 50)
				for i := range refs {
					refs[i] = ref(t, fmt.Sprintf("event:%s/%d", runA, p*50+i+1))
				}
				b.Items = append(b.Items, Item{Kind: ItemEvidence, Evidence: &EvidencePanel{Title: "p", Refs: refs}})
			}
		}},
	}
	for _, c := range cases {
		at, past := goodBrief(t), goodBrief(t)
		c.at(&at)
		c.past(&past)
		if err := check(at); err != nil {
			t.Errorf("%s at its bound: %v", c.field, err)
		}
		inv := wantInvalid(t, check(past), c.field)
		if inv.Code != CodeInvalidBrief {
			t.Errorf("%s: code %s", c.field, inv.Code)
		}
	}
	// The whole, as kept: ten draft tickets of 24 KiB each fit, eleven do not.
	whole := func(n int) Brief {
		b := goodBrief(t)
		for range n {
			it := draftTicket(t, "CND", "Big")
			it.DraftTicket.Description, it.DraftTicket.Acceptance = pad("d", 16<<10), pad("a", 8<<10)
			b.Items = append(b.Items, it)
		}
		return b
	}
	if err := check(whole(10)); err != nil {
		t.Errorf("a brief under %d bytes: %v", MaxBrief, err)
	}
	if err := check(whole(11)); !errors.Is(err, ErrBound) {
		t.Errorf("a brief over %d bytes: %v", MaxBrief, err)
	}
	b := whole(10)
	enc, _ := encode(b)
	if len(enc) > MaxBrief || len(enc) < MaxBrief-30<<10 {
		t.Errorf("the brief at its bound is %d bytes", len(enc))
	}
}

// TestAFactWithoutEvidenceIsRefused.
func TestAFactWithoutEvidenceIsRefused(t *testing.T) {
	b := goodBrief(t)
	b.Items[0].Fact.Evidence = nil
	if inv := wantInvalid(t, check(b), "items[0].evidence"); !strings.Contains(inv.Why, "names its evidence") {
		t.Errorf("why: %s", inv.Why)
	}
	body := `{"schema":"brakeman.brief/1","title":"t","summary":"s","items":[{"kind":"fact/1","text":"no proof"}]}`
	_, err := DecodeBrief([]byte(body), allowAll, goodFacts())
	wantInvalid(t, err, "items[0].evidence")
}

// TestAnInterpretationReadsFactsOfItsBrief: an interpretation names at
// least one fact, and each is a fact item of the same brief.
func TestAnInterpretationReadsFactsOfItsBrief(t *testing.T) {
	interp := func(facts ...int) Brief {
		b := goodBrief(t)
		b.Items = append(b.Items, decision(t), Item{Kind: ItemInterpretation, Interpretation: &Interpretation{Text: "It looks stuck.", Facts: facts}})
		return b
	}
	if err := check(interp(0)); err != nil {
		t.Fatalf("an interpretation of fact 0: %v", err)
	}
	wantInvalid(t, check(interp()), "items[2].facts")
	wantInvalid(t, check(interp(1)), "items[2].facts[0]") // a decision, not a fact
	wantInvalid(t, check(interp(2)), "items[2].facts[0]") // itself
	wantInvalid(t, check(interp(0, 3)), "items[2].facts[1]")
	wantInvalid(t, check(interp(-1)), "items[2].facts[0]")
}

// TestAnOutcomeNamesAReceipt: a verified outcome names a check, a command
// or a Dispatch receipt; anything else is a fact or an interpretation.
func TestAnOutcomeNamesAReceipt(t *testing.T) {
	for _, r := range []string{"check:" + runA + "/build/1/vet/1", "command:" + runA + "/retry-1", "op:admin/op-9"} {
		b := goodBrief(t)
		b.Items = append(b.Items, Item{Kind: ItemOutcome, Outcome: &Outcome{Text: "The retry passed.", Receipt: ref(t, r)}})
		if err := check(b); err != nil {
			t.Errorf("an outcome on %s: %v", r, err)
		}
	}
	for _, r := range []string{"run:" + runA, "task:" + runA + "/build", "ticket:CND-1"} {
		b := goodBrief(t)
		b.Items = append(b.Items, Item{Kind: ItemOutcome, Outcome: &Outcome{Text: "Done.", Receipt: ref(t, r)}})
		wantInvalid(t, check(b), "items[1].receipt")
	}
	b := goodBrief(t)
	b.Items = append(b.Items, Item{Kind: ItemOutcome, Outcome: &Outcome{Text: "Done."}})
	if inv := wantInvalid(t, check(b), "items[1].receipt"); !strings.Contains(inv.Why, "names a receipt") {
		t.Errorf("why: %s", inv.Why)
	}
}

// TestADecisionCardIsWhole: each part of a card is required.
func TestADecisionCardIsWhole(t *testing.T) {
	cases := map[string]func(*Decision){
		"items[1].about":                     func(d *Decision) { d.About = Ref{} },
		"items[1].happened":                  func(d *Decision) { d.Happened = "" },
		"items[1].whyHuman":                  func(d *Decision) { d.WhyHuman = " " },
		"items[1].question":                  func(d *Decision) { d.Question = "" },
		"items[1].alternatives":              func(d *Decision) { d.Alternatives = d.Alternatives[:1] },
		"items[1].recommendation":            func(d *Decision) { d.Recommendation = nil },
		"items[1].authorizes":                func(d *Decision) { d.Authorizes = "" },
		"items[1].evidence":                  func(d *Decision) { d.Evidence = nil },
		"items[1].alternatives[1].tradeoffs": func(d *Decision) { d.Alternatives[1].Tradeoffs = "" },
		"items[1].subjectRevision":           func(d *Decision) { d.SubjectRevision = "7 or so" },
	}
	for field, mut := range cases {
		b := goodBrief(t)
		it := decision(t)
		mut(it.Decision)
		b.Items = append(b.Items, it)
		wantInvalid(t, check(b), field)
	}
	for _, rec := range []int{-2, 2} {
		b := goodBrief(t)
		it := decision(t)
		it.Decision.Recommendation = &rec
		b.Items = append(b.Items, it)
		wantInvalid(t, check(b), "items[1].recommendation")
	}
	none := -1
	b := goodBrief(t)
	it := decision(t)
	it.Decision.Recommendation, it.Decision.About = &none, ref(t, "ticket:CND-3")
	b.Items = append(b.Items, it)
	if err := check(b); err != nil {
		t.Errorf("a card with no recommendation about a ticket: %v", err)
	}
	b = goodBrief(t)
	it = decision(t)
	it.Decision.About = ref(t, "run:"+runA)
	b.Items = append(b.Items, it)
	wantInvalid(t, check(b), "items[1].about")
	// A recommendation left out of the JSON is not the first alternative.
	body := `{"schema":"brakeman.brief/1","title":"t","summary":"s","items":[{"kind":"decision/1","about":"ticket:CND-1","happened":"h","whyHuman":"w","question":"q?","alternatives":[{"label":"a","tradeoffs":"x"},{"label":"b","tradeoffs":"y"}],"authorizes":"z","evidence":["ticket:CND-1"]}]}`
	_, err := DecodeBrief([]byte(body), allowAll, goodFacts())
	wantInvalid(t, err, "items[0].recommendation")
}

// TestDraftTicketsBlockWithoutACycle: a draft ticket blocks other draft
// tickets of its brief, never itself, never in a cycle.
func TestDraftTicketsBlockWithoutACycle(t *testing.T) {
	brief := func(items ...Item) Brief {
		b := goodBrief(t)
		b.Items = append(b.Items, items...)
		return b
	}
	// 1 blocks 2 blocks 3: a chain.
	if err := check(brief(draftTicket(t, "CND", "A", 2), draftTicket(t, "CND", "B", 3), draftTicket(t, "CND", "C"))); err != nil {
		t.Fatalf("a chain: %v", err)
	}
	wantInvalid(t, check(brief(draftTicket(t, "CND", "A", 2), draftTicket(t, "CND", "B", 1))), "items[1].blocks")
	wantInvalid(t, check(brief(draftTicket(t, "CND", "A", 2), draftTicket(t, "CND", "B", 3), draftTicket(t, "CND", "C", 1))), "items[1].blocks")
	wantInvalid(t, check(brief(draftTicket(t, "CND", "A", 1))), "items[1].blocks[0]")
	wantInvalid(t, check(brief(draftTicket(t, "CND", "A", 0))), "items[1].blocks[0]")
	wantInvalid(t, check(brief(draftTicket(t, "CND", "A", 2, 2), draftTicket(t, "CND", "B"))), "items[1].blocks[1]")
	wantInvalid(t, check(brief(draftTicket(t, "cnd", "A"))), "items[1].project")
}

// TestADraftCrewIsCheckedByFacts: a draft crew's body is checked as POST
// /api/crews checks it, through Facts, and refused with its words.
func TestADraftCrewIsCheckedByFacts(t *testing.T) {
	b := goodBrief(t)
	b.Items = append(b.Items, draftCrew())
	f := goodFacts()
	if err := CheckBrief(&b, allowAll, f); err != nil {
		t.Fatal(err)
	}
	if len(f.crews) != 1 || !strings.Contains(f.crews[0], `"name":"Parser"`) {
		t.Fatalf("CheckCrew was given %q", f.crews)
	}
	f = goodFacts()
	f.crewErr = errors.New(`task "build": too many checks (at most 8)`)
	b = goodBrief(t)
	b.Items = append(b.Items, draftCrew())
	if inv := wantInvalid(t, CheckBrief(&b, allowAll, f), "items[1].crew"); !strings.Contains(inv.Why, "too many checks") {
		t.Errorf("why: %s", inv.Why)
	}
	b = goodBrief(t)
	it := draftCrew()
	it.DraftCrew.Crew = json.RawMessage(`[]`)
	b.Items = append(b.Items, it)
	wantInvalid(t, check(b), "items[1].crew")
	b = goodBrief(t)
	it = draftCrew()
	it.DraftCrew.Gates[0].Task = "Not A Task"
	b.Items = append(b.Items, it)
	wantInvalid(t, check(b), "items[1].gates[0].task")
}

// TestTextIsCleanedBeforeItIsKept: control characters but newline and
// tab are gone from every text, in the brief that is kept.
func TestTextIsCleanedBeforeItIsKept(t *testing.T) {
	f := newFixture(t)
	r := f.start("op-1")
	b := goodBrief(t)
	b.Title = "Over\x1b[31mnight\r"
	b.Summary = "line one\nline\ttwo\x07\x00\u009b"
	b.Items[0].Fact.Text = "\x08fact\x7f"
	b.Items = append(b.Items, decision(t))
	b.Items[1].Decision.Alternatives[0].Label = "Y\x1bes"
	if err := CheckBrief(&b, allowAll, goodFacts()); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.KeepBrief(r.ID, Kept{OpID: "b-1", Brief: b, Refs: nil}); err != nil {
		t.Fatal(err)
	}
	f.reopen()
	kept, _, err := f.s.Brief(r.ID)
	if err != nil {
		t.Fatal(err)
	}
	for name, got := range map[string]string{
		"title": kept.Title, "summary": kept.Summary, "fact": kept.Items[0].Fact.Text, "label": kept.Items[1].Decision.Alternatives[0].Label,
	} {
		if strings.ContainsFunc(got, isControl) {
			t.Errorf("%s kept a control character: %q", name, got)
		}
	}
	if kept.Title != "Over[31mnight" || kept.Summary != "line one\nline\ttwo" || kept.Items[0].Fact.Text != "fact" || kept.Items[1].Decision.Alternatives[0].Label != "Yes" {
		t.Errorf("kept %q %q %q %q", kept.Title, kept.Summary, kept.Items[0].Fact.Text, kept.Items[1].Decision.Alternatives[0].Label)
	}
}

// TestTextPastByte500Survives: a summary of 4 KiB and a description of 16
// KiB are kept whole, which session.CleanMessage would have cut at 500.
func TestTextPastByte500Survives(t *testing.T) {
	f := newFixture(t)
	r := f.start("op-1")
	b := goodBrief(t)
	b.Summary = pad("s", MaxSummary-1) + "!"
	it := draftTicket(t, "CND", "Long")
	it.DraftTicket.Description = pad("d", 16<<10-1) + "!"
	b.Items = append(b.Items, it)
	if err := check(b); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.KeepBrief(r.ID, Kept{OpID: "b-1", Brief: b}); err != nil {
		t.Fatal(err)
	}
	kept, _, err := f.s.Brief(r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(kept.Summary) != MaxSummary || !strings.HasSuffix(kept.Summary, "!") {
		t.Errorf("summary kept at %d bytes", len(kept.Summary))
	}
	if d := kept.Items[1].DraftTicket.Description; len(d) != 16<<10 || !strings.HasSuffix(d, "!") {
		t.Errorf("description kept at %d bytes", len(d))
	}
}

// TestAFieldPastItsBoundIsRefusedNotCut: the refusal names the field and
// its bound, and the brief is as it was given.
func TestAFieldPastItsBoundIsRefusedNotCut(t *testing.T) {
	b := goodBrief(t)
	b.Summary = pad("s", MaxSummary+1)
	inv := wantInvalid(t, check(b), "summary")
	if !strings.Contains(inv.Why, fmt.Sprint(MaxSummary)) {
		t.Errorf("the refusal names no bound: %s", inv)
	}
	if len(b.Summary) != MaxSummary+1 {
		t.Errorf("the summary was cut to %d", len(b.Summary))
	}
	b = goodBrief(t)
	b.Items[0].Fact.Text = pad("f", 1<<10+1)
	if inv := wantInvalid(t, CheckBrief(&b, allowAll, goodFacts()), "items[0].text"); !strings.Contains(inv.Why, "1024") {
		t.Errorf("the refusal names no bound: %s", inv)
	}
	if len(b.Items[0].Fact.Text) != 1<<10+1 {
		t.Errorf("the text was cut to %d", len(b.Items[0].Fact.Text))
	}
}

// TestABriefCarryingAsOfIsRefused: what a brief covers is the server's
// record, so an asOf is refused; so is a field of another item's kind.
func TestABriefCarryingAsOfIsRefused(t *testing.T) {
	good := `{"schema":"brakeman.brief/1","title":"t","summary":"s","items":[{"kind":"fact/1","text":"x","evidence":["run:` + runA + `"]}]}`
	if _, err := DecodeBrief([]byte(good), allowAll, goodFacts()); err != nil {
		t.Fatal(err)
	}
	asOf := `{"schema":"brakeman.brief/1","asOf":"2026-11-01T00:00:00Z","title":"t","summary":"s","items":[]}`
	_, err := DecodeBrief([]byte(asOf), allowAll, goodFacts())
	wantInvalid(t, err, "asOf")
	for _, body := range []string{
		`{"schema":"brakeman.brief/1","title":"t","summary":"s","items":[{"kind":"fact/1","text":"x","evidence":["run:` + runA + `"],"op":"task_retry"}]}`,
		`{"schema":"brakeman.brief/1","title":"t","summary":"s","items":[{"kind":"fact/2","text":"x"}]}`,
		`{"schema":"brakeman.brief/1","title":"t","summary":"s","items":[],"extra":1}`,
		`{"schema":"brakeman.brief/2","title":"t","summary":"s","items":[]}`,
		`{"schema":"brakeman.brief/1","title":"t","summary":"s","items":[{"kind":"fact/1","text":"x","evidence":["run:nope"]}]}`,
	} {
		if _, err := DecodeBrief([]byte(body), allowAll, goodFacts()); err == nil {
			t.Errorf("taken: %s", body)
		}
	}
	// An item goes back to JSON as one object, its kind first.
	b, err := DecodeBrief([]byte(good), allowAll, goodFacts())
	if err != nil {
		t.Fatal(err)
	}
	out, err := json.Marshal(b.Items[0])
	if err != nil || string(out) != `{"kind":"fact/1","text":"x","evidence":["run:`+runA+`"]}` {
		t.Errorf("the item is written %s %v", out, err)
	}
}

// everyKind is a brief with one item of each kind, the proposal's args and
// the crew's body holding <, > and &.
func everyKind(t *testing.T) Brief {
	b := goodBrief(t)
	prop := proposal(t)
	prop.Proposal.Args = json.RawMessage(`{"reason":"<a> & <b>"}`)
	crew := draftCrew()
	crew.DraftCrew.Crew = json.RawMessage(`{"name":"P<1>&","cwd":"/src/app","members":[]}`)
	b.Items = append(b.Items,
		Item{Kind: ItemInterpretation, Interpretation: &Interpretation{Text: "It looks stuck.", Facts: []int{0}}},
		prop,
		Item{Kind: ItemOutcome, Outcome: &Outcome{Text: "The retry passed.", Receipt: ref(t, "check:"+runA+"/build/1/vet/1")}},
		decision(t),
		comparison(t),
		Item{Kind: ItemEvidence, Evidence: &EvidencePanel{Title: "Runs", Refs: []Ref{ref(t, "run:"+runA)}}},
		draftTicket(t, "CND", "Fix it"),
		crew,
		handoff(t),
		overlap(t),
	)
	return b
}

// TestEveryItemKindRoundTrips: a brief of every kind, written as it is
// kept, decodes through DecodeBrief and writes back byte for byte.
func TestEveryItemKindRoundTrips(t *testing.T) {
	b := everyKind(t)
	if err := check(b); err != nil {
		t.Fatal(err)
	}
	kinds := map[string]bool{}
	for _, it := range b.Items {
		kinds[it.Kind] = true
	}
	if len(kinds) != 11 {
		t.Fatalf("the brief has %d kinds", len(kinds))
	}
	enc, err := encode(b)
	if err != nil {
		t.Fatal(err)
	}
	back, err := DecodeBrief(enc, allowAll, goodFacts())
	if err != nil {
		t.Fatal(err)
	}
	again, err := encode(back)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(enc, again) {
		t.Fatalf("the brief came back otherwise:\n%s\nwas\n%s", again, enc)
	}
}

// TestRawFieldsAreMeasuredAsTheyCame: a proposal's args and a draft
// crew's body are measured as the session sent them, never as an HTML
// escaping would write them: 700 '<' fit in args of 4 KiB.
func TestRawFieldsAreMeasuredAsTheyCame(t *testing.T) {
	brief := func(item string) []byte {
		return []byte(`{"schema":"brakeman.brief/1","title":"t","summary":"s","items":[{"kind":"fact/1","text":"x","evidence":["run:` + runA + `"]},` + item + `]}`)
	}
	args := func(n int) string {
		head := `{"a":"`
		return head + strings.Repeat("<", n-len(head)-2) + `"}`
	}
	prop := func(a string) string {
		return `{"kind":"proposal/1","text":"Retry.","op":"task_retry","target":"task:` + runA + `/build","args":` + a + `}`
	}
	b, err := DecodeBrief(brief(prop(args(700))), allowAll, goodFacts())
	if err != nil {
		t.Fatalf("args of 700 bytes, most of them '<': %v", err)
	}
	if got := string(b.Items[1].Proposal.Args); got != args(700) {
		t.Fatalf("the args were kept as %s", got)
	}
	if _, err := DecodeBrief(brief(prop(args(MaxArgs))), allowAll, goodFacts()); err != nil {
		t.Fatalf("args of %d bytes: %v", MaxArgs, err)
	}
	_, err = DecodeBrief(brief(prop(args(MaxArgs+1))), allowAll, goodFacts())
	wantInvalid(t, err, "items[1].args")
	crew := func(n int) string {
		head := `{"name":"Parser","cwd":"/src/app","members":[],"note":"`
		return `{"kind":"draft_crew/1","crew":` + head + strings.Repeat("&", n-len(head)-2) + `"}}`
	}
	if _, err := DecodeBrief(brief(crew(MaxCrewBody)), allowAll, goodFacts()); err != nil {
		t.Fatalf("a crew body of %d bytes, most of them '&': %v", MaxCrewBody, err)
	}
	_, err = DecodeBrief(brief(crew(MaxCrewBody+1)), allowAll, goodFacts())
	wantInvalid(t, err, "items[1].crew")
}

// TestAnItemHoldsItsOwnKindsFields: an item without its kind's fields,
// or with another kind's, is refused, never a panic, whatever the caller
// built.
func TestAnItemHoldsItsOwnKindsFields(t *testing.T) {
	full := everyKind(t).Items
	for _, own := range full {
		for _, other := range full {
			it := Item{Kind: own.Kind}
			// other's fields alone: the kind's own are missing.
			it.Fact, it.Interpretation, it.Proposal, it.Outcome, it.Decision, it.Comparison = other.Fact, other.Interpretation, other.Proposal, other.Outcome, other.Decision, other.Comparison
			it.Evidence, it.DraftTicket, it.DraftCrew, it.Handoff, it.Overlap = other.Evidence, other.DraftTicket, other.DraftCrew, other.Handoff, other.Overlap
			if other.Kind == own.Kind {
				continue
			}
			b := goodBrief(t)
			b.Items = append(b.Items, it)
			if inv := wantInvalid(t, check(b), "items[1]"); !strings.Contains(inv.Why, "has no fields") {
				t.Errorf("%s with %s's fields: %s", own.Kind, other.Kind, inv.Why)
			}
		}
		b := goodBrief(t)
		b.Items = append(b.Items, Item{Kind: own.Kind})
		wantInvalid(t, check(b), "items[1]")
	}
	// Its own fields and another kind's too.
	b := goodBrief(t)
	it := factItem(t, "x")
	it.Overlap = &Overlap{}
	b.Items = append(b.Items, it)
	if inv := wantInvalid(t, check(b), "items[1]"); !strings.Contains(inv.Why, "another kind's fields") {
		t.Errorf("why: %s", inv.Why)
	}
}
