//go:build !unix

package updater

// binaryOwner has no meaningful answer off Unix.
func binaryOwner(string) (int, bool) { return -1, false }
