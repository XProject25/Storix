//go:build unix

package vfs

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// statfsInt covers the integer widths the statfs fields use across the Unix
// family, so the arithmetic below stays portable without a build tag per
// operating system.
type statfsInt interface {
	~int32 | ~int64 | ~uint32 | ~uint64
}

// u64 widens a statfs field. A negative count, which some BSD kernels report
// for an over committed reserve, is clamped to zero instead of wrapping into
// an absurd number of bytes.
func u64[T statfsInt](v T) uint64 {
	if v < 0 {
		return 0
	}
	return uint64(v)
}

// diskUsage fills a DiskUsage from statfs.
func diskUsage(osPath string, du *DiskUsage) error {
	var st unix.Statfs_t
	if err := unix.Statfs(osPath, &st); err != nil {
		return mapErr(err)
	}
	bsize := u64(st.Bsize)
	du.Total = u64(st.Blocks) * bsize
	du.Free = u64(st.Bfree) * bsize
	du.Available = u64(st.Bavail) * bsize
	du.InodesTotal = u64(st.Files)
	du.InodesFree = u64(st.Ffree)
	du.Filesystem = filesystemName(osPath, u64(st.Type))
	return nil
}

// procMounts is the kernel view of what is mounted where.
const procMounts = "/proc/self/mounts"

// filesystemName names the volume the way df does: the backing device when the
// kernel exposes one, its type otherwise. The mount table is read first because
// it is precise; the file system magic is only a fallback for systems that do
// not publish one.
func filesystemName(osPath string, magic uint64) string {
	if source, fstype := lookupMount(osPath); source != "" || fstype != "" {
		if strings.HasPrefix(source, "/") {
			return source
		}
		if fstype != "" {
			return fstype
		}
		return source
	}
	return magicName(magic)
}

// lookupMount finds the longest mount point that is a prefix of the path.
func lookupMount(osPath string) (source, fstype string) {
	abs, err := filepath.Abs(osPath)
	if err != nil {
		abs = osPath
	}
	abs = Clean(abs)

	f, err := os.Open(procMounts)
	if err != nil {
		return "", ""
	}
	defer f.Close()

	best := 0
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 3 {
			continue
		}
		point := Clean(unescapeMount(fields[1]))
		if point == "" || !Contains(point, abs) {
			continue
		}
		if len(point) < best {
			continue
		}
		best = len(point)
		source = unescapeMount(fields[0])
		fstype = fields[2]
	}
	if err := scanner.Err(); err != nil {
		return "", ""
	}
	return source, fstype
}

// unescapeMount decodes the octal escapes the kernel writes for the space,
// tab, newline and backslash characters in a mount table field.
func unescapeMount(field string) string {
	if !strings.Contains(field, `\`) {
		return field
	}
	var b strings.Builder
	b.Grow(len(field))
	for i := 0; i < len(field); i++ {
		if field[i] != '\\' || i+3 >= len(field) {
			b.WriteByte(field[i])
			continue
		}
		var value byte
		valid := true
		for _, c := range []byte(field[i+1 : i+4]) {
			if c < '0' || c > '7' {
				valid = false
				break
			}
			value = value<<3 | (c - '0')
		}
		if !valid {
			b.WriteByte(field[i])
			continue
		}
		b.WriteByte(value)
		i += 3
	}
	return b.String()
}

// magicNames maps the Linux file system magic numbers that a server is likely
// to be running on. Other Unix kernels report a small type index here instead,
// which does not collide with these values in practice and simply yields an
// empty name.
var magicNames = map[uint64]string{
	0xef53:     "ext4",
	0x58465342: "xfs",
	0x9123683e: "btrfs",
	0x01021994: "tmpfs",
	0x858458f6: "ramfs",
	0x794c7630: "overlay",
	0x2fc12fc1: "zfs",
	0x6969:     "nfs",
	0x517b:     "smb",
	0xfe534d42: "smb2",
	0x65735546: "fuse",
	0x73717368: "squashfs",
	0x4d44:     "vfat",
	0x2011bab0: "exfat",
	0x5346544e: "ntfs",
	0xf2f52010: "f2fs",
	0xe0f5e1e2: "erofs",
	0x3153464a: "jfs",
	0x52654973: "reiserfs",
	0x9660:     "iso9660",
	0x24051905: "ubifs",
	0x1cd1:     "devpts",
	0x9fa0:     "proc",
	0x62656572: "sysfs",
	0x27e0eb:   "cgroup",
	0x63677270: "cgroup2",
}

// magicName resolves a file system magic number to a short name.
func magicName(magic uint64) string {
	if name, ok := magicNames[magic]; ok {
		return name
	}
	return ""
}
