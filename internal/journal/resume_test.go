package journal

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// A resume's receipt is reserved pending and written before
// ReserveResume returns, in a record held only on disk too; the same
// command id answers it and reserves nothing; a pending one keeps its
// record from Forget, ForgetAll and Prune; settled, it is written at
// once, and the record is forgettable again.
func TestAResumesReceiptIsReservedOnceAndSettled(t *testing.T) {
	dir := t.TempDir()
	j := openAt(t, dir, Options{})
	endedRecords(t, j, 1, 2)
	j.Close()
	// Opened again: the records are on disk alone.
	j = openAt(t, dir, Options{})
	rec, got, fresh, err := j.ReserveResume(sid(1), Resume{CommandID: "c1", Actor: "admin", Session: sid(50)})
	if err != nil || !fresh || got.State != ResumePending || got.Session != sid(50) || got.At.IsZero() || len(rec.Resumes) != 1 {
		t.Fatalf("reserved: %+v %v %v %+v", got, fresh, err, rec.Resumes)
	}
	if r, _ := onDisk(t, dir, sid(1)); len(r.Resumes) != 1 || r.Resumes[0] != got {
		t.Fatalf("on disk: %+v", r.Resumes)
	}
	again, _, _, _ := j.ReserveResume(sid(1), Resume{CommandID: "c1", Session: sid(51)})
	_, same, fresh, err := j.ReserveResume(sid(1), Resume{CommandID: "c1", Session: sid(51)})
	if err != nil || fresh || same != got || len(again.Resumes) != 1 {
		t.Fatalf("the same id again: %+v %v %v", same, fresh, err)
	}
	if err := j.Forget(sid(1)); !errors.Is(err, ErrResumePending) {
		t.Fatalf("forget while pending: %v", err)
	}
	if forgotten, kept, err := j.ForgetAll(); err != nil || forgotten != 1 || kept != 1 || !exists(dir, sid(1)) {
		t.Fatalf("forget all while pending: %d %d %v", forgotten, kept, err)
	}
	if _, err := j.SettleResume(sid(1), "c1", Resume{State: ResumeFailed, Code: "env_not_kept", Error: "its launch had variables"}); err != nil {
		t.Fatal(err)
	}
	r, _ := onDisk(t, dir, sid(1))
	if x := r.Resumes[0]; x.State != ResumeFailed || x.Code != "env_not_kept" || x.Session != sid(50) || x.CommandID != "c1" {
		t.Fatalf("settled: %+v", x)
	}
	// Settled once: a second settlement changes nothing.
	if _, err := j.SettleResume(sid(1), "c1", Resume{State: ResumeStarted}); err != nil {
		t.Fatal(err)
	}
	if r, _ := onDisk(t, dir, sid(1)); r.Resumes[0].State != ResumeFailed {
		t.Fatalf("settled twice: %+v", r.Resumes[0])
	}
	if err := j.Forget(sid(1)); err != nil || exists(dir, sid(1)) {
		t.Fatalf("forget once settled: %v", err)
	}
	if _, _, _, err := j.ReserveResume(sid(1), Resume{CommandID: "c2"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a record forgotten: %v", err)
	}
}

// At MaxResumes receipts a new command id is refused with nothing
// reserved, and every earlier id still answers its own receipt, after the
// journal is opened again too.
func TestTheSeventeenthReceiptIsRefused(t *testing.T) {
	dir := t.TempDir()
	j := openAt(t, dir, Options{})
	endedRecords(t, j, 1, 1)
	for i := range MaxResumes {
		cmd := fmt.Sprintf("c%d", i)
		if _, _, fresh, err := j.ReserveResume(sid(1), Resume{CommandID: cmd, Session: sid(100 + i)}); err != nil || !fresh {
			t.Fatalf("receipt %d: %v %v", i, fresh, err)
		}
		if _, err := j.SettleResume(sid(1), cmd, Resume{State: ResumeFailed, Code: "already_resumed"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, _, err := j.ReserveResume(sid(1), Resume{CommandID: "c-17"}); !errors.Is(err, ErrResumeLimit) {
		t.Fatalf("the seventeenth: %v", err)
	}
	j.Close()
	j = openAt(t, dir, Options{})
	_, first, fresh, err := j.ReserveResume(sid(1), Resume{CommandID: "c0"})
	if err != nil || fresh || first.Session != sid(100) || first.State != ResumeFailed {
		t.Fatalf("the oldest again: %+v %v %v", first, fresh, err)
	}
	if r, _ := onDisk(t, dir, sid(1)); len(r.Resumes) != MaxResumes {
		t.Fatalf("%d receipts", len(r.Resumes))
	}
}

// hookWrites sets beforeWrite under j's writes' lock (wmu), which every
// write that reads it holds: the writer's own passes cannot read it
// meanwhile.
func hookWrites(j *Journal, f func(name string) error) {
	j.wmu.Lock()
	defer j.wmu.Unlock()
	beforeWrite = f
}

// A reservation whose write fails is taken back: nothing runs under a
// receipt that is not on disk, and the record holds none.
func TestAReservationNotWrittenIsTakenBack(t *testing.T) {
	j, dir := newJournal(t, Options{})
	endedRecords(t, j, 1, 1)
	crash := errors.New("the disk refused it")
	written, _ := onDisk(t, dir, sid(1))
	var refused atomic.Int32
	hookWrites(j, func(name string) error {
		if name == SessionsDir+"/"+sid(1)+".json" {
			refused.Add(1)
			return crash
		}
		return nil
	})
	t.Cleanup(func() { hookWrites(j, nil) })
	if _, _, fresh, err := j.ReserveResume(sid(1), Resume{CommandID: "c1", Session: sid(9)}); !errors.Is(err, crash) || fresh || refused.Load() == 0 {
		t.Fatalf("reserved on a disk that refuses: %v %v", fresh, err)
	}
	// The writer may try the record again meanwhile, refused as well; the
	// hook is let go under the writes' lock, and the flush's pass writes
	// it.
	hookWrites(j, nil)
	j.Flush()
	if r, _ := onDisk(t, dir, sid(1)); len(r.Resumes) != 0 || r.Seq <= written.Seq {
		t.Fatalf("receipts kept, or not written again (seq %d, was %d): %+v", r.Seq, written.Seq, r.Resumes)
	}
	if err := j.Forget(sid(1)); err != nil {
		t.Fatalf("forget: %v", err)
	}
}

// A working directory past MaxCwd is cut, and the cut known as one: a path
// whose JSON form is the bound exactly, of quotes, with a folder in it, is
// kept as that path, its parent; every cut of a path of any kind of
// character at the bound is judged cut, and a whole one well short of it
// is not.
func TestACutWorkingDirectoryIsKnownCut(t *testing.T) {
	j, dir := newJournal(t, Options{})
	parent := "/" + strings.Repeat(`"`, (MaxCwd-1)/2) + strings.Repeat("a", (MaxCwd-1)%2)
	r := serverRecord(sid(1))
	r.Cwd = parent + "/x"
	begin(t, j, r)
	j.Flush()
	got, _ := onDisk(t, dir, sid(1))
	if got.Cwd != parent || !CwdMayBeCut(got.Cwd) {
		t.Fatalf("kept %d bytes, cut %v", len(got.Cwd), CwdMayBeCut(got.Cwd))
	}
	for _, c := range []string{"a", `"`, `\`, "\x01", "\u00e9", "\u2028", "\U0001F600"} {
		for pad := range maxEscape {
			s := strings.Repeat("a", pad) + strings.Repeat(c, MaxCwd+1)
			if cut := fitPath(s, MaxCwd); cut == s || !CwdMayBeCut(cut) {
				t.Fatalf("%q after %d: the cut of %d bytes is not judged cut", c, pad, len(cut))
			}
		}
	}
	if CwdMayBeCut("/w") || CwdMayBeCut(strings.Repeat("a", MaxCwd-maxEscape)) {
		t.Fatal("a whole folder judged cut")
	}
}

// A record is written at the lowest version that holds it: version 1, as
// before, until one of its receipts carries a refusal's code, VCode from
// then on, so that a version 1 file never holds the field a server that
// knows version 1 alone would refuse. SettleResume answers the receipt as
// the record keeps it, its words cut to their bound. Opened again, both
// versions read strictly, not as a later version's, and a version 1 file
// of the old format reads as before.
func TestAReceiptsCodeIsWrittenAtItsOwnVersion(t *testing.T) {
	dir := t.TempDir()
	j := openAt(t, dir, Options{})
	endedRecords(t, j, 1, 2)
	version := func(id string) int {
		t.Helper()
		r, b := onDisk(t, dir, id)
		if r.V == V && bytes.Contains(b, []byte(`"code"`)) {
			t.Fatalf("a version %d file holds a code:\n%s", V, b)
		}
		return r.V
	}
	if _, _, _, err := j.ReserveResume(sid(1), Resume{CommandID: "c1", Session: sid(50)}); err != nil {
		t.Fatal(err)
	}
	if got, err := j.SettleResume(sid(1), "c1", Resume{State: ResumeStarted}); err != nil || got.State != ResumeStarted || got.Session != sid(50) || version(sid(1)) != V {
		t.Fatalf("started: %+v %v, version %d", got, err, version(sid(1)))
	}
	if _, _, _, err := j.ReserveResume(sid(1), Resume{CommandID: "c2", Session: sid(51)}); err != nil {
		t.Fatal(err)
	}
	long := strings.Repeat("a variable's name, ", 40)
	got, err := j.SettleResume(sid(1), "c2", Resume{State: ResumeFailed, Code: "env_not_kept", Error: long})
	if err != nil || got.Code != "env_not_kept" || len(got.Error) > maxResumeEr || !strings.HasPrefix(long, got.Error) || got.Error == "" {
		t.Fatalf("failed: %+v %v", got, err)
	}
	if r, _ := onDisk(t, dir, sid(1)); version(sid(1)) != VCode || r.Resumes[1] != got {
		t.Fatalf("on disk: version %d, %+v, answered %+v", r.V, r.Resumes, got)
	}
	// Settled once: answered as it is kept.
	if again, err := j.SettleResume(sid(1), "c2", Resume{State: ResumeStarted}); err != nil || again != got {
		t.Fatalf("settled again: %+v %v", again, err)
	}
	if version(sid(2)) != V {
		t.Fatal("a record without a code is not written at version 1")
	}
	j.Close()
	// A version 1 file of the format before codes: a failed receipt
	// without one.
	at := time.Date(2026, 11, 5, 9, 0, 0, 0, time.UTC).Format(time.RFC3339)
	old := `{"v": 1, "id": "` + sid(3) + `", "seq": 4, "name": "older", "kind": "server", "agent": {"id": "cat"}, "host": {}, "cwd": "/w", "startedAt": "` + at + `", "endedAt": "` + at + `", "ending": "stopped", "entries": [], ` +
		`"resumes": [{"commandId": "c9", "session": "` + sid(52) + `", "state": "failed", "error": "refused", "at": "` + at + `"}]}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, Dir, SessionsDir, sid(3)+".json"), []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	j = openAt(t, dir, Options{})
	for _, id := range []string{sid(1), sid(2), sid(3)} {
		r, newer, err := j.Get(id)
		if err != nil || newer {
			t.Fatalf("%s opened again: %v %v", id, newer, err)
		}
		if id == sid(1) && r.Resumes[1].Code != "env_not_kept" {
			t.Fatalf("the code read back: %+v", r.Resumes)
		}
		if id == sid(3) && (len(r.Resumes) != 1 || r.Resumes[0].State != ResumeFailed || r.Resumes[0].Error != "refused") {
			t.Fatalf("the version 1 record: %+v", r)
		}
	}
	_, receipt, fresh, err := j.ReserveResume(sid(3), Resume{CommandID: "c9"})
	if err != nil || fresh || receipt.State != ResumeFailed || receipt.Code != "" {
		t.Fatalf("the version 1 receipt: %+v %v %v", receipt, fresh, err)
	}
}
