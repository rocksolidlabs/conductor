package crew

import "os"

// OpenRoot opens the tree's directory as an os.Root, where its read runs
// (on Linux the open directory itself), for a look-up in the tree that no
// link leads out of: a root that is not the directory OpenTree opened is
// ErrTreeMoved.
func (t *Tree) OpenRoot() (*os.Root, error) {
	r, err := os.OpenRoot(t.at)
	if err != nil {
		return nil, err
	}
	want, err := t.dir.Stat()
	if err == nil {
		var got os.FileInfo
		if got, err = r.Stat("."); err == nil && !os.SameFile(want, got) {
			err = ErrTreeMoved
		}
	}
	if err != nil {
		r.Close()
		return nil, err
	}
	return r, nil
}
