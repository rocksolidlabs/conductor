package dispatch

import (
	"encoding/json"
	"sort"
	"strconv"
	"time"
)

// The field rules for replication (docs/plans/dispatch.md, "Field rules
// for replication"; O10, Owner decisions, 1): every changes row is an
// operation on one field of one entity, and the row tables are the state
// those rows make. CS1 ships changes rows between replicas, drops one
// whose identity it holds, and applies the rest in the one order by each
// field's class: Class names the class, Merge what two changes of one
// field make, Reconcile what a merged parent graph, blocks graph or run
// link keeps of its invariant. Pure: nothing here reads the store.

// FieldClass is a field's class under replication.
type FieldClass string

// The classes. An immutable field is never written twice. A scalar field
// takes the last writer per field (the later at wins; equal, the greater
// actor; equal, the greater op_id), each field merged alone. A set is an
// observed-remove set: an add is tagged by its row's identity, a remove
// names in prior the tags of the member's adds it had seen and removes
// those alone, and a member is present while an add of it exists that no
// remove names. A log is append only, in the one order, never merged and
// never lost. An allocation (a ticket's number, a project's key) is
// minted from a range the writer owns, so it never conflicts. A gated
// field (status set to done) was gated by the writer's close policy and
// merges as a scalar: a merge never re-applies the policy.
const (
	ClassImmutable  FieldClass = "immutable"
	ClassScalar     FieldClass = "scalar"
	ClassSet        FieldClass = "set"
	ClassLog        FieldClass = "log"
	ClassAllocation FieldClass = "allocation"
	ClassGated      FieldClass = "gated"
)

// Class names the class of a field of an entity, the entity as a change
// row names it and the field as its column (a set by its field's name,
// assignees, labels, relations, links or attachments, or by its table's;
// a ticket's comments and the changes log as log); "" for a pair it does
// not know.
func Class(entity, field string) FieldClass {
	switch entity {
	case EntityTicket:
		switch field {
		case "id", "project_id", "key", "created_at", "created_by":
			return ClassImmutable
		case "number":
			return ClassAllocation
		case "status":
			return ClassGated
		case "title", "description", "acceptance", "priority", "horizon", "owner", "parent_id", "milestone_id",
			"accepted_at", "accepted_by", "accepted_for", "accepted_criteria", "accepted_note", "closed_at", "deleted_at",
			"source", "external_id", "external_url", "external_snapshot", "authority":
			return ClassScalar
		case "assignees", "ticket_assignees", "labels", "ticket_labels", "relations", "links", "attachments":
			return ClassSet
		case "comments":
			return ClassLog
		}
	case EntityProject:
		switch field {
		case "id", "created_at":
			return ClassImmutable
		case "key", "next_number", "number_from", "number_to":
			return ClassAllocation
		case "name", "description", "cwd", "tracker", "tracker_url", "close_requires", "archived_at":
			return ClassScalar
		}
	case EntityLabel:
		switch field {
		case "id", "project_id", "created_at":
			return ClassImmutable
		case "name", "color", "deleted_at":
			return ClassScalar
		}
	case EntityMilestone:
		switch field {
		case "id", "project_id", "created_at":
			return ClassImmutable
		case "name", "description", "target", "state", "deleted_at":
			return ClassScalar
		}
	case EntityView:
		switch field {
		case "id", "project_id", "created_at":
			return ClassImmutable
		case "name", "kind", "query", "deleted_at":
			return ClassScalar
		}
	case EntityComment:
		switch field {
		case "id", "ticket_id", "author", "by", "created_at":
			return ClassImmutable
		case "body", "edited_at", "deleted_at":
			return ClassScalar
		}
	case EntityPerson:
		switch field {
		case "name", "created_at":
			return ClassImmutable
		}
	case "link":
		// A link's row, a member of its ticket's links set: what its add
		// carries is never written again, the operation's fingerprint and a
		// delivery's candidate and verification receipt among it (Program
		// item DS2); its removal is the set's.
		switch field {
		case "id", "ticket_id", "kind", "ref", "url", "note", "created_at", "created_by", "fingerprint", "candidate", "verified":
			return ClassImmutable
		}
	case EntityPlan:
		// A plan revision (docs/plans/dispatch-evidence.md, "Field rules for
		// replication"): its content is immutable, its body named by its
		// digest and shipped beside the create row that names it; its
		// approval and withdrawal are scalars, the last writer winning per
		// field; its state is gated, as a ticket's status is: an approval
		// was gated by the writer's rule (the operator, a fresh revision),
		// and a merge takes the last writer's state and never applies the
		// rule again. Two revisions of one ticket approved on two replicas
		// are two entities, and both stand (kept): a launch names one, and
		// a person withdraws the other.
		switch field {
		case "id", "ticket_id", "based_on", "crew_id", "crew_name", "crew_updated_at", "base", "body", "digest", "bytes",
			"created_at", "created_by", "fingerprint":
			return ClassImmutable
		case "state":
			return ClassGated
		case "approved_at", "approved_by", "approved_note", "approved_rev", "approved_intent", "withdrawn_at", "withdrawn_by":
			return ClassScalar
		}
	case EntityLaunch:
		// A launch: what was launched, from which revision, with which
		// package, is immutable, the package named by its digest; its
		// state, why, agents and start are scalars. A launch was made under
		// the writer's rule (an approved, fresh revision), which a merge
		// never applies again.
		switch field {
		case "id", "ticket_id", "plan_id", "run_id", "label", "ticket_rev", "intent", "criteria", "package", "package_sha256",
			"created_at", "created_by", "fingerprint":
			return ClassImmutable
		case "state", "why", "agents", "started_at":
			return ClassScalar
		}
	case "launch_file", "launch_files":
		// The attachments a launch's package names: rows of the launch's,
		// written once with it.
		switch field {
		case "launch_id", "sha256":
			return ClassImmutable
		}
	case "change", "changes":
		switch field {
		case "actor", "op_id", "n":
			return ClassImmutable
		}
		return ClassLog
	}
	return ""
}

// Tag is a change row's identity across replicas: the tag of a set's
// add, and what a remove names in its prior.
type Tag struct {
	Actor string `json:"actor"`
	OpID  string `json:"opId"`
	N     int    `json:"n"`
}

// TagOf is c's identity.
func TagOf(c ChangeRow) Tag { return Tag{c.Actor, c.OpID, c.N} }

// Tags reads the tags a remove's prior names; none for anything else.
func Tags(prior string) []Tag {
	var tags []Tag
	if json.Unmarshal([]byte(prior), &tags) != nil {
		return nil
	}
	return tags
}

// EncodeTags is tags as a remove's prior.
func EncodeTags(tags []Tag) string {
	if tags == nil {
		tags = []Tag{}
	}
	b, _ := json.Marshal(tags)
	return string(b)
}

// names reports whether the remove names tag.
func names(remove ChangeRow, tag Tag) bool {
	for _, t := range Tags(remove.Prior) {
		if t == tag {
			return true
		}
	}
	return false
}

// Less orders two change rows in the one order of the log: at, then
// actor, then op_id, then n. CS1 may replace at with a hybrid logical
// clock without changing a rule, since every rule compares these four
// and nothing else.
func Less(a, b ChangeRow) bool {
	return less(a.At, a.Actor, a.OpID, a.N, b.At, b.Actor, b.OpID, b.N)
}

func less(at1 time.Time, actor1, op1 string, n1 int, at2 time.Time, actor2, op2 string, n2 int) bool {
	if !at1.Equal(at2) {
		return at1.Before(at2)
	}
	if actor1 != actor2 {
		return actor1 < actor2
	}
	if op1 != op2 {
		return op1 < op2
	}
	return n1 < n2
}

// Order sorts rows into the one order, in place.
func Order(rows []ChangeRow) {
	sort.SliceStable(rows, func(i, j int) bool { return Less(rows[i], rows[j]) })
}

// Winner is what Merge makes of two changes of one field.
type Winner int

const (
	// KeepLocal keeps the local change and drops the remote one: a
	// second create of an id held, a change row of a held identity, an
	// immutable field, the earlier scalar, an add a remove had seen.
	KeepLocal Winner = iota
	// TakeRemote replaces the local change with the remote one: the later
	// scalar, a remove that names the local add's tag.
	TakeRemote
	// KeepBoth keeps both: two adds of a set, two removes, a log's rows,
	// an add and a remove that did not see it (the add stands, and the
	// remove stands for the tags it names), from either side.
	KeepBoth
)

func (w Winner) String() string {
	switch w {
	case KeepLocal:
		return "keep local"
	case TakeRemote:
		return "take remote"
	case KeepBoth:
		return "keep both"
	}
	return "winner(" + strconv.Itoa(int(w)) + ")"
}

// Merge is what two changes of one field of one entity, one held by this
// replica and one arriving, make together, by the field's class. A
// change whose identity the replica holds, and a second create of an id
// it has, are dropped. The two replicas get the same rows whichever of
// them merges: Merge(a, b) and Merge(b, a) are mirrors.
func Merge(local, remote ChangeRow) Winner {
	if TagOf(local) == TagOf(remote) {
		return KeepLocal
	}
	if local.Op == OpCreate || remote.Op == OpCreate {
		return KeepLocal
	}
	switch Class(local.Entity, local.Field) {
	case ClassScalar, ClassGated:
		if Less(local, remote) {
			return TakeRemote
		}
		return KeepLocal
	case ClassSet:
		switch {
		case local.Op == OpAdd && remote.Op == OpRemove:
			if names(remote, TagOf(local)) {
				return TakeRemote
			}
			return KeepBoth
		case local.Op == OpRemove && remote.Op == OpAdd:
			if names(local, TagOf(remote)) {
				return KeepLocal
			}
			return KeepBoth
		}
		return KeepBoth
	case ClassLog:
		return KeepBoth
	}
	return KeepLocal
}

// The kinds of edge Reconcile replays: a ticket's parent set, a blocks
// relation added or removed, a run link added or removed, and a ticket's
// deleted_at set, since a tombstone holds no run.
const (
	EdgeParent    = "parent"
	EdgeBlocks    = "blocks"
	EdgeRun       = "run"
	EdgeTombstone = "tombstone"
)

// EdgeChange is one source change of the edges an invariant spans, as
// Reconcile replays them: its identity and place in the one order (At,
// Actor, OpID, N), its Kind, its Op (set for a parent or a tombstone,
// add or remove for a relation or a link), and the edge: From the child
// and To its new parent ("" to clear); From the blocker and To the
// blocked; From the ticket and To the run; From the ticket of a
// tombstone set, Deleted whether it is deleted after it. A remove names
// in Tags the adds of its member it removes, as its change row's prior
// does, and removes those alone: one naming none removes nothing.
type EdgeChange struct {
	At      time.Time
	Actor   string
	OpID    string
	N       int
	Kind    string
	Op      string
	From    string
	To      string
	Tags    []Tag
	Deleted bool
}

// Tag is e's identity.
func (e EdgeChange) Tag() Tag { return Tag{e.Actor, e.OpID, e.N} }

// Reconcile replays the source changes of the invariants in the one
// order, as if they had been written one after another on one replica,
// and names the adds it refuses: a parent set that would make a cycle or
// a chain past MaxParentDepth (the chain from the deepest ticket of the
// moved subtree to the root), a blocks add that would make a cycle, a
// run link added to a live ticket while another live ticket holds the
// run, and the link of a ticket undeleted while another live ticket
// holds the run meanwhile. An add is admitted only when the edges
// admitted and not removed before it keep the invariant, as that writer
// would have been refused had it seen the others, so the answer is a
// function of the whole log alone, the same whichever batches it arrived
// in and never of the state a batch found; the row tables hold the
// admitted edges, and a refused add is undone there with a Repair row.
// The replay goes on from the graph those rows hold: parent_id is a
// scalar, which a later set replaces whether or not the set stands, so a
// refused parent set leaves its ticket with no parent, as its repair
// clears the field, never with the parent the set replaced. A tombstone
// holds no run, so a link added to one (its writer had not seen the
// delete) is admitted inactive whoever holds the run, and is refused, if
// at all, at its ticket's undelete, when another live ticket holds the
// run then. Tombstoned tickets keep their parent and their relations, so
// those walks take them as edges.
// A remove removes the adds of its member it names, whatever their place
// in the order: one that sorts before an add it names (its writer's clock
// behind the adder's) removes that add when it comes, as the
// observed-remove rule removes it whatever the clocks say, so a removed
// add is neither admitted nor refused.
func Reconcile(changes []EdgeChange) []EdgeChange {
	log := append([]EdgeChange(nil), changes...)
	sort.SliceStable(log, func(i, j int) bool {
		return less(log[i].At, log[i].Actor, log[i].OpID, log[i].N, log[j].At, log[j].Actor, log[j].OpID, log[j].N)
	})
	var refused []EdgeChange
	parents := map[string]string{}
	blocks := map[string]map[string][]Tag{}
	type link struct {
		e                EdgeChange
		removed, refused bool
	}
	byRun := map[string][]*link{}
	byTicket := map[string][]*link{}
	deleted := map[string]bool{}
	// removed is every tag a remove has named, by its member, for the
	// adds that sort after the remove that names them.
	type member struct{ kind, from, to string }
	removed := map[member]map[Tag]bool{}
	remove := func(e EdgeChange) {
		m := member{e.Kind, e.From, e.To}
		if removed[m] == nil {
			removed[m] = map[Tag]bool{}
		}
		for _, t := range e.Tags {
			removed[m][t] = true
		}
	}
	isRemoved := func(e EdgeChange) bool { return removed[member{e.Kind, e.From, e.To}][e.Tag()] }
	// holder is a live ticket other than except whose admitted, unremoved
	// link holds the run.
	holder := func(run, except string) *link {
		for _, l := range byRun[run] {
			if l.e.From != except && !l.removed && !l.refused && !deleted[l.e.From] {
				return l
			}
		}
		return nil
	}
	for _, e := range log {
		switch e.Kind {
		case EdgeParent:
			if e.Op != OpSet {
				continue
			}
			if e.To == "" {
				parents[e.From] = ""
				continue
			}
			if e.To == e.From || reachesUp(parents, e.To, e.From) || depthOf(parents, e.To)+height(parents, e.From) > MaxParentDepth {
				refused = append(refused, e)
				// The set replaced the parent it found, and its repair
				// clears the field: no parent from here, as in the rows.
				parents[e.From] = ""
				continue
			}
			parents[e.From] = e.To
		case EdgeBlocks:
			switch e.Op {
			case OpAdd:
				if isRemoved(e) {
					continue
				}
				if e.From == e.To || reachesBlocks(blocks, e.To, e.From) {
					refused = append(refused, e)
					continue
				}
				if blocks[e.From] == nil {
					blocks[e.From] = map[string][]Tag{}
				}
				blocks[e.From][e.To] = append(blocks[e.From][e.To], e.Tag())
			case OpRemove:
				remove(e)
				var kept []Tag
				for _, t := range blocks[e.From][e.To] {
					if !hasTag(e.Tags, t) {
						kept = append(kept, t)
					}
				}
				if len(kept) == 0 {
					delete(blocks[e.From], e.To)
				} else {
					blocks[e.From][e.To] = kept
				}
			}
		case EdgeRun:
			switch e.Op {
			case OpAdd:
				l := &link{e: e, removed: isRemoved(e)}
				// A tombstone's link holds no run: admitted inactive, it
				// meets the holder at its ticket's undelete, below.
				if !l.removed && !deleted[e.From] && holder(e.To, e.From) != nil {
					l.refused = true
					refused = append(refused, e)
				}
				byRun[e.To] = append(byRun[e.To], l)
				byTicket[e.From] = append(byTicket[e.From], l)
			case OpRemove:
				remove(e)
				for _, l := range byRun[e.To] {
					if l.e.From == e.From && hasTag(e.Tags, l.e.Tag()) {
						l.removed = true
					}
				}
			}
		case EdgeTombstone:
			deleted[e.From] = e.Deleted
			if e.Deleted {
				continue
			}
			// Undeleted: each of its links holds its run again, unless
			// another live ticket took the run meanwhile.
			for _, l := range byTicket[e.From] {
				if !l.removed && !l.refused && holder(l.e.To, e.From) != nil {
					l.refused = true
					refused = append(refused, l.e)
				}
			}
		}
	}
	return refused
}

func hasTag(tags []Tag, t Tag) bool {
	for _, x := range tags {
		if x == t {
			return true
		}
	}
	return false
}

// reachesUp reports whether target is from or one of its ancestors.
func reachesUp(parents map[string]string, from, target string) bool {
	seen := map[string]bool{}
	for id := from; id != "" && !seen[id]; id = parents[id] {
		if id == target {
			return true
		}
		seen[id] = true
	}
	return false
}

// depthOf is how many tickets the chain from id to the root holds, id
// included.
func depthOf(parents map[string]string, id string) int {
	seen := map[string]bool{}
	n := 0
	for ; id != "" && !seen[id]; id = parents[id] {
		seen[id] = true
		n++
	}
	return n
}

// height is how many tickets the longest chain down from id holds, id
// included.
func height(parents map[string]string, id string) int {
	children := map[string][]string{}
	for c, p := range parents {
		if p != "" {
			children[p] = append(children[p], c)
		}
	}
	var walk func(id string, seen map[string]bool) int
	walk = func(id string, seen map[string]bool) int {
		if seen[id] {
			return 0
		}
		seen[id] = true
		deepest := 0
		for _, c := range children[id] {
			if h := walk(c, seen); h > deepest {
				deepest = h
			}
		}
		return deepest + 1
	}
	return walk(id, map[string]bool{})
}

// reachesBlocks reports whether target is reached from from along the
// live blocks edges.
func reachesBlocks(blocks map[string]map[string][]Tag, from, target string) bool {
	seen := map[string]bool{}
	stack := []string{from}
	for len(stack) > 0 {
		id := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if id == target {
			return true
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		for to := range blocks[id] {
			stack = append(stack, to)
		}
	}
	return false
}

// ActorMerge is the actor of the change rows a merge's repair writes
// (Repair): derived from the source log, never a source change; CS1
// ships none.
const ActorMerge = "merge"

// Repair is the change row that records an edge's change of standing
// after a merge, by the actor merge with the op reconcile: refused
// (admitted false), the edge is undone in the row tables, the parent
// cleared to "" or the relation or link removed; admitted once an earlier
// change arrives (admitted true), restored. Its op id is the edge's own
// identity, its tag as JSON, which no two tags share (a join of the parts
// with '.' would, since an op id and an attempt's actor may each hold
// one), and n numbers the row among that edge's repairs, since an edge's
// standing may change more than once; the caller fills the project.
func Repair(e EdgeChange, admitted bool, at time.Time, n int) ChangeRow {
	fields := map[string]string{EdgeParent: "parent_id", EdgeBlocks: "relations", EdgeRun: "links"}
	tag, _ := json.Marshal(e.Tag())
	value, _ := json.Marshal(struct {
		Kind     string `json:"kind"`
		From     string `json:"from"`
		To       string `json:"to"`
		Tag      Tag    `json:"tag"`
		Admitted bool   `json:"admitted"`
	}{e.Kind, e.From, e.To, e.Tag(), admitted})
	return ChangeRow{
		Entity: EntityTicket, EntityID: e.From, TicketID: e.From, Field: fields[e.Kind], Op: OpReconcile, Value: string(value),
		At: at, Actor: ActorMerge, OpID: string(tag), N: n,
	}
}
