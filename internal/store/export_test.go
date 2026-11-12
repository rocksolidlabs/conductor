package store

import "testing"

// SetBeforeOpen gives LoadLimit's beforeOpen to the tests of store_test
// until t ends.
func SetBeforeOpen(t *testing.T, fn func(path string)) {
	beforeOpen = fn
	t.Cleanup(func() { beforeOpen = nil })
}
