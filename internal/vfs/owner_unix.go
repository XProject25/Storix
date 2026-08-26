//go:build unix

package vfs

import (
	"io/fs"
	"os/user"
	"strconv"
	"sync"
	"syscall"
)

var (
	ownerMu    sync.RWMutex
	userCache  = map[int]string{}
	groupCache = map[int]string{}
)

// ownerOf resolves the numeric and textual owner of a file. Name lookups are
// cached because a directory listing would otherwise hit NSS once per row.
func ownerOf(info fs.FileInfo) (uid, gid int, owner, group string) {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || st == nil {
		return -1, -1, "", ""
	}
	uid = int(st.Uid)
	gid = int(st.Gid)

	ownerMu.RLock()
	owner, okU := userCache[uid]
	group, okG := groupCache[gid]
	ownerMu.RUnlock()

	if !okU {
		owner = strconv.Itoa(uid)
		if u, err := user.LookupId(strconv.Itoa(uid)); err == nil && u.Username != "" {
			owner = u.Username
		}
		ownerMu.Lock()
		userCache[uid] = owner
		ownerMu.Unlock()
	}
	if !okG {
		group = strconv.Itoa(gid)
		if g, err := user.LookupGroupId(strconv.Itoa(gid)); err == nil && g.Name != "" {
			group = g.Name
		}
		ownerMu.Lock()
		groupCache[gid] = group
		ownerMu.Unlock()
	}
	return uid, gid, owner, group
}

// LookupUID resolves a user name to a numeric id.
func LookupUID(name string) (int, error) {
	u, err := user.Lookup(name)
	if err != nil {
		if n, convErr := strconv.Atoi(name); convErr == nil {
			return n, nil
		}
		return -1, err
	}
	return strconv.Atoi(u.Uid)
}

// LookupGID resolves a group name to a numeric id.
func LookupGID(name string) (int, error) {
	g, err := user.LookupGroup(name)
	if err != nil {
		if n, convErr := strconv.Atoi(name); convErr == nil {
			return n, nil
		}
		return -1, err
	}
	return strconv.Atoi(g.Gid)
}

// nlink reports the hard link count when the platform exposes it.
func nlink(info fs.FileInfo) int {
	if st, ok := info.Sys().(*syscall.Stat_t); ok && st != nil {
		return int(st.Nlink)
	}
	return 1
}
