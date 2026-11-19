package gitcli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"maps"
	"os/exec"

	"github.com/rocksolidlabs/conductor/internal/pty"
)

// Limits bound a RunEach: Out is the most of git's standard output read,
// in bytes, before the run is ended.
type Limits struct {
	Out int64
}

// eachChunk is what RunEach reads of the pipe at a time.
const eachChunk = 32 << 10

// RunEach runs git with args in dir, hardened as Run is, with env set in
// its environment beside the safe one (as RunToEnv sets it), and hands its
// standard output to each record by record, split on NUL (a last record
// with no NUL after it too), as it is read from a pipe: each copies what
// it keeps (the record's bytes are RunEach's own once it returns) and
// answers false once it has all it may take. When each answers false, or
// the output passes lim.Out bytes, the run's own context is cancelled,
// which ends git, the pipe is drained to its end, and RunEach answers
// truncated, with no error: what was handed is what there is. No more
// than one record and 4 KiB of git's standard error are held by RunEach
// itself. A context that ends ends git, and its error is the answer; a
// git that fails says why from its standard error's head, as RunBounded
// does.
func RunEach(ctx context.Context, dir string, env map[string]string, lim Limits, each func(record []byte) bool, args ...string) (truncated bool, err error) {
	argv, err := argvForEnv(ctx, "", []string{"-C", dir}, env, args)
	if err != nil {
		return false, err
	}
	run, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := exec.CommandContext(run, "git", argv...)
	set := safeEnv
	if len(env) > 0 {
		set = maps.Clone(safeEnv)
		maps.Copy(set, env)
	}
	cmd.Env = pty.BuildEnv(pty.ParentEnv(), nil, set, nil)
	stderr := &Head{N: maxBoundedStderr}
	cmd.Stderr = stderr
	out, err := cmd.StdoutPipe()
	if err != nil {
		return false, err
	}
	if err := cmd.Start(); err != nil {
		return false, gitError(ctx, args[0], "", err)
	}
	truncated = readEach(out, lim.Out, each)
	if truncated {
		cancel()
	}
	// Drained to its end, so git, ended or done, is never left blocked on
	// a full pipe, and Wait finds the copy finished.
	_, _ = io.Copy(io.Discard, out)
	werr := cmd.Wait()
	switch {
	case truncated && ctx.Err() == nil:
		return true, nil
	case ctx.Err() != nil:
		return truncated, ctx.Err()
	case werr != nil:
		return false, gitError(ctx, args[0], stderr.String(), werr)
	}
	return false, nil
}

// readEach reads r, handing each NUL-separated record to each, until r
// ends (false), each answers false or more than limit bytes are read
// (true). Only the record being read is held.
func readEach(r io.Reader, limit int64, each func([]byte) bool) bool {
	buf := make([]byte, eachChunk)
	var rec []byte
	var read int64
	for {
		n, err := r.Read(buf)
		if n > 0 {
			read += int64(n)
			if read > limit {
				// Past the bound: the records whole within it are handed,
				// the one it cuts is not.
				n -= int(read - limit)
				handRecords(&rec, buf[:max(n, 0)], each)
				return true
			}
			if !handRecords(&rec, buf[:n], each) {
				return true
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) && len(rec) > 0 {
				return !each(rec)
			}
			return false
		}
	}
}

// handRecords hands each record p completes (rec holding the start of
// the first) to each, keeping the start of the last in rec; false once
// each answers false.
func handRecords(rec *[]byte, p []byte, each func([]byte) bool) bool {
	for {
		i := bytes.IndexByte(p, 0)
		if i < 0 {
			*rec = append(*rec, p...)
			return true
		}
		r := append(*rec, p[:i]...)
		p = p[i+1:]
		if !each(r) {
			return false
		}
		// each copied what it keeps: the next record reuses the buffer.
		*rec = r[:0]
	}
}
