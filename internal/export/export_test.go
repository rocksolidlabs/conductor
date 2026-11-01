package export

import "testing"

// The seams, for the package's tests (package export_test): each set for
// one test and put back at its end.

// SetAfterRecords runs f between the runs' records' copy and the ledger's
// snapshot.
func SetAfterRecords(t testing.TB, f func()) {
	old := afterRecords
	afterRecords = f
	t.Cleanup(func() { afterRecords = old })
}

// SetAfterDispatchSnapshot runs f after each snapshot of Dispatch, with its
// number from 1, before the files it names are copied.
func SetAfterDispatchSnapshot(t testing.TB, f func(n int)) {
	old := afterDispatchSnapshot
	afterDispatchSnapshot = f
	t.Cleanup(func() { afterDispatchSnapshot = old })
}

// SetBeforeRenames runs f once an install's checks have all passed, before
// its first rename; SetBeforeRename before each rename, with its name.
func SetBeforeRenames(t testing.TB, f func()) {
	old := beforeRenames
	beforeRenames = f
	t.Cleanup(func() { beforeRenames = old })
}

func SetBeforeRename(t testing.TB, f func(name string)) {
	old := beforeRename
	beforeRename = f
	t.Cleanup(func() { beforeRename = old })
}

// SetSyncDir runs f before each sync of a directory an install makes, with
// the directory's slash path in the data directory ("." for the data
// directory, .restore-<random>/... for a staged one): an error f returns is
// the sync's.
func SetSyncDir(t testing.TB, f func(rel string) error) {
	old := syncFault
	syncFault = f
	t.Cleanup(func() { syncFault = old })
}
