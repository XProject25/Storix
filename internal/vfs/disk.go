package vfs

// DiskUsage describes the volume that holds a path. Byte counts come from the
// file system itself, so they include what other users and other services put
// there, exactly like df reports it.
type DiskUsage struct {
	Path       string `json:"path"`
	Filesystem string `json:"filesystem"`

	Total     uint64 `json:"total"`
	Free      uint64 `json:"free"`
	Used      uint64 `json:"used"`
	Available uint64 `json:"available"`

	InodesTotal uint64 `json:"inodesTotal"`
	InodesFree  uint64 `json:"inodesFree"`
	InodesUsed  uint64 `json:"inodesUsed"`

	Percent      float64 `json:"percent"`
	InodePercent float64 `json:"inodePercent"`
}

// Disk reports usage for the volume that holds p.
//
// Platforms without a statfs equivalent report zeros rather than an error, so
// the interface can render the panel everywhere and simply show nothing.
func (v *VFS) Disk(scope Scope, p string) (*DiskUsage, error) {
	loc, err := v.Resolve(scope, p)
	if err != nil {
		return nil, err
	}
	if _, err := loc.Root.Stat(loc.Rel); err != nil {
		return nil, mapErr(err)
	}
	du := &DiskUsage{Path: loc.Virtual}
	if err := diskUsage(loc.OSPath(), du); err != nil {
		return nil, err
	}
	du.derive()
	return du, nil
}

// derive fills the computed fields. Pseudo file systems such as procfs report
// zero blocks and zero inodes, so every ratio is guarded.
func (du *DiskUsage) derive() {
	if du.Total > 0 {
		if du.Free <= du.Total {
			du.Used = du.Total - du.Free
		}
		du.Percent = float64(du.Used) / float64(du.Total) * 100
	}
	if du.InodesTotal > 0 {
		if du.InodesFree <= du.InodesTotal {
			du.InodesUsed = du.InodesTotal - du.InodesFree
		}
		du.InodePercent = float64(du.InodesUsed) / float64(du.InodesTotal) * 100
	}
}
