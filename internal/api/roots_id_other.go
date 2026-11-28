//go:build !unix

package api

import "os"

// folderOwner is -1: this system names no owner in a stat.
func folderOwner(os.FileInfo) int { return -1 }

// serverUID is -1, as folderOwner.
func serverUID() int { return -1 }
