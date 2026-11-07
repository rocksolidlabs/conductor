package journal

import (
	"errors"
	"fmt"
	"testing"
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
	if err := j.SettleResume(sid(1), "c1", Resume{State: ResumeFailed, Code: "env_not_kept", Error: "its launch had variables"}); err != nil {
		t.Fatal(err)
	}
	r, _ := onDisk(t, dir, sid(1))
	if x := r.Resumes[0]; x.State != ResumeFailed || x.Code != "env_not_kept" || x.Session != sid(50) || x.CommandID != "c1" {
		t.Fatalf("settled: %+v", x)
	}
	// Settled once: a second settlement changes nothing.
	if err := j.SettleResume(sid(1), "c1", Resume{State: ResumeStarted}); err != nil {
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
		if err := j.SettleResume(sid(1), cmd, Resume{State: ResumeFailed, Code: "already_resumed"}); err != nil {
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

// A reservation whose write fails is taken back: nothing runs under a
// receipt that is not on disk, and the record holds none.
func TestAReservationNotWrittenIsTakenBack(t *testing.T) {
	j, dir := newJournal(t, Options{})
	endedRecords(t, j, 1, 1)
	crash := errors.New("the disk refused it")
	beforeWrite = func(name string) error {
		if name == SessionsDir+"/"+sid(1)+".json" {
			return crash
		}
		return nil
	}
	t.Cleanup(func() { beforeWrite = nil })
	if _, _, fresh, err := j.ReserveResume(sid(1), Resume{CommandID: "c1", Session: sid(9)}); !errors.Is(err, crash) || fresh {
		t.Fatalf("reserved on a disk that refuses: %v %v", fresh, err)
	}
	beforeWrite = nil
	j.Flush()
	if r, _ := onDisk(t, dir, sid(1)); len(r.Resumes) != 0 {
		t.Fatalf("receipts kept: %+v", r.Resumes)
	}
	if err := j.Forget(sid(1)); err != nil {
		t.Fatalf("forget: %v", err)
	}
}
