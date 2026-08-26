// Package store is the SQLite persistence layer for Storix.
//
// Storix - modern web file manager for servers.
// Developed by X Project.
package store

import (
	"slices"
	"strings"
	"time"
)

// Role is a coarse permission preset assigned to a user.
type Role string

// Built in roles.
const (
	RoleAdmin    Role = "admin"
	RoleManager  Role = "manager"
	RoleUser     Role = "user"
	RoleReadOnly Role = "readonly"
	RoleCustom   Role = "custom"
)

// Valid reports whether the role is one Storix knows.
func (r Role) Valid() bool {
	switch r {
	case RoleAdmin, RoleManager, RoleUser, RoleReadOnly, RoleCustom:
		return true
	}
	return false
}

// Label is the human readable role name.
func (r Role) Label() string {
	switch r {
	case RoleAdmin:
		return "Administrator"
	case RoleManager:
		return "Manager"
	case RoleUser:
		return "User"
	case RoleReadOnly:
		return "Read only"
	case RoleCustom:
		return "Custom"
	}
	return string(r)
}

// Permission is a single capability a user may hold.
type Permission string

// The complete permission set. Keep in sync with the frontend.
const (
	PermView     Permission = "view"
	PermDownload Permission = "download"
	PermUpload   Permission = "upload"
	PermCreate   Permission = "create"
	PermRename   Permission = "rename"
	PermMove     Permission = "move"
	PermCopy     Permission = "copy"
	PermDelete   Permission = "delete"
	PermShare    Permission = "share"
	PermArchive  Permission = "archive"
	PermEdit     Permission = "edit"
	PermAdvanced Permission = "advanced"
	PermUsers    Permission = "users"
	PermSettings Permission = "settings"
)

// AllPermissions lists every permission in display order.
func AllPermissions() []Permission {
	return []Permission{
		PermView, PermDownload, PermUpload, PermCreate, PermRename,
		PermMove, PermCopy, PermDelete, PermShare, PermArchive,
		PermEdit, PermAdvanced, PermUsers, PermSettings,
	}
}

// PermissionsForRole returns the default permission set of a role.
func PermissionsForRole(r Role) []Permission {
	switch r {
	case RoleAdmin:
		return AllPermissions()
	case RoleManager:
		return []Permission{
			PermView, PermDownload, PermUpload, PermCreate, PermRename,
			PermMove, PermCopy, PermDelete, PermShare, PermArchive, PermEdit,
		}
	case RoleUser:
		return []Permission{
			PermView, PermDownload, PermUpload, PermCreate, PermRename, PermCopy, PermArchive,
		}
	case RoleReadOnly:
		return []Permission{PermView, PermDownload}
	}
	return []Permission{PermView}
}

// User is a Storix account.
type User struct {
	ID                 int64        `json:"id"`
	Username           string       `json:"username"`
	DisplayName        string       `json:"displayName"`
	Email              string       `json:"email"`
	PasswordHash       string       `json:"-"`
	Role               Role         `json:"role"`
	Permissions        []Permission `json:"permissions"`
	TOTPSecret         string       `json:"-"`
	TOTPEnabled        bool         `json:"totpEnabled"`
	Active             bool         `json:"active"`
	MustChangePassword bool         `json:"mustChangePassword"`
	Theme              string       `json:"theme"`
	Locale             string       `json:"locale"`
	Quota              int64        `json:"quota"`
	FailedLogins       int          `json:"failedLogins"`
	LockedUntil        *time.Time   `json:"lockedUntil,omitempty"`
	LastLoginAt        *time.Time   `json:"lastLoginAt,omitempty"`
	LastLoginIP        string       `json:"lastLoginIp,omitempty"`
	CreatedAt          time.Time    `json:"createdAt"`
	UpdatedAt          time.Time    `json:"updatedAt"`
	Mounts             []Mount      `json:"mounts"`
}

// Can reports whether the user holds a permission. Administrators hold all.
func (u *User) Can(p Permission) bool {
	if u == nil {
		return false
	}
	if u.Role == RoleAdmin {
		return true
	}
	return slices.Contains(u.Permissions, p)
}

// IsAdmin reports administrator status.
func (u *User) IsAdmin() bool { return u != nil && u.Role == RoleAdmin }

// Mount is a directory a user is allowed to work in.
type Mount struct {
	ID        int64     `json:"id"`
	UserID    int64     `json:"userId"`
	Path      string    `json:"path"`
	Label     string    `json:"label"`
	Icon      string    `json:"icon"`
	ReadOnly  bool      `json:"readOnly"`
	SortOrder int       `json:"sortOrder"`
	CreatedAt time.Time `json:"createdAt"`
}

// Root is a directory tree the administrator exposed to Storix. Every user
// mount has to live inside one of these.
type Root struct {
	ID        int64     `json:"id"`
	Path      string    `json:"path"`
	Label     string    `json:"label"`
	Icon      string    `json:"icon"`
	ReadOnly  bool      `json:"readOnly"`
	SortOrder int       `json:"sortOrder"`
	CreatedAt time.Time `json:"createdAt"`
}

// Session is a signed in browser session.
type Session struct {
	ID         string    `json:"id"`
	UserID     int64     `json:"userId"`
	CSRF       string    `json:"-"`
	IP         string    `json:"ip"`
	UserAgent  string    `json:"userAgent"`
	CreatedAt  time.Time `json:"createdAt"`
	LastSeenAt time.Time `json:"lastSeenAt"`
	ExpiresAt  time.Time `json:"expiresAt"`
}

// ShareKind distinguishes a published folder from an upload request.
type ShareKind string

// Share kinds.
const (
	ShareDownload ShareKind = "download"
	ShareUpload   ShareKind = "upload"
)

// Share is a public link.
type Share struct {
	ID            int64      `json:"id"`
	Token         string     `json:"token"`
	OwnerID       int64      `json:"ownerId"`
	OwnerName     string     `json:"ownerName,omitempty"`
	Path          string     `json:"path"`
	Name          string     `json:"name"`
	Kind          ShareKind  `json:"kind"`
	IsDir         bool       `json:"isDir"`
	PasswordHash  string     `json:"-"`
	HasPassword   bool       `json:"hasPassword"`
	AllowDownload bool       `json:"allowDownload"`
	AllowUpload   bool       `json:"allowUpload"`
	AllowList     bool       `json:"allowList"`
	MaxDownloads  int        `json:"maxDownloads"`
	Downloads     int        `json:"downloads"`
	Note          string     `json:"note"`
	ExpiresAt     *time.Time `json:"expiresAt,omitempty"`
	LastAccessAt  *time.Time `json:"lastAccessAt,omitempty"`
	CreatedAt     time.Time  `json:"createdAt"`
}

// Expired reports whether the share is past its expiry or download budget.
func (s *Share) Expired(now time.Time) bool {
	if s == nil {
		return true
	}
	if s.ExpiresAt != nil && now.After(*s.ExpiresAt) {
		return true
	}
	if s.MaxDownloads > 0 && s.Downloads >= s.MaxDownloads {
		return true
	}
	return false
}

// TrashItem is a file or folder moved to the recycle bin.
type TrashItem struct {
	ID           int64     `json:"id"`
	UserID       int64     `json:"userId"`
	Name         string    `json:"name"`
	OriginalPath string    `json:"originalPath"`
	StoredPath   string    `json:"-"`
	IsDir        bool      `json:"isDir"`
	Size         int64     `json:"size"`
	DeletedAt    time.Time `json:"deletedAt"`
	ExpiresAt    time.Time `json:"expiresAt"`
}

// Favorite is a pinned location.
type Favorite struct {
	ID        int64     `json:"id"`
	UserID    int64     `json:"userId"`
	Path      string    `json:"path"`
	Name      string    `json:"name"`
	IsDir     bool      `json:"isDir"`
	CreatedAt time.Time `json:"createdAt"`
}

// Recent is a recently touched file.
type Recent struct {
	ID     int64     `json:"id"`
	UserID int64     `json:"userId"`
	Path   string    `json:"path"`
	Name   string    `json:"name"`
	IsDir  bool      `json:"isDir"`
	Size   int64     `json:"size"`
	Action string    `json:"action"`
	At     time.Time `json:"at"`
}

// AuditEntry records a security relevant action.
type AuditEntry struct {
	ID       int64     `json:"id"`
	UserID   int64     `json:"userId"`
	Username string    `json:"username"`
	Action   string    `json:"action"`
	Target   string    `json:"target"`
	Detail   string    `json:"detail"`
	IP       string    `json:"ip"`
	UA       string    `json:"userAgent"`
	OK       bool      `json:"ok"`
	At       time.Time `json:"at"`
}

// UploadSession is one resumable (tus) upload in flight.
type UploadSession struct {
	ID         string    `json:"id"`
	UserID     int64     `json:"userId"`
	ShareToken string    `json:"shareToken,omitempty"`
	TargetDir  string    `json:"targetDir"`
	Filename   string    `json:"filename"`
	RelPath    string    `json:"relPath"`
	Size       int64     `json:"size"`
	Offset     int64     `json:"offset"`
	TempPath   string    `json:"-"`
	Metadata   string    `json:"metadata"`
	Overwrite  bool      `json:"overwrite"`
	Completed  bool      `json:"completed"`
	FinalPath  string    `json:"finalPath,omitempty"`
	CreatedAt  time.Time `json:"createdAt"`
	UpdatedAt  time.Time `json:"updatedAt"`
	ExpiresAt  time.Time `json:"expiresAt"`
}

// JobStatus is the lifecycle state of a background operation.
type JobStatus string

// Job states.
const (
	JobQueued   JobStatus = "queued"
	JobRunning  JobStatus = "running"
	JobDone     JobStatus = "done"
	JobFailed   JobStatus = "failed"
	JobCanceled JobStatus = "canceled"
)

// Job is a long running file operation such as copy, move or extract.
type Job struct {
	ID          string     `json:"id"`
	UserID      int64      `json:"userId"`
	Type        string     `json:"type"`
	Status      JobStatus  `json:"status"`
	Title       string     `json:"title"`
	Message     string     `json:"message"`
	Error       string     `json:"error,omitempty"`
	Total       int64      `json:"total"`
	Done        int64      `json:"done"`
	TotalItems  int64      `json:"totalItems"`
	DoneItems   int64      `json:"doneItems"`
	Params      string     `json:"-"`
	Result      string     `json:"result,omitempty"`
	CreatedAt   time.Time  `json:"createdAt"`
	UpdatedAt   time.Time  `json:"updatedAt"`
	StartedAt   *time.Time `json:"startedAt,omitempty"`
	FinishedAt  *time.Time `json:"finishedAt,omitempty"`
	Cancellable bool       `json:"cancellable"`
}

// Percent is the completion ratio in the 0..100 range. An estimate made
// before the work started can undercount, so the value is clamped rather than
// allowed to report more than a finished job.
func (j *Job) Percent() float64 {
	if j == nil {
		return 0
	}
	if j.Status == JobDone {
		return 100
	}
	var ratio float64
	switch {
	case j.Total > 0:
		ratio = float64(j.Done) / float64(j.Total) * 100
	case j.TotalItems > 0:
		ratio = float64(j.DoneItems) / float64(j.TotalItems) * 100
	default:
		return 0
	}
	return min(max(ratio, 0), 100)
}

// Branding holds the customizable product identity shown in the UI.
type Branding struct {
	Name       string `json:"name"`
	Tagline    string `json:"tagline"`
	LogoURL    string `json:"logoUrl"`
	AccentFrom string `json:"accentFrom"`
	AccentTo   string `json:"accentTo"`
	Footer     string `json:"footer"`
}

// DefaultBranding is the stock X Project identity.
func DefaultBranding() Branding {
	return Branding{
		Name:       "Storix",
		Tagline:    "Fast. Secure. Powerful.",
		AccentFrom: "#00D4FF",
		AccentTo:   "#7C3AED",
		Footer:     "Developed by X Project",
	}
}

// NormalizePermissions removes duplicates and unknown entries, preserving
// the canonical display order.
func NormalizePermissions(in []Permission) []Permission {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[Permission]bool, len(in))
	for _, p := range in {
		seen[Permission(strings.ToLower(strings.TrimSpace(string(p))))] = true
	}
	out := make([]Permission, 0, len(seen))
	for _, p := range AllPermissions() {
		if seen[p] {
			out = append(out, p)
		}
	}
	return out
}
