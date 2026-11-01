//go:build !unix

package export

import "os"

// Where there is no flock, restore.lock is its file alone: a reservation
// a killed process left is never taken over, and is removed by hand.

func tryLock(*os.File) (bool, error)  { return false, nil }
func lockWait(*os.File) (bool, error) { return false, nil }
func unlock(*os.File)                 {}
