//go:build !unix

package store

// OwnerLock is an owner lock; this platform has none, so no caller holds
// one and each is read-only (TakeOwnerLock).
type OwnerLock struct{}

// TakeOwnerLock answers no lock and why.
func TakeOwnerLock(path string) (*OwnerLock, string, bool, error) {
	return nil, "the owner lock is not supported on this platform", false, nil
}

// Release does nothing.
func (*OwnerLock) Release() {}

// LockHeld tells nothing where there is no owner lock.
func LockHeld(string) (bool, error) { return false, nil }
