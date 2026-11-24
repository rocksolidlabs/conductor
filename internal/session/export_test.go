package session

import "testing"

// Seams for this package's external tests (package session_test), which
// start sessions with the options a host or the server builds from an
// agent's recipe (internal/agents, which imports this package): the
// session on the fake program, the line's rule and its oracles read on
// one screen, and the matrix of screens the rule is held to.

// TestSession is a session of the fake program with a controlling viewer
// (screenSession).
type TestSession struct {
	*Local
	Sub   *Subscription
	Write func(string)
	p     *fakeProc
}

// NewTestSession is screenSession's session of opts at cols by rows.
func NewTestSession(t *testing.T, opts Options, cols, rows int) *TestSession {
	t.Helper()
	s, p, sub, write := screenSession(t, opts, cols, rows)
	return &TestSession{Local: s, Sub: sub, Write: write, p: p}
}

// Written is every write the program holds once the session's writer has
// none in hand.
func (ts *TestSession) Written(t *testing.T) []string {
	t.Helper()
	untilIdle(t, ts.Local)
	var out []string
	for {
		select {
		case b := <-ts.p.input:
			out = append(out, string(b))
		default:
			return out
		}
	}
}

// DrawsOn has the program draw screen on a cleared screen and waits until
// the session's screen shows it (drawsOn).
func (ts *TestSession) DrawsOn(screen string) bool { return drawsOn(ts.Local, ts.p, screen) }

// BeforeEnter runs f as the writer takes a line's Enter, before its guard
// (beforeEnter); nil clears it.
func (ts *TestSession) BeforeEnter(f func()) {
	if f == nil {
		ts.beforeEnter.Store(nil)
		return
	}
	ts.beforeEnter.Store(&f)
}

// LineJudgement is what the line's rule and its oracles say of the screen
// shown now (TestTheStartupLineRefusesWhereEitherGuardDid): whether the
// guard a line's text had refuses it, whether the guard a line's Enter had
// holds it, the rule's answer (startupLine), and atPrompt with its oracle.
type LineJudgement struct {
	TextRefused, EnterHeld bool
	Line                   error
	AtPrompt, WasAtPrompt  bool
}

// Judge is the LineJudgement of the screen ts shows now.
func (ts *TestSession) Judge() LineJudgement {
	return LineJudgement{TextRefused: ts.startupOnScreen(), EnterHeld: ts.oracleMayShow(), Line: ts.startupLine(false),
		AtPrompt: ts.atPrompt(), WasAtPrompt: ts.oracleAtPrompt()}
}

// MatrixScreen is one of the line rule's matrix of screens.
type MatrixScreen struct{ Name, Screen string }

// MatrixScreens is the line rule's matrix of screens (matrixScreens).
func MatrixScreens() []MatrixScreen {
	var out []MatrixScreen
	for _, sc := range matrixScreens() {
		out = append(out, MatrixScreen{sc.name, sc.screen})
	}
	return out
}

// The screens the external tests draw.
var (
	CodexPromptScreen     = codexPrompt
	CodexFooterOnlyScreen = codexFooterOnly
	UnknownSignInScreen   = unknownSignIn
	ClaudePromptScreen    = claudePromptRow("")
)
