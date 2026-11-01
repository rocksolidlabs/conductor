//go:build unix && !linux && !darwin

package export

import "os"

// renameNoReplace moves staging/name to name in root, refusing a name
// already there (renameByHand).
func renameNoReplace(root *os.Root, staging, name string) error {
	return renameByHand(root, staging, name)
}
