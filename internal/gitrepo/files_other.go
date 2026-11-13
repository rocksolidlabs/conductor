//go:build !unix

package gitrepo

import "os"

// openFlags open a file for reading. These systems (Windows) have no flags
// to open without blocking or without taking a terminal, so careOS's looks
// at the file before and after the open are the whole of it here.
const openFlags = os.O_RDONLY
