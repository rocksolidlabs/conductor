package exttest

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/ext"
)

var ann = ext.Principal{Subject: "u-1", Name: "Ann"}

// subjectRef is a subject's reference of a form the server knows.
const subjectRef = "change:git.example.com/acme/web#12@0123456789abcdef0123456789abcdef01234567"

// providerLaunch is a provider's launch of crew with the operation id op.
func providerLaunch(crew, op string) ext.LaunchRequest {
	return ext.LaunchRequest{Crew: crew, By: ann, OpID: op, Origin: ext.Origin{Ref: subjectRef, Label: "Nightly"}}
}

func TestFakeHostLaunchIsOncePerOpID(t *testing.T) {
	ctx := context.Background()
	h := NewFakeHost(t)
	first, err := h.Launch(ctx, providerLaunch("web", "op-1"))
	if err != nil || !ext.ValidRunID(first.ID) || first.Crew != "web" {
		t.Fatalf("Launch = %+v, %v; want a run of web with a run id's shape", first, err)
	}
	// The same operation id with another request answers the run it made,
	// whatever the request asks: the run's origin says what it was
	// launched with.
	other := providerLaunch("api", "op-1")
	other.Origin.Label, other.StopAfter = "Another label", time.Hour
	again, err := h.Launch(ctx, other)
	if err != nil || again != first {
		t.Fatalf("a second launch with the operation id = %+v, %v; want %+v", again, err, first)
	}
	st, err := h.RunByOp(ctx, "op-1")
	if err != nil || st.ID != first.ID || st.Crew != "web" || st.Origin.Label != "Nightly" || !st.Origin.Deadline.IsZero() {
		t.Fatalf("RunByOp = %+v, %v; want the first launch's run, as launched", st, err)
	}
	if err := st.Valid(); err != nil {
		t.Errorf("the fake's run status: %v", err)
	}
	// Another operation id makes another run.
	if second, err := h.Launch(ctx, providerLaunch("web", "op-2")); err != nil || second.ID == first.ID {
		t.Errorf("a launch with another operation id = %+v, %v", second, err)
	}

	// Launches at once with one operation id make one run, each answered it.
	var wg sync.WaitGroup
	ids := make(chan string, Goroutines)
	for range Goroutines {
		wg.Go(func() {
			ref, err := h.Launch(ctx, providerLaunch("web", "op-together"))
			if err != nil {
				t.Error(err)
			}
			ids <- ref.ID
		})
	}
	wg.Wait()
	close(ids)
	seen := map[string]bool{}
	for id := range ids {
		seen[id] = true
	}
	if len(seen) != 1 {
		t.Errorf("launches at once with one operation id made %d runs: %v", len(seen), seen)
	}

	// A time limit is the run's deadline from its start, whole seconds.
	lim := providerLaunch("web", "op-limit")
	lim.StopAfter = 90 * time.Minute
	ref, _ := h.Launch(ctx, lim)
	st, _ = h.Run(ctx, ref.ID)
	if got := st.Origin.Deadline.Sub(st.StartedAt); got != 90*time.Minute || st.Origin.Deadline.Location() != time.UTC {
		t.Errorf("the deadline is %v after the start (%v), want 90m in UTC", got, st.Origin.Deadline)
	}
}

func TestFakeHostRefusesALaunchAsTheServerDoes(t *testing.T) {
	ctx := context.Background()
	h := NewFakeHost(t)
	mine, _ := h.Launch(ctx, providerLaunch("web", "op-1"))
	h.AddForeignRun("web-0000abcd")
	h.SetManaged("plain", false)
	cases := []struct {
		name string
		r    ext.LaunchRequest
		code string
	}{
		{"an origin without an op id", func() ext.LaunchRequest { r := providerLaunch("web", "op-x"); r.OpID = ""; return r }(), ext.RefusalInvalidRequest},
		{"a crew without tasks", providerLaunch("plain", "op-plain"), ext.RefusalNotManaged},
		{"a prior that is not the provider's", func() ext.LaunchRequest {
			r := providerLaunch("web", "op-p1")
			r.Subject = ext.Subject{Ref: subjectRef, Prior: "web-0000abcd"}
			return r
		}(), ext.RefusalPriorUnknown},
		{"a prior that is not there", func() ext.LaunchRequest {
			r := providerLaunch("web", "op-p2")
			r.Subject = ext.Subject{Ref: subjectRef, Prior: "web-ffffffff"}
			return r
		}(), ext.RefusalPriorUnknown},
	}
	for _, c := range cases {
		_, err := h.Launch(ctx, c.r)
		if got := ext.RefusalCode(err); got != c.code || !errors.Is(err, ext.ErrRefused) {
			t.Errorf("%s: Launch = %v (code %q), want %s", c.name, err, got, c.code)
		}
		if c.r.OpID != "" {
			if _, err := h.RunByOp(ctx, c.r.OpID); !errors.Is(err, ext.ErrNotFound) {
				t.Errorf("%s: the refused launch made a run: %v", c.name, err)
			}
		}
	}
	// A subject the server knows no form of is refused first, before the
	// earlier run and the crew; a Brakeman reference is one it knows.
	odd := providerLaunch("plain", "op-odd")
	odd.Subject = ext.Subject{Ref: "example:42", Prior: "web-ffffffff"}
	if _, err := h.Launch(ctx, odd); ext.RefusalCode(err) != ext.RefusalInvalidSubject {
		t.Errorf("a subject in no known form = %v, want invalid_subject", err)
	}
	ticket := providerLaunch("web", "op-ticket")
	ticket.Subject = ext.Subject{Ref: "ticket:CND-12"}
	if _, err := h.Launch(ctx, ticket); err != nil {
		t.Errorf("a ticket's reference as the subject = %v", err)
	}
	// A retry of a launch made answers its run before any check.
	again := providerLaunch("web", "op-ticket")
	again.Subject = ext.Subject{Ref: "example:42"}
	if _, err := h.Launch(ctx, again); err != nil {
		t.Errorf("a retry with another subject = %v, want the run", err)
	}
	// As the server judges them: the earlier run before the crew.
	both := providerLaunch("plain", "op-both")
	both.Subject = ext.Subject{Ref: subjectRef, Prior: "web-ffffffff"}
	if _, err := h.Launch(ctx, both); ext.RefusalCode(err) != ext.RefusalPriorUnknown {
		t.Errorf("an unknown prior with a crew without tasks = %v, want prior_unknown", err)
	}
	// A prior that is the provider's own is taken.
	r := providerLaunch("web", "op-p3")
	r.Subject = ext.Subject{Ref: subjectRef, Prior: mine.ID}
	if _, err := h.Launch(ctx, r); err != nil {
		t.Errorf("a launch after the provider's own run: %v", err)
	}
	// A crew without tasks launched without an origin is today's launch.
	if _, err := h.Launch(ctx, ext.LaunchRequest{Crew: "plain", By: ann}); err != nil {
		t.Errorf("a person's launch of a crew without tasks: %v", err)
	}
}

func TestFakeHostKeepsItsRunsAcrossARestart(t *testing.T) {
	ctx := context.Background()
	h := NewFakeHost(t)
	ref, err := h.Launch(ctx, providerLaunch("web", "op-1"))
	if err != nil {
		t.Fatal(err)
	}
	h.AddForeignRun("web-0000abcd")
	next := h.Restarted(t)
	if !h.Closed() {
		t.Error("the restarted host's old one is open")
	}
	if _, err := h.Run(ctx, ref.ID); !errors.Is(err, context.Canceled) {
		t.Errorf("the old host's Run = %v, want context.Canceled", err)
	}
	if st, err := next.Run(ctx, ref.ID); err != nil || st.ID != ref.ID {
		t.Errorf("the new host's Run = %+v, %v", st, err)
	}
	if again, err := next.Launch(ctx, providerLaunch("web", "op-1")); err != nil || again != ref {
		t.Errorf("a retry after the restart = %+v, %v; want %+v", again, err, ref)
	}
	if _, err := next.Run(ctx, "web-0000abcd"); !errors.Is(err, ext.ErrNotFound) {
		t.Errorf("another's run after the restart: %v", err)
	}
	if n := len(next.Launches()); n != 1 {
		t.Errorf("the new host recorded %d launches, want its own one", n)
	}
	// A new run after the restart takes a new id.
	if other, _ := next.Launch(ctx, providerLaunch("web", "op-2")); other.ID == ref.ID {
		t.Errorf("a run after the restart took the id %s again", other.ID)
	}
}

func TestFakeHostAnswersOnlyItsOwnRuns(t *testing.T) {
	ctx := context.Background()
	h := NewFakeHost(t)
	h.AddForeignRun("web-0000abcd")
	for _, id := range []string{"web-0000abcd", "web-ffffffff"} {
		if _, err := h.Run(ctx, id); !errors.Is(err, ext.ErrNotFound) {
			t.Errorf("Run(%s) = %v, want ErrNotFound", id, err)
		}
		if err := h.StopRun(ctx, ext.StopRequest{Run: id, By: ann}); !errors.Is(err, ext.ErrNotFound) {
			t.Errorf("StopRun(%s) = %v, want ErrNotFound", id, err)
		}
	}
	// A launch without an origin is no provider's run: no op id finds it.
	plain, _ := h.Launch(ctx, ext.LaunchRequest{Crew: "web", By: ann})
	if _, err := h.Run(ctx, plain.ID); ext.RefusalCode(err) != ext.RefusalInvalidRequest {
		t.Errorf("Run of a person's launch's id %s = %v", plain.ID, err)
	}
	if _, err := h.RunByOp(ctx, "op-none"); !errors.Is(err, ext.ErrNotFound) {
		t.Errorf("RunByOp of no launch = %v", err)
	}
	if n := len(h.Launches()); n != 1 {
		t.Errorf("the look-ups launched: %d launches", n)
	}
	for _, c := range []struct {
		name string
		err  error
	}{
		{"Run of an id of another shape", func() error { _, err := h.Run(ctx, "run-1"); return err }()},
		{"RunByOp of an op id of another shape", func() error { _, err := h.RunByOp(ctx, "a/b"); return err }()},
		{"StopRun of a why of two lines", h.StopRun(ctx, ext.StopRequest{Run: "web-0000abcd", By: ann, Why: "a\nb"})},
		{"StopRun by no one", h.StopRun(ctx, ext.StopRequest{Run: "web-0000abcd"})},
	} {
		if ext.RefusalCode(c.err) != ext.RefusalInvalidRequest {
			t.Errorf("%s = %v, want invalid_request", c.name, c.err)
		}
	}
	// A script answers in its place.
	h.OnRun(func(id string) (ext.RunStatus, error) { return ext.RunStatus{ID: id}, nil })
	if st, err := h.Run(ctx, "web-0000abcd"); err != nil || st.ID != "web-0000abcd" {
		t.Errorf("a scripted Run = %+v, %v", st, err)
	}
}

func TestFakeHostStopsOnlyARunningRun(t *testing.T) {
	ctx := context.Background()
	h := NewFakeHost(t)
	ref, _ := h.Launch(ctx, providerLaunch("web", "op-1"))
	if err := h.StopRun(ctx, ext.StopRequest{Run: ref.ID, By: ann, Why: "superseded"}); err != nil {
		t.Fatalf("StopRun = %v", err)
	}
	st, _ := h.Run(ctx, ref.ID)
	if st.State != ext.RunStopped || st.StoppedAt.IsZero() {
		t.Errorf("the stopped run reads %+v", st)
	}
	if err := h.StopRun(ctx, ext.StopRequest{Run: ref.ID, By: ann}); !errors.Is(err, ext.ErrNotRunning) {
		t.Errorf("a second stop = %v, want ErrNotRunning", err)
	}
	for _, c := range []struct{ state, outcome string }{{ext.RunFinished, ext.OutcomeSucceeded}, {ext.RunInterrupted, ""}} {
		r, _ := h.Launch(ctx, providerLaunch("web", "op-"+c.state))
		h.SetRunState(r.ID, c.state, c.outcome)
		before, _ := h.Run(ctx, r.ID)
		if err := h.StopRun(ctx, ext.StopRequest{Run: r.ID, By: ann}); !errors.Is(err, ext.ErrNotRunning) {
			t.Errorf("a stop of a run %s = %v, want ErrNotRunning", c.state, err)
		}
		if after, _ := h.Run(ctx, r.ID); after != before {
			t.Errorf("a refused stop changed the run %s: %+v", c.state, after)
		}
	}
	// A run waiting on a prompt is running: it stops.
	r, _ := h.Launch(ctx, providerLaunch("web", "op-asks"))
	h.SetRunState(r.ID, ext.RunNeedsInput, "")
	if err := h.StopRun(ctx, ext.StopRequest{Run: r.ID, By: ann}); err != nil {
		t.Errorf("a stop of a run waiting on a prompt = %v", err)
	}
	if got := h.Stops(); len(got) != 5 || got[0].Why != "superseded" {
		t.Errorf("Stops = %+v", got)
	}
}

func TestFakeHostDoorsRefuseOnceClosed(t *testing.T) {
	ctx := context.Background()
	h := NewFakeHost(t)
	ref, _ := h.Launch(ctx, providerLaunch("web", "op-1"))
	h.SetResolved("web", sampleResolved())

	// Blocked, each door waits; closed, it answers context.Canceled.
	h.Block()
	done := make(chan error, 4)
	go func() { _, err := h.Run(ctx, ref.ID); done <- err }()
	go func() { _, err := h.RunByOp(ctx, "op-1"); done <- err }()
	go func() { done <- h.StopRun(ctx, ext.StopRequest{Run: ref.ID, By: ann}) }()
	go func() { _, err := h.ResolveLaunch(ctx, ext.ResolveRequest{Crew: "web"}); done <- err }()
	if err := Await(waitCtx(t), h, func() bool { return h.Waiting() == 4 }); err != nil {
		t.Fatalf("the doors never waited: %v", err)
	}
	h.Close()
	for range 4 {
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Errorf("a held door the close let go answered %v", err)
		}
	}
	if st, _ := h.Restarted(t).Run(ctx, ref.ID); st.State != ext.RunRunning {
		t.Errorf("a stop the close cut off stopped the run: %+v", st)
	}
	for name, err := range map[string]error{
		"Run":           func() error { _, err := h.Run(ctx, ref.ID); return err }(),
		"RunByOp":       func() error { _, err := h.RunByOp(ctx, "op-1"); return err }(),
		"StopRun":       h.StopRun(ctx, ext.StopRequest{Run: ref.ID, By: ann}),
		"ResolveLaunch": func() error { _, err := h.ResolveLaunch(ctx, ext.ResolveRequest{Crew: "web"}); return err }(),
	} {
		if !errors.Is(err, context.Canceled) {
			t.Errorf("a closed host's %s answered %v", name, err)
		}
	}
}
