//go:build unix

package store

import (
	"bufio"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// The helper holds the owner lock at STORE_LOCK_HOLD until its stdin
// closes.
func TestHelperHoldsAnOwnerLock(t *testing.T) {
	path := os.Getenv("STORE_LOCK_HOLD")
	if path == "" {
		t.Skip("a helper")
	}
	lock, why, _, err := TakeOwnerLock(path)
	if err != nil || lock == nil {
		os.Stdout.WriteString("error: " + why + "\n")
		os.Exit(2)
	}
	os.Stdout.WriteString("held\n")
	bufio.NewReader(os.Stdin).ReadString('\n')
	lock.Release()
}

// The owner lock as the ledger has always taken it (the ledger's lock
// tests, moved with it): one holder at a time, in this process and across
// processes, the holder's pid in the file and in the words of whoever
// finds it held, 0600, a link in its place refused, and taken again once
// let go.
func TestOwnerLock(t *testing.T) {
	t.Run("one holder in a process", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "owner.lock")
		lock, why, held, err := TakeOwnerLock(path)
		if err != nil || lock == nil || held || why != "" {
			t.Fatalf("first take: %v %q %v %v", lock, why, held, err)
		}
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if want := "pid " + strconv.Itoa(os.Getpid()) + " since "; !strings.HasPrefix(string(b), want) {
			t.Fatalf("the lock file says %q, want it to begin %q", b, want)
		}
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != 0o600 {
			t.Fatalf("mode %o", fi.Mode().Perm())
		}
		again, why, held, err := TakeOwnerLock(path)
		if err != nil || again != nil || !held {
			t.Fatalf("a second take while held: %v %q %v %v", again, why, held, err)
		}
		if !strings.HasPrefix(why, "another server holds "+path) || !strings.Contains(why, "pid "+strconv.Itoa(os.Getpid())) {
			t.Fatalf("held, why: %s", why)
		}
		lock.Release()
		lock, _, _, err = TakeOwnerLock(path)
		if err != nil || lock == nil {
			t.Fatalf("a take once let go: %v %v", lock, err)
		}
		lock.Release()
	})
	t.Run("a link in its place", func(t *testing.T) {
		dir := t.TempDir()
		target := filepath.Join(dir, "elsewhere")
		if err := os.WriteFile(target, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, "owner.lock")
		if err := os.Symlink(target, path); err != nil {
			t.Fatal(err)
		}
		if lock, _, _, err := TakeOwnerLock(path); err == nil || lock != nil {
			lock.Release()
			t.Fatal("a symbolic link was followed")
		}
	})
	t.Run("across processes", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "owner.lock")
		helper := exec.Command(os.Args[0], "-test.run=^TestHelperHoldsAnOwnerLock$")
		helper.Env = append(os.Environ(), "STORE_LOCK_HOLD="+path)
		stdin, err := helper.StdinPipe()
		if err != nil {
			t.Fatal(err)
		}
		stdout, err := helper.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		if err := helper.Start(); err != nil {
			t.Fatal(err)
		}
		defer func() {
			stdin.Close()
			helper.Wait()
		}()
		sc := bufio.NewScanner(stdout)
		held := false
		for sc.Scan() {
			if sc.Text() == "held" {
				held = true
				break
			}
			if strings.HasPrefix(sc.Text(), "error:") {
				t.Fatal(sc.Text())
			}
		}
		if !held {
			t.Fatal("the helper did not take the lock")
		}
		lock, why, isHeld, err := TakeOwnerLock(path)
		if err != nil || lock != nil || !isHeld {
			t.Fatalf("while the helper holds it: %v %q %v %v", lock, why, isHeld, err)
		}
		if !strings.Contains(why, "pid "+strconv.Itoa(helper.Process.Pid)) {
			t.Fatalf("held, why names no helper: %s", why)
		}
		stdin.Close()
		helper.Wait()
		lock, _, _, err = TakeOwnerLock(path)
		if err != nil || lock == nil {
			t.Fatalf("once the helper let go: %v %v", lock, err)
		}
		lock.Release()
	})
}
