//go:build !unix

package vfs

import "path/filepath"

// diskUsage has no portable answer outside the Unix family. It reports a best
// effort result with zero counters and no error, so a development run on
// another platform still renders the storage panel instead of failing the
// request.
func diskUsage(osPath string, du *DiskUsage) error {
	du.Filesystem = filepath.VolumeName(osPath)
	return nil
}
