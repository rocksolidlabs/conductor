package api

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/rocksolidlabs/conductor/internal/session"
	"github.com/rocksolidlabs/conductor/internal/session/sessiontest"
)

// A resume of a listed session waits for its process to end (#728): a
// stop that fails marks the session stopped with its process running on,
// and the resume judges the process, never the status.

// outlivingNative is a resume server whose native Runtime's terminals
// outlive a stop (outliving), so that a session and its resume start
// there; every process left is ended at the test's end.
func outlivingNative(t *testing.T) (*testEnv, *outliving, *sessiontest.FakeRuntime) {
	t.Helper()
	terms := &outliving{procs: make(chan *sessiontest.FakeProc, 8)}
	t.Cleanup(func() {
		for {
			select {
			case p := <-terms.procs:
				p.End(0)
			default:
				return
			}
		}
	})
	rt := &sessiontest.FakeRuntime{Place: "seam", Base: terms}
	return resumeEnv(t, withNativeRuntime(rt)), terms, rt
}

// outlivedStop launches a talker, whose terminal outlives a stop, gives
// it a turn when turn says so, and stops it: the stop fails, its status
// ended, its process running. It answers the session and its process.
func outlivedStop(t *testing.T, e *testEnv, terms *outliving, turn bool) (*session.Local, *sessiontest.FakeProc) {
	t.Helper()
	l, aerr := e.srv.createLocalSession(createSessionRequest{AgentID: "talker"}, nil)
	if aerr != nil {
		t.Fatal(aerr)
	}
	p := <-terms.procs
	t.Cleanup(func() { p.End(0) })
	if turn {
		talk(t, e, l.Info().ID)
	}
	if err := l.Stop(context.Background()); err == nil || !l.Info().Status.Ended() {
		t.Fatalf("the stop: %v, the status %s", err, l.Info().Status)
	}
	select {
	case <-l.ProcessDone():
		t.Fatal("the process ended")
	default:
	}
	return l, p
}

// resumeKinds are a resume that takes up the session's conversation and
// a plain relaunch (no turn yet).
var resumeKinds = []struct {
	name string
	turn bool
}{{"conversation", true}, {"relaunch", false}}

// refusedStillRunning fails t unless the reply is the refusal of a resume
// whose session's process outlived its stop, with its words.
func refusedStillRunning(t *testing.T, resp *http.Response, out map[string]any) {
	t.Helper()
	errObj, _ := out["error"].(map[string]any)
	msg, _ := errObj["message"].(string)
	if resp.StatusCode != http.StatusConflict || errorCode(out) != "still_running" ||
		!strings.Contains(msg, "process is still running") || !strings.Contains(msg, "stop it again") {
		t.Fatalf("not refused still_running: %d %v", resp.StatusCode, out)
	}
}

// oneProcess fails t unless the only session with a process running is
// next: one process of the session's line.
func oneProcess(t *testing.T, e *testEnv, next string) {
	t.Helper()
	holders := e.srv.registry.Holders()
	if len(holders) != 1 || holders[0].Info().ID != next {
		ids := []string{}
		for _, d := range holders {
			ids = append(ids, d.Info().ID)
		}
		t.Fatalf("processes running: %v, want only %s", ids, next)
	}
}

// A session whose stop failed, resumed from its page: refused
// still_running with the words, nothing started, whether it takes up its
// conversation or is relaunched; once its process has ended, the resume
// proceeds and one process runs.
func TestAResumeWaitsForAProcessThatOutlivedItsStop(t *testing.T) {
	for _, k := range resumeKinds {
		t.Run(k.name, func(t *testing.T) {
			e, terms, rt := outlivingNative(t)
			l, p := outlivedStop(t, e, terms, k.turn)
			id := l.Info().ID
			starts := len(rt.Asked("StartTerminal"))
			resp, out := e.do("POST", "/api/sessions/"+id+"/resume", adminToken, nil)
			refusedStillRunning(t, resp, out)
			if n := len(rt.Asked("StartTerminal")); n != starts {
				t.Fatalf("started %d processes", n-starts)
			}
			p.End(0)
			<-l.ProcessDone()
			resp, out = e.do("POST", "/api/sessions/"+id+"/resume", adminToken, nil)
			if resp.StatusCode != http.StatusCreated || out["resumed"] != k.turn {
				t.Fatalf("once the process ended: %d %v", resp.StatusCode, out)
			}
			if n := len(rt.Asked("StartTerminal")); n != starts+1 {
				t.Fatalf("started %d processes", n-starts)
			}
			oneProcess(t, e, out["session"].(map[string]any)["id"].(string))
		})
	}
}

// The resume judges the process as it launches, not as the route first
// read the session: held after the route's own reads (the seam judged),
// a process that ends there lets it proceed, and one that runs on past
// it refuses it, which then proceeds once the process has ended.
func TestAResumeJudgesTheProcessAsItLaunches(t *testing.T) {
	for _, k := range resumeKinds {
		for _, ends := range []bool{true, false} {
			t.Run(k.name+map[bool]string{true: "/ends while held", false: "/runs on"}[ends], func(t *testing.T) {
				e, terms, rt := outlivingNative(t)
				l, p := outlivedStop(t, e, terms, k.turn)
				id := l.Info().ID
				starts := len(rt.Asked("StartTerminal"))
				reached, release := holdResume(e, "judged", id)
				t.Cleanup(release)
				type reply struct {
					resp *http.Response
					out  map[string]any
				}
				done := make(chan reply, 1)
				go func() {
					resp, out := e.do("POST", "/api/sessions/"+id+"/resume", adminToken, nil)
					done <- reply{resp, out}
				}()
				select {
				case <-reached:
				case r := <-done:
					t.Fatalf("answered before the seam: %d %v", r.resp.StatusCode, r.out)
				}
				if ends {
					p.End(0)
					<-l.ProcessDone()
				}
				release()
				r := <-done
				if !ends {
					refusedStillRunning(t, r.resp, r.out)
					if n := len(rt.Asked("StartTerminal")); n != starts {
						t.Fatalf("started %d processes", n-starts)
					}
					p.End(0)
					<-l.ProcessDone()
					r.resp, r.out = e.do("POST", "/api/sessions/"+id+"/resume", adminToken, nil)
				}
				if r.resp.StatusCode != http.StatusCreated || r.out["resumed"] != k.turn {
					t.Fatalf("once the process ended: %d %v", r.resp.StatusCode, r.out)
				}
				if n := len(rt.Asked("StartTerminal")); n != starts+1 {
					t.Fatalf("started %d processes", n-starts)
				}
				oneProcess(t, e, r.out["session"].(map[string]any)["id"].(string))
			})
		}
	}
}

// A crew member whose session's stop failed, resumed as the member
// (POST /api/runs/{run}/members/{name}/resume, the session page's route
// for a member's session): the same refusal, nothing started, until its
// process has ended; then it is relaunched, one process running.
func TestAMemberResumeWaitsForAProcessThatOutlivedItsStop(t *testing.T) {
	e, terms, rt := outlivingNative(t)
	runID := e.launchCrew(t, "Talk", map[string]any{"name": "a", "agentId": "talker", "prompt": "", "start": map[string]any{"when": "immediately"}})
	sid := e.waitRunning(t, runID, "a")
	p := <-terms.procs
	t.Cleanup(func() { p.End(0) })
	l := e.local(sid)
	if err := l.Stop(context.Background()); err == nil || !l.Info().Status.Ended() {
		t.Fatalf("the stop: %v, the status %s", err, l.Info().Status)
	}
	starts := len(rt.Asked("StartTerminal"))
	for _, path := range []string{"/api/runs/" + runID + "/members/a/resume", "/api/sessions/" + sid + "/resume"} {
		resp, out := e.do("POST", path, adminToken, nil)
		refusedStillRunning(t, resp, out)
	}
	if n := len(rt.Asked("StartTerminal")); n != starts {
		t.Fatalf("started %d processes", n-starts)
	}
	p.End(0)
	<-l.ProcessDone()
	resp, out := e.do("POST", "/api/runs/"+runID+"/members/a/resume", adminToken, nil)
	if resp.StatusCode != http.StatusCreated || out["resumed"] != false {
		t.Fatalf("once the process ended: %d %v", resp.StatusCode, out)
	}
	oneProcess(t, e, out["session"].(map[string]any)["id"].(string))
}
