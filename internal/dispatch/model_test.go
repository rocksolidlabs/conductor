package dispatch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// planBounds is every text bound of the plan's schema table, by the
// field's rule: the number and its unit as the plan gives them, and an
// operation's two, op.by (a person's name's bound) and op.actor
// (MaxActor, which the plan leaves to the code). The test below fails
// when model.go's rules and these differ, either way.
var planBounds = map[string]struct {
	max   int
	chars bool
}{
	"project.name":            {60, true},
	"project.description":     {16 << 10, false},
	"project.cwd":             {4096, false},
	"project.tracker_url":     {2 << 10, false},
	"person.name":             {60, true},
	"ticket.title":            {200, true},
	"ticket.description":      {64 << 10, false},
	"ticket.acceptance":       {16 << 10, false},
	"ticket.accepted_note":    {1000, true},
	"label.name":              {40, true},
	"milestone.name":          {60, true},
	"milestone.description":   {4 << 10, false},
	"comment.body":            {16 << 10, false},
	"attachment.name":         {255, false},
	"attachment.content_type": {100, false},
	"link.ref":                {2 << 10, false},
	"link.url":                {2 << 10, false},
	"link.note":               {1000, true},
	"view.name":               {60, true},
	"view.query":              {8 << 10, false},
	// Program item DS2's (docs/plans/dispatch-evidence.md, "A plan
	// revision's body" and the plans table).
	"plan.approved_note":     {1000, true},
	"plan.crew_name":         {60, true},
	"plan.constraints":       {8 << 10, false},
	"plan.note":              {4 << 10, false},
	"plan.reference_path":    {1024, false},
	"plan.reference_note":    {200, true},
	"plan.decision_question": {500, true},
	// The launches table's: a label at most 60 characters, why at most
	// 2,000, and an agent's reported version, which the plan leaves to the
	// code inside the agents' 4 KiB.
	"launch.label":         {60, true},
	"launch.why":           {2000, true},
	"launch.agent_version": {100, false},
	"op.by":                {60, true},
	"op.actor":             {60, false},
}

// atBound is a value of the rule's kind exactly at its bound, and past
// one character or byte more: a character bound is filled with "é", two
// bytes each, so that a bound counted in bytes would refuse it.
func atBound(r textRule) (at, past string) {
	fill := func(n int) string {
		if r.chars {
			return strings.Repeat("é", n)
		}
		return strings.Repeat("a", n)
	}
	switch r.kind {
	case kindURL:
		return "https://h/" + fill(r.max-len("https://h/")), "https://h/" + fill(r.max-len("https://h/")+1)
	case kindJSON:
		return `"` + fill(r.max-2) + `"`, `"` + fill(r.max-1) + `"`
	}
	return fill(r.max), fill(r.max + 1)
}

func TestEveryTextBoundAtItsBoundAndOnePast(t *testing.T) {
	var keys []string
	for key := range fieldRules {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for key := range planBounds {
		if _, ok := fieldRules[key]; !ok {
			t.Errorf("the plan's %s has no rule", key)
		}
	}
	for _, key := range keys {
		r := fieldRules[key]
		plan, ok := planBounds[key]
		if !ok {
			t.Errorf("the rule %s is none of the plan's bounds", key)
			continue
		}
		if r.max != plan.max || r.chars != plan.chars {
			t.Errorf("%s: %d (characters %v), the plan's %d (characters %v)", key, r.max, r.chars, plan.max, plan.chars)
		}
		at, past := atBound(r)
		if err := checkField(key, key, at); err != nil {
			t.Errorf("%s at its bound (%d bytes, %d characters): %v", key, len(at), utf8.RuneCountInString(at), err)
		}
		var ref *Refusal
		if err := checkField(key, key, past); !errors.As(err, &ref) || ref.Code != RefusalBound {
			t.Errorf("%s one past its bound: %v", key, err)
		}
		// Empty: refused when the field must hold something.
		err := checkField(key, key, "")
		if r.required != (err != nil) {
			t.Errorf("%s empty (required %v): %v", key, r.required, err)
		}
		if r.required && (!errors.As(err, &ref) || ref.Code != RefusalInvalid) {
			t.Errorf("%s empty: %v", key, err)
		}
	}
	if err := checkField("no.such", "x", "x"); err == nil || errors.As(err, new(*Refusal)) {
		t.Fatalf("a field with no rule: %v", err)
	}
}

func TestEachKindOfTextRefusesWhatItCannotHold(t *testing.T) {
	refused := func(key, s, code string) {
		t.Helper()
		var ref *Refusal
		if err := checkField(key, "x", s); !errors.As(err, &ref) || ref.Code != code {
			t.Errorf("%s %q: %v, want %s", key, s, err, code)
		}
	}
	allowed := func(key, s string) {
		t.Helper()
		if err := checkField(key, "x", s); err != nil {
			t.Errorf("%s %q: %v", key, s, err)
		}
	}
	controls := []string{"\x00", "\x01", "\r", "\v", "\f", "\x1b", "\x7f", "\u0085", "\u009f"}
	for _, key := range []string{"ticket.description", "ticket.acceptance", "project.description", "comment.body", "milestone.description", "ticket.accepted_note", "link.note"} {
		allowed(key, "## A\n\n- b\n\t- c")
		for _, c := range controls {
			refused(key, "a"+c+"b", RefusalInvalid)
		}
		refused(key, "a\xffb", RefusalInvalid)
	}
	for _, key := range []string{"ticket.title", "project.name", "person.name", "label.name", "milestone.name", "view.name", "op.by", "op.actor"} {
		allowed(key, "A name — with <brackets> & \u2028")
		for _, c := range append(controls, "\n", "\t") {
			refused(key, "a"+c+"b", RefusalInvalid)
		}
	}
	for _, key := range []string{"project.cwd", "link.ref", "attachment.content_type"} {
		allowed(key, `/home/a b/c\d`)
		refused(key, "a\nb", RefusalInvalid)
		refused(key, "a\tb", RefusalInvalid)
	}
	for _, key := range []string{"project.tracker_url", "link.url"} {
		allowed(key, "https://github.com/rocksolidlabs/conductor")
		allowed(key, "HTTP://example.com/a?b=c#d")
		allowed(key, "https://h:443/x")
		allowed(key, "https://[::1]:8080/x")
		// A port with no host name is no host: url.Parse gives each of
		// these a Host that is a port alone and an empty Hostname.
		for _, s := range []string{"ftp://h/x", "https:///x", "//h/x", "github.com/x", "https://h/\u0085", "https://h/a\nb", "mailto:a@b.c", "javascript:alert(1)",
			"https://:443/issues", "http://:80/", "https://:/x", "https://user@:443/x"} {
			refused(key, s, RefusalInvalid)
		}
	}
	allowed("attachment.name", "report (final).pdf")
	for _, s := range []string{".", "..", "a/b", `a\b`, "a\x00b", "a\nb"} {
		refused("attachment.name", s, RefusalInvalid)
	}
	allowed("view.query", `{"status":["ready"],"q":"a\nb"}`)
	refused("view.query", `{"status":`, RefusalInvalid)
}

// The bounds that are counts, sizes and depths, each the plan's number;
// a count is refused past its bound and allowed at it.
func TestEveryCountBoundIsThePlansAndRefusedOnePast(t *testing.T) {
	for _, c := range []struct {
		what      string
		got, plan int
	}{
		{"assignees per ticket", MaxAssignees, 8},
		{"labels per ticket", MaxTicketLabels, 20},
		{"live labels per project", MaxLabels, 100},
		{"live milestones per project", MaxMilestones, 100},
		{"live relations per ticket", MaxRelations, 100},
		{"comments per ticket", MaxComments, 2000},
		{"attachments per ticket", MaxAttachments, 50},
		{"live links per ticket", MaxLinks, 50},
		{"live views", MaxViews, 200},
		{"receipts kept", maxReceipts, 20000},
		{"plan revisions per ticket", MaxPlans, 100},
		{"agents per revision", MaxPlanAgents, 12},
		{"references per revision", MaxPlanReferences, 20},
		{"attachments handed over", MaxPlanAttachments, 8},
		{"comments handed over", MaxPlanComments, 20},
		{"decisions per revision", MaxPlanDecisions, 10},
		{"tasks waiting on a decision", MaxDecisionBefore, 8},
		{"launches per ticket", MaxLaunches, 200},
		{"blockers a package names", MaxPackageBlockers, 20},
	} {
		if c.got != c.plan {
			t.Errorf("%s: %d, the plan's %d", c.what, c.got, c.plan)
		}
		if err := checkCount(c.what, c.got, c.got); err != nil {
			t.Errorf("%s at its bound: %v", c.what, err)
		}
		var ref *Refusal
		if err := checkCount(c.what, c.got+1, c.got); !errors.As(err, &ref) || ref.Code != RefusalBound || !strings.Contains(ref.Words, fmt.Sprint(c.got)) {
			t.Errorf("%s one past its bound: %v", c.what, err)
		}
	}
	for _, c := range []struct {
		what      string
		got, plan int
	}{
		{"an attachment's size", MaxAttachmentSize, 16 << 20},
		{"a change row's value", MaxChangeValue, 64 << 10},
		{"a receipt", MaxReceipt, 4 << 10},
		{"a parent chain", MaxParentDepth, 8},
		{"the highest priority", MaxPriority, 4},
		{"a list's page", MaxListLimit, 200},
		{"a list's page when none is named", DefaultListLimit, 100},
		{"a search", MaxQueryText, 200},
		{"a page of the projects", MaxProjectPage, 200},
		{"the children a ticket's read carries", MaxTicketChildren, 50},
		{"the comments a ticket's read carries", MaxTicketComments, 50},
		{"the relations a ticket's read carries", MaxTicketRelations, 100},
		{"a page of a ticket's history", MaxHistoryPage, 500},
		{"a saved view's query", MaxViewQuery, 8 << 10},
		{"a revision's body", MaxPlanBody, 2 << 20},
		{"a revision's read", MaxPlanReply, 3 << 20},
		{"the attachments a revision hands over", MaxPlanAttachmentBytes, 16 << 20},
		{"the comments a revision hands over", MaxPlanCommentBytes, 64 << 10},
		{"a file-form manifest", MaxManifestFile, 64 << 10},
		{"a delivery link's candidate", MaxLinkCandidate, 200},
		{"a delivery link's verification receipt", MaxLinkVerified, 8 << 10},
		{"a launch's package", MaxPackage, 512 << 10},
		{"a launch's agents", MaxLaunchAgents, 4 << 10},
		{"an attachment's portable file name", MaxAttachmentFile, 64},
	} {
		if c.got != c.plan {
			t.Errorf("%s: %d, the plan's %d", c.what, c.got, c.plan)
		}
	}
}

func TestTheVocabulariesAreThePlans(t *testing.T) {
	if strings.Join(Statuses, " ") != "backlog ready in_progress in_review done cancelled" {
		t.Fatalf("statuses %v", Statuses)
	}
	for _, s := range Statuses {
		if err := checkStatus(s); err != nil {
			t.Errorf("status %s: %v", s, err)
		}
		if closed(s) != (s == TicketDone || s == TicketCancelled) {
			t.Errorf("closed(%s)", s)
		}
	}
	for _, s := range []string{"", "Done", "doing", "closed", " ready"} {
		if err := checkStatus(s); err == nil {
			t.Errorf("status %q allowed", s)
		}
	}
	if strings.Join(Priorities, " ") != "none low medium high urgent" || len(Priorities) != MaxPriority+1 {
		t.Fatalf("priorities %v", Priorities)
	}
	for p := -1; p <= MaxPriority+1; p++ {
		if err := checkPriority(p); (err == nil) != (p >= 0 && p <= MaxPriority) {
			t.Errorf("priority %d: %v", p, err)
		}
	}
	for _, h := range []string{"", "now", "next", "later"} {
		if err := checkHorizon(h); err != nil {
			t.Errorf("horizon %q: %v", h, err)
		}
	}
	for _, h := range []string{"Now", "soon", "never"} {
		if err := checkHorizon(h); err == nil {
			t.Errorf("horizon %q allowed", h)
		}
	}
	for actor, ok := range map[string]bool{
		"person:Alice": true, "person:Zoë Q": true, "agent:claude": true, "agent:codex-2": true, "person:" + strings.Repeat("é", MaxPersonName): true,
		"Alice": false, "person:": false, "agent:": false, "agent:Claude": false, "agent:a_b": false, "person:a\nb": false,
		"agent:" + strings.Repeat("a", 33): false, "person:" + strings.Repeat("é", MaxPersonName+1): false, "human:alice": false,
	} {
		if err := checkAssignee(actor); (err == nil) != ok {
			t.Errorf("assignee %q: %v", actor, err)
		}
	}
	for owner, ok := range map[string]bool{"": true, "Alice": true, strings.Repeat("é", MaxPersonName): true, strings.Repeat("é", MaxPersonName+1): false, "a\tb": false} {
		if err := checkOwner(owner); (err == nil) != ok {
			t.Errorf("owner %q: %v", owner, err)
		}
	}
	for _, c := range []struct {
		tracker, url string
		ok           bool
	}{
		{TrackerNative, "", true},
		{TrackerExternal, "https://github.com/rocksolidlabs/conductor/issues", true},
		{TrackerNative, "https://github.com/x", false},
		{TrackerExternal, "", false},
		{TrackerExternal, "ftp://h/x", false},
		{"jira", "", false},
		{"", "", false},
	} {
		if err := checkTracker(c.tracker, c.url); (err == nil) != c.ok {
			t.Errorf("tracker %q %q: %v", c.tracker, c.url, err)
		}
	}
	for _, c := range []struct {
		requires []string
		want     string
	}{
		{nil, "[]"},
		{[]string{}, "[]"},
		{[]string{"acceptance"}, `["acceptance"]`},
		{[]string{"delivery", "acceptance"}, `["acceptance","delivery"]`},
		{[]string{"verified"}, "verified"},
		{[]string{"acceptance", "acceptance"}, "twice"},
		{[]string{"merged"}, "neither"},
		{[]string{""}, "neither"},
	} {
		got, err := closePolicyValue(c.requires)
		if strings.HasPrefix(c.want, "[") {
			if err != nil || got != c.want {
				t.Errorf("close policy %v: %q %v", c.requires, got, err)
			}
			continue
		}
		var ref *Refusal
		if !errors.As(err, &ref) || ref.Code != RefusalInvalid || !strings.Contains(ref.Words, c.want) {
			t.Errorf("close policy %v: %v, want invalid with %q", c.requires, err, c.want)
		}
	}
	if got := defaultClosePolicy(TrackerNative); !slices.Equal(got, []string{RequireAcceptance}) {
		t.Fatalf("a native project's default policy: %v", got)
	}
	if got := defaultClosePolicy(TrackerExternal); got == nil || len(got) != 0 {
		t.Fatalf("an external project's default policy: %#v", got)
	}
}

// The largest structured change value, a link's add with every field at
// its bound and every byte at its escaped worst (a '\' or '"' doubled;
// U+2028 and U+2029 six bytes for their three, which encoding/json
// writes whatever SetEscapeHTML says; '<', '>' and '&' one byte, since
// the store writes its values with HTML escaping off), is under 16 KiB,
// far inside MaxChangeValue; the values this change writes are smaller.
func TestTheLargestStructuredValueIsUnder16KiB(t *testing.T) {
	const limit = 16 << 10
	longest := ""
	for _, k := range LinkKinds {
		if len(k) > len(longest) {
			longest = k
		}
	}
	ref := "https://h/" + strings.Repeat(`\`, MaxLinkRef-len("https://h/"))
	link := Link{
		ID:        strings.Repeat("f", IDLen),
		TicketID:  strings.Repeat("e", IDLen),
		Kind:      longest,
		Ref:       ref,
		URL:       "https://h/" + strings.Repeat(`\`, MaxLinkURL-len("https://h/")),
		Note:      strings.Repeat("\u2028", MaxLinkNote),
		CreatedAt: time.Date(9999, 12, 31, 23, 59, 59, 999999999, time.UTC),
		CreatedBy: strings.Repeat("\u2029", MaxByName),
	}
	for key, v := range map[string]string{"link.ref": link.Ref, "link.url": link.URL, "link.note": link.Note, "op.by": link.CreatedBy} {
		if err := checkField(key, key, v); err != nil {
			t.Fatalf("%s is not at its bound but past it: %v", key, err)
		}
	}
	fp := strings.Repeat("f", 64)
	value, err := encodeValue(linkAdded{Link: link, Fingerprint: fp})
	if err != nil {
		t.Fatal(err)
	}
	if len(value) >= limit {
		t.Fatalf("a link's add at its worst is %d bytes, not under %d", len(value), limit)
	}
	// Its worst it is: every byte of the ref and the URL doubled, every
	// character of the note and the name six bytes.
	if worst := 2*(MaxLinkRef+MaxLinkURL-2*len("https://h/")) + 6*(MaxLinkNote+MaxByName); len(value) < worst {
		t.Fatalf("the value is %d bytes, under the %d its fields escape to: it is not at its worst", len(value), worst)
	}
	var back map[string]any
	if err := json.Unmarshal([]byte(value), &back); err != nil {
		t.Fatal(err)
	}
	var keys []string
	for k := range back {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if strings.Join(keys, ",") != "createdAt,createdBy,fingerprint,id,kind,note,ref,url" {
		t.Fatalf("a link's add carries %v", keys)
	}
	// The values this change writes at their worst are smaller: a
	// ticket's create row, a project's, a close policy.
	by := strings.Repeat("\u2028", MaxByName)
	for name, v := range map[string]any{
		"a ticket's create":  ticketCreated{ID: link.ID, ProjectID: link.ID, Number: 1<<63 - 1, Key: "ABCDEF-999999999999999999", CreatedAt: link.CreatedAt, CreatedBy: by},
		"a project's create": projectCreated{ID: link.ID, Key: "ABCDEF", CreatedAt: link.CreatedAt},
		"a close policy":     CloseRequirements,
		"a plan revision's create": planCreated{ID: link.ID, TicketID: link.ID, BasedOn: link.ID, CrewID: strings.Repeat("a", 64),
			CrewName: strings.Repeat("\u2028", MaxCrewName), CrewUpdatedAt: link.CreatedAt, Base: strings.Repeat("a", 64),
			Digest: fp, Bytes: MaxPlanBody, Fingerprint: fp, CreatedAt: link.CreatedAt, CreatedBy: by},
		"a launch's create": launchCreated{ID: link.ID, TicketID: link.ID, PlanID: link.ID, RunID: strings.Repeat("a", 64) + "-ffffffff",
			Label: strings.Repeat("\u2028", MaxLaunchLabel), TicketRev: 1<<63 - 1, Intent: fp, Criteria: fp, PackageSHA256: fp, Fingerprint: fp,
			CreatedAt: link.CreatedAt, CreatedBy: by},
		"a launch's why": launchWhy(strings.Repeat("\u2028", 2*MaxLaunchWhy)),
	} {
		got, err := encodeValue(v)
		if err != nil || len(got) >= len(value) {
			t.Errorf("%s at its worst is %d bytes, not under a link's %d: %v", name, len(got), len(value), err)
		}
	}
}

func TestAStructuredValueIsWrittenWithHTMLEscapingOff(t *testing.T) {
	got, err := encodeValue(map[string]string{"a": "<b> & \u2028"})
	if err != nil {
		t.Fatal(err)
	}
	if got != `{"a":"<b> & \u2028"}` {
		t.Fatalf("%s", got)
	}
}

// A ticket's and a project's scalar fields start at the schema's
// defaults: the create's set rows are what differs from them, so a
// default the code and the schema disagree on would be a field the log
// never sets.
func TestTheDefaultsAreTheSchemas(t *testing.T) {
	s := openStore(t, storeDir(t))
	ctx := context.Background()
	now := stamp(time.Now())
	if _, err := s.db.Exec("INSERT INTO projects (id, key, name, created_at, updated_at) VALUES ('p', 'CND', '', ?, ?)", now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("INSERT INTO tickets (id, project_id, number, key, title, created_at, updated_at) VALUES ('t', 'p', 1, 'CND-1', '', ?, ?)", now, now); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		table, id string
		defaults  []fieldValue
	}{{"projects", "p", projectDefaults}, {"tickets", "t", ticketDefaults}} {
		_, err := s.Apply(ctx, testOp("read-"+c.table), "test.read", nil, func(x *tx) error {
			got, err := x.scalars(c.table, c.id, columns(c.defaults))
			if err != nil {
				return err
			}
			if !slices.Equal(got, c.defaults) {
				t.Errorf("%s: the schema's %v, the code's %v", c.table, got, c.defaults)
			}
			return refuse(RefusalInvalid, "read only")
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

// A delivery link's add with its candidate and its verification receipt
// at their bounds (Program item DS2, which writes them from its PR-9; the
// columns are version 2's), every other field at its escaped worst, is
// still under MaxChangeValue: a receipt is JSON kept as it is, so its
// bound as stored is its size in the value.
func TestADeliveryLinksAddWithACandidateIsUnderTheRowBound(t *testing.T) {
	receipt := `{"receipts":[{"where":"` + strings.Repeat("a", MaxLinkVerified-len(`{"receipts":[{"where":""}]}`)) + `"}]}`
	if len(receipt) != MaxLinkVerified || !json.Valid([]byte(receipt)) {
		t.Fatalf("the receipt is %d bytes, valid %v", len(receipt), json.Valid([]byte(receipt)))
	}
	candidate := strings.Repeat("r", MaxLinkCandidate-len("/build/1@")-40) + "/build/1@" + strings.Repeat("a", 40)
	if len(candidate) != MaxLinkCandidate {
		t.Fatalf("the candidate is %d bytes", len(candidate))
	}
	link := Link{
		ID:        strings.Repeat("f", IDLen),
		Kind:      LinkDelivery,
		Ref:       "https://h/" + strings.Repeat(`\`, MaxLinkRef-len("https://h/")),
		URL:       "https://h/" + strings.Repeat(`\`, MaxLinkURL-len("https://h/")),
		Note:      strings.Repeat("\u2028", MaxLinkNote),
		Candidate: candidate,
		Verified:  json.RawMessage(receipt),
		CreatedAt: time.Date(9999, 12, 31, 23, 59, 59, 999999999, time.UTC),
		CreatedBy: strings.Repeat("\u2029", MaxByName),
	}
	value, err := encodeValue(linkAdded{Link: link, Fingerprint: strings.Repeat("f", 64)})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(value, `"candidate":"`+candidate+`"`) || !strings.Contains(value, `"verified":`+receipt) {
		t.Fatalf("the value does not carry the candidate and the receipt as they are")
	}
	t.Logf("a delivery link's add at its worst: %d bytes of %d", len(value), MaxChangeValue)
	if len(value) > MaxChangeValue {
		t.Fatalf("a delivery link's add with its candidate is %d bytes, more than %d", len(value), MaxChangeValue)
	}
}

// A revision's read at its largest, a body at MaxPlanBody beside a
// summary whose texts are at their escaped worst, encoded as the store
// encodes (HTML escaping off, which the API's replies share), is under
// MaxPlanReply: the body is answered as the bytes it is kept as.
func TestTheLargestPlanReadIsUnderItsBound(t *testing.T) {
	body := `{"note":"` + strings.Repeat("<", MaxPlanBody-len(`{"note":""}`)) + `"}`
	if len(body) != MaxPlanBody {
		t.Fatalf("the body is %d bytes", len(body))
	}
	at := time.Date(9999, 12, 31, 23, 59, 59, 999999999, time.UTC)
	worst := func(n int) string { return strings.Repeat("\u2028", n) }
	v := PlanView{
		PlanSummary: PlanSummary{
			ID: strings.Repeat("f", IDLen), TicketID: strings.Repeat("f", IDLen), N: MaxPlans, BasedOn: strings.Repeat("f", IDLen),
			CrewID: strings.Repeat("a", 64), CrewName: worst(MaxCrewName), CrewUpdatedAt: at, Base: strings.Repeat("a", 64),
			Digest: strings.Repeat("a", 64), Bytes: MaxPlanBody, State: PlanSuperseded, ApprovedAt: at, ApprovedBy: worst(MaxByName),
			ApprovedNote: worst(MaxApprovedNote), ApprovedRev: 1<<63 - 1, ApprovedIntent: strings.Repeat("a", 64), WithdrawnAt: at,
			WithdrawnBy: worst(MaxByName), CreatedAt: at, CreatedBy: worst(MaxByName), Fingerprint: strings.Repeat("a", 64), Rev: 1<<63 - 1,
			Stale: &PlanStale{Words: worst(maxReceiptWords), Remedy: RemedyRevise},
		},
		Body: json.RawMessage(body),
	}
	got, err := encodeValue(v)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, body) {
		t.Fatal("the body is not in the read as it is kept")
	}
	t.Logf("the largest revision's read: %d bytes of %d", len(got), MaxPlanReply)
	if len(got) >= MaxPlanReply {
		t.Fatalf("the largest revision's read is %d bytes, not under %d", len(got), MaxPlanReply)
	}
}
