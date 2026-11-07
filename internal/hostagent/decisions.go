package hostagent

import (
	"context"

	"github.com/rocksolidlabs/conductor/internal/proto"
)

// Decisions serves a decision link's lane (docs/plans/decision-inbox.md,
// PR-5; docs/protocol.md, Decision links): the decisions in the scope of
// the link linkID, read and acted on over the connection sessionID's
// viewer holds through it ("" for a run's desk, which holds no session).
// The server's API implements it (internal/api, lane.go) and serves its
// own decision links' connections with it; a publisher hands it to the
// connections it carries through a switchyard (PR-6). Each method answers
// one request with the frame its reply is: a TypeDecision frame, or a
// decide_ok or decide_refused control, naming the request's reqId. The
// link is checked again on every request, so a link revoked, expired or
// gone refuses what comes after.
type Decisions interface {
	// List answers a page of the scope's items, Read one item, Track the
	// four steps of an action taken through this link, Record a part of a
	// DR1 record (unsupported until DR1's records are items), Evidence a
	// bounded read of one evidence entry, named by its ref.
	List(ctx context.Context, linkID, sessionID string, m proto.DecisionsList) []byte
	Read(ctx context.Context, linkID, sessionID string, m proto.DecisionRead) []byte
	Track(ctx context.Context, linkID, sessionID string, m proto.DecisionTrack) []byte
	Record(ctx context.Context, linkID, sessionID string, m proto.DecisionRecord) []byte
	Evidence(ctx context.Context, linkID, sessionID string, m proto.DecisionEvidence) []byte
	// Decide takes one action through the item's own operation, as the
	// link's actor. answer, when not nil, is the connection's own answer
	// (its subscriber's, as its `answer` message is taken): a question of
	// sessionID is answered through it, and every other question through
	// the question's claim as the link.
	Decide(ctx context.Context, linkID, sessionID string, m proto.Decide, answer func(context.Context, proto.Answer) error) []byte
	// Watch calls changed whenever what the link's viewer sees may have
	// changed (its scope's items, their actions' revisions, the steps of
	// what it acted on), until stop is called.
	Watch(linkID, sessionID string, changed func()) (stop func())
	// DecisionLink says whether linkID is a decision link the publisher
	// minted at its switchyard that opens a lane here, as it stands: a
	// published session's (sessionID, the server's own id of it: a link of
	// that session, or of the run it is a member of), or a run desk's
	// (sessionID "", runID the desk's run: a link of that run). The
	// publisher's peers and desks ask it of every viewer they carry
	// (PR-6), so a viewer of any other link, or a link of the server's
	// own, has no lane through the switchyard.
	DecisionLink(linkID, sessionID, runID string) bool
}
