package dispatch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// A relation's or a link's own id names one ticket's member: a row from
// elsewhere adds a member under an id this store holds for another ticket
// of its own neither by a refused add's apply nor by a projection of the
// rows it keeps.

// memberPair is two replicas of a shared project CND with two tickets,
// a's relation and link on the first, each added and removed and each
// added again and kept, pulled to b; and b's project OWN, not shared,
// whose ticket holds a relation and a link removed and a relation and a
// link live.
type memberPair struct {
	a, b                     *Store
	after                    int64
	s1, s2                   string // the shared tickets' keys
	x                        string // b's own ticket's id
	relLive, relGone         string // b's own relations' ids
	linkLive, linkGone       string // b's own links' ids
	relRemoved, relKept      string // a's relations' op ids: added and removed, added and kept
	linkRemoved, linkKept    string // a's links' op ids
	relRemove, linkRemove    string // a's removes' op ids
	ownBefore, changesBefore string
}

func newMemberPair(t *testing.T) *memberPair {
	t.Helper()
	ctx := context.Background()
	p := &memberPair{relRemoved: "rel-a", relKept: "rel-a-kept", linkRemoved: "link-a", linkKept: "link-a-kept", relRemove: "unrel-a", linkRemove: "unlink-a"}
	p.a, p.b = replicaPair(t)
	newProject(t, p.a, "CND")
	shareAll(t, p.a)
	_, p.s1 = newTicket(t, p.a, "CND", "Shared one")
	_, p.s2 = newTicket(t, p.a, "CND", "Shared two")
	rc, err := relate(p.a, p.relRemoved, p.s1, RelationBlocks, p.s2)
	want(t, rc, err, StatusDone, "")
	rc, err = p.a.RemoveRelation(ctx, testOp(p.relRemove), p.s1, rc.ID)
	want(t, rc, err, StatusDone, "")
	rc, err = relate(p.a, p.relKept, p.s1, RelationRelates, p.s2)
	want(t, rc, err, StatusDone, "")
	rc, err = addLink(p.a, p.linkRemoved, p.s1, NewLink{Kind: LinkDelivery, URL: "https://example.com/pull/1"})
	want(t, rc, err, StatusDone, "")
	rc, err = p.a.RemoveLink(ctx, testOp(p.linkRemove), p.s1, rc.ID)
	want(t, rc, err, StatusDone, "")
	rc, err = addLink(p.a, p.linkKept, p.s1, NewLink{Kind: LinkDelivery, URL: "https://example.com/pull/2"})
	want(t, rc, err, StatusDone, "")
	pull(t, p.a, p.b, &p.after)
	newProject(t, p.b, "OWN")
	var xKey, yKey string
	p.x, xKey = newTicket(t, p.b, "OWN", "Mine")
	_, yKey = newTicket(t, p.b, "OWN", "Mine too")
	rc, err = relate(p.b, "b-rel-live", xKey, RelationBlocks, yKey)
	want(t, rc, err, StatusDone, "")
	p.relLive = rc.ID
	rc, err = relate(p.b, "b-rel-gone", xKey, RelationRelates, yKey)
	want(t, rc, err, StatusDone, "")
	p.relGone = rc.ID
	rc, err = p.b.RemoveRelation(ctx, testOp("b-unrel"), xKey, p.relGone)
	want(t, rc, err, StatusDone, "")
	rc, err = addLink(p.b, "b-link-live", xKey, NewLink{Kind: LinkDelivery, URL: "https://example.com/mine/1"})
	want(t, rc, err, StatusDone, "")
	p.linkLive = rc.ID
	rc, err = addLink(p.b, "b-link-gone", xKey, NewLink{Kind: LinkDelivery, URL: "https://example.com/mine/2"})
	want(t, rc, err, StatusDone, "")
	p.linkGone = rc.ID
	rc, err = p.b.RemoveLink(ctx, testOp("b-unlink"), xKey, p.linkGone)
	want(t, rc, err, StatusDone, "")
	p.ownBefore, p.changesBefore = p.own(t), p.changes(t)
	return p
}

// own is b's own ticket's relations and links, each with whether it is
// removed.
func (p *memberPair) own(t *testing.T) string {
	t.Helper()
	var out []string
	for _, q := range []string{"SELECT id, deleted_at != '' FROM relations WHERE from_id = ? ORDER BY id", "SELECT id, deleted_at != '' FROM links WHERE ticket_id = ? ORDER BY id"} {
		rows, err := p.b.db.Query(q, p.x)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var id string
			var gone bool
			if err := rows.Scan(&id, &gone); err != nil {
				t.Fatal(err)
			}
			out = append(out, fmt.Sprintf("%s removed=%v", id, gone))
		}
		rows.Close()
	}
	return strings.Join(out, "; ")
}

// changes is how many rows b's log holds.
func (p *memberPair) changes(t *testing.T) string {
	t.Helper()
	return fmt.Sprint(count(t, p.b, "SELECT COUNT(*) FROM changes"))
}

// row is a's row of op id op.
func (p *memberPair) row(t *testing.T, op string) ChangeRow {
	t.Helper()
	rows, _, err := p.a.ChangesFrom(context.Background(), 0, MaxChangesPage)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range rows {
		if c.OpID == op {
			return c
		}
	}
	t.Fatalf("no row of %s on a", op)
	return ChangeRow{}
}

// memberForged is the add row c made again under the op id op, its member's id
// id; with remove, the remove row remove made again under op+"-rm",
// naming that add.
func memberForged(t *testing.T, c ChangeRow, id, op string, remove *ChangeRow) []ChangeRow {
	t.Helper()
	add := withID(t, c, id)
	add.OpID = op
	if remove == nil {
		return []ChangeRow{add}
	}
	rm := *remove
	tags := Tags(rm.Prior)
	if len(tags) != 1 {
		t.Fatalf("the remove names %d adds", len(tags))
	}
	tags[0].OpID = op
	rm.Prior, rm.OpID = EncodeTags(tags), op+"-rm"
	return []ChangeRow{add, rm}
}

// withID is the add row c with its member's id id.
func withID(t *testing.T, c ChangeRow, id string) ChangeRow {
	t.Helper()
	v := map[string]any{}
	if err := json.Unmarshal([]byte(c.Value), &v); err != nil {
		t.Fatal(err)
	}
	v["id"] = id
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	c.Value = string(raw)
	return c
}

// A row from elsewhere adding a relation or a link to the shared ticket
// under the id of b's own ticket's member (a removed one, which would
// come back live, and a live one, added and removed in the batch, which
// would be removed) is refused, the batch whole, ErrNotShared; nothing of
// b's own ticket changes and no row is kept.
func TestAMemberAddedUnderAnotherTicketsIDIsRefused(t *testing.T) {
	ctx := context.Background()
	p := newMemberPair(t)
	for _, tc := range []struct {
		name  string
		batch []ChangeRow
	}{
		{"a removed relation's id", memberForged(t, p.row(t, p.relKept), p.relGone, "forge-1", nil)},
		{"a live relation's id, added and removed", memberForged(t, p.row(t, p.relRemoved), p.relLive, "forge-2", ptr(p.row(t, p.relRemove)))},
		{"a removed link's id", memberForged(t, p.row(t, p.linkKept), p.linkGone, "forge-3", nil)},
		{"a live link's id, added and removed", memberForged(t, p.row(t, p.linkRemoved), p.linkLive, "forge-4", ptr(p.row(t, p.linkRemove)))},
	} {
		got, err := p.b.ApplyRemote(ctx, tc.batch, nil, registryKeys(t, p.a))
		if !errors.Is(err, ErrNotShared) || got.Applied != 0 {
			t.Errorf("%s: %+v %v", tc.name, got, err)
		}
		if own, n := p.own(t), p.changes(t); own != p.ownBefore || n != p.changesBefore {
			t.Fatalf("%s changed b's own ticket: %s (was %s), %s rows (were %s)", tc.name, own, p.ownBefore, n, p.changesBefore)
		}
	}
}

// A projection of the shared ticket's members writes only that ticket's
// rows: with rows kept that name the ids of b's own ticket's members (a
// relation added and removed naming the live relation, a link kept naming
// the removed link, as a build without the bound on members kept them),
// a new member of the shared ticket from a projects its set again, and
// b's own relation stays live and its own link removed.
func TestAProjectionOfMembersWritesOnlyItsTicketsRows(t *testing.T) {
	p := newMemberPair(t)
	for op, id := range map[string]string{p.relRemoved: p.relLive, p.linkKept: p.linkGone} {
		c := withID(t, p.row(t, op), id)
		exec(t, p.b, "UPDATE changes SET value = ? WHERE op_id = ? AND origin = 'a1'", c.Value, op)
	}
	rc, err := addLink(p.a, "link-a-again", p.s1, NewLink{Kind: LinkDelivery, URL: "https://example.com/pull/3"})
	want(t, rc, err, StatusDone, "")
	rc, err = relate(p.a, "rel-a-again", p.s1, RelationBlocks, p.s2)
	want(t, rc, err, StatusDone, "")
	pull(t, p.a, p.b, &p.after)
	if own := p.own(t); own != p.ownBefore {
		t.Fatalf("a projection of the shared ticket changed b's own: %s (was %s)", own, p.ownBefore)
	}
}

// An honest add and remove of a relation and a link on the shared ticket
// apply on the other replica, which holds the shared ticket's members as
// the first does, live and removed alike.
func TestMembersOfASharedTicketConverge(t *testing.T) {
	p := newMemberPair(t)
	members := func(s *Store) string {
		var out []string
		for _, q := range []string{
			"SELECT r.id, r.kind, r.deleted_at != '' FROM relations r JOIN tickets t ON t.id = r.from_id WHERE t.key = ? ORDER BY r.id",
			"SELECT l.id, l.url, l.deleted_at != '' FROM links l JOIN tickets t ON t.id = l.ticket_id WHERE t.key = ? ORDER BY l.id",
		} {
			rows, err := s.db.Query(q, p.s1)
			if err != nil {
				t.Fatal(err)
			}
			for rows.Next() {
				var id, what string
				var gone bool
				if err := rows.Scan(&id, &what, &gone); err != nil {
					t.Fatal(err)
				}
				out = append(out, fmt.Sprintf("%s %s removed=%v", id, what, gone))
			}
			rows.Close()
		}
		return strings.Join(out, "; ")
	}
	if got, want := members(p.b), members(p.a); got != want || strings.Count(want, "removed=true") != 2 || strings.Count(want, "removed=false") != 2 {
		t.Fatalf("the shared ticket's members on b: %s, on a: %s", got, want)
	}
	if own := p.own(t); own != p.ownBefore {
		t.Fatalf("b's own ticket: %s (was %s)", own, p.ownBefore)
	}
}
