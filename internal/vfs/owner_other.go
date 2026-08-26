//go:build !unix

package vfs

import (
	"errors"
	"io/fs"
)

// ownerOf has no meaningful answer off Unix; the UI hides the column there.
func ownerOf(fs.FileInfo) (uid, gid int, owner, group string) { return -1, -1, "", "" }

// LookupUID is unsupported off Unix.
func LookupUID(string) (int, error) { return -1, errors.New("vfs: user lookup is Unix only") }

// LookupGID is unsupported off Unix.
func LookupGID(string) (int, error) { return -1, errors.New("vfs: group lookup is Unix only") }

// nlink is always one off Unix.
func nlink(fs.FileInfo) int { return 1 }
