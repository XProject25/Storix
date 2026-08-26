//go:build unix

package updater

import (
	"os"
	"syscall"
)

// binaryOwner reports the numeric owner of a file.
func binaryOwner(path string) (int, bool) {
	info, err := os.Stat(path)
	if err != nil {
		return -1, false
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || st == nil {
		return -1, false
	}
	return int(st.Uid), true
}
