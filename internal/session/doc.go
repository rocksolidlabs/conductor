// Package session owns the terminal session model shared by the server and
// the host agent: scrollback, subscriber fan-out, role enforcement, resize
// policy and bounded file reads.
//
// # The record an answer leaves
//
// A question answered through its claim (Local.resolve) leaves one record,
// read by its id (Local.QuestionResult) for the decision inbox's track:
//
//   - its receipt: who answered and when, and the winning reply's request
//     id (Answer.ReqID). resolve writes it, under the session's lock, in
//     the step that clears the claim, once the claim's keys were written
//     (a choice's keys, a line's Enter). It is written once and never
//     replaced: the claim is the question's only writer, and a reply
//     after it is refused.
//   - its resumption (Answer.Resumed): how the session went on, the agent
//     working, a next question, or the end. Set once, under the lock, by
//     the first attention change after the answer that is one of those
//     (noteResumedLocked), never by a later one, so it says what happened
//     when it happened. Each answer waiting keeps its own: a second
//     answer waiting beside the first never takes its place.
//
// A question that goes for good without an answer through its claim
// leaves instead how it went and when (Local.QuestionLeft: keys typed in
// the terminal, the end, another question, withdrawn), noted once in the
// step that let it go (noteGoneLocked). A question with a claim whose
// keys may have been written has not gone: at the session's end such a
// claim stands with its question (endClaimsLocked) until its claimant
// settles it, resolved (the receipt, its resumption the end) or given up
// (left as ended), so that keys written before the end are never read as
// an answer not taken. Nothing replaces a receipt or a resumption; a
// leaving is forgotten only when the same question is shown again with
// its id, held again, to leave anew.
//
// The records are kept in memory for the session's last 64 questions
// each, through the session's end and its orderly stop, for as long as
// the session is listed: a session gone, or a question past them, has
// none, and the track reads it unknown, never a guess.
package session
