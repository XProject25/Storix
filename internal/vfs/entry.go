package vfs

import (
	"io/fs"
	"mime"
	"path"
	"sort"
	"strings"
	"time"
)

// Kind is the coarse file family the UI uses to pick an icon and a preview.
type Kind string

// Known kinds.
const (
	KindFolder   Kind = "folder"
	KindImage    Kind = "image"
	KindVideo    Kind = "video"
	KindAudio    Kind = "audio"
	KindPDF      Kind = "pdf"
	KindArchive  Kind = "archive"
	KindCode     Kind = "code"
	KindText     Kind = "text"
	KindDocument Kind = "document"
	KindDisk     Kind = "disk"
	KindFont     Kind = "font"
	KindBinary   Kind = "binary"
	KindOther    Kind = "other"
)

// Entry is a single row in the file browser.
type Entry struct {
	Name        string    `json:"name"`
	Path        string    `json:"path"`
	IsDir       bool      `json:"isDir"`
	Size        int64     `json:"size"`
	Modified    time.Time `json:"modified"`
	Mode        string    `json:"mode"`
	ModeOctal   string    `json:"modeOctal"`
	Owner       string    `json:"owner"`
	Group       string    `json:"group"`
	UID         int       `json:"uid"`
	GID         int       `json:"gid"`
	Kind        Kind      `json:"kind"`
	MIME        string    `json:"mime"`
	Ext         string    `json:"ext"`
	Hidden      bool      `json:"hidden"`
	Symlink     bool      `json:"symlink"`
	LinkTarget  string    `json:"linkTarget,omitempty"`
	Broken      bool      `json:"broken,omitempty"`
	ReadOnly    bool      `json:"readOnly"`
	Previewable bool      `json:"previewable"`
	Editable    bool      `json:"editable"`
	Thumbnail   bool      `json:"thumbnail"`
}

// Listing is a directory read result.
type Listing struct {
	Path      string  `json:"path"`
	Parent    string  `json:"parent"`
	Mount     Mount   `json:"mount"`
	Entries   []Entry `json:"entries"`
	Total     int     `json:"total"`
	Truncated bool    `json:"truncated"`
	Files     int     `json:"files"`
	Folders   int     `json:"folders"`
	Size      int64   `json:"size"`
	ReadOnly  bool    `json:"readOnly"`
	Hidden    int     `json:"hiddenCount"`
}

var codeExts = map[string]bool{
	".go": true, ".js": true, ".jsx": true, ".ts": true, ".tsx": true, ".py": true,
	".rb": true, ".php": true, ".java": true, ".c": true, ".h": true, ".cpp": true,
	".hpp": true, ".cs": true, ".rs": true, ".swift": true, ".kt": true, ".kts": true,
	".sh": true, ".bash": true, ".zsh": true, ".fish": true, ".ps1": true, ".bat": true,
	".sql": true, ".html": true, ".htm": true, ".css": true, ".scss": true, ".sass": true,
	".less": true, ".vue": true, ".svelte": true, ".lua": true, ".pl": true, ".r": true,
	".dart": true, ".scala": true, ".groovy": true, ".m": true, ".mm": true, ".asm": true,
	".tf": true, ".hcl": true, ".proto": true, ".graphql": true, ".gql": true, ".zig": true,
}

var textExts = map[string]bool{
	".txt": true, ".md": true, ".markdown": true, ".log": true, ".json": true,
	".yaml": true, ".yml": true, ".xml": true, ".toml": true, ".ini": true,
	".conf": true, ".cfg": true, ".env": true, ".csv": true, ".tsv": true,
	".properties": true, ".service": true, ".rules": true, ".list": true,
	".gitignore": true, ".dockerignore": true, ".editorconfig": true, ".lock": true,
}

var archiveExts = map[string]bool{
	".zip": true, ".tar": true, ".gz": true, ".tgz": true, ".bz2": true, ".tbz": true,
	".xz": true, ".txz": true, ".7z": true, ".rar": true, ".zst": true, ".lz4": true,
}

var imageExts = map[string]bool{
	".jpg": true, ".jpeg": true, ".png": true, ".gif": true, ".webp": true,
	".bmp": true, ".svg": true, ".ico": true, ".avif": true, ".tif": true, ".tiff": true,
}

var videoExts = map[string]bool{
	".mp4": true, ".webm": true, ".mkv": true, ".mov": true, ".avi": true,
	".m4v": true, ".mpg": true, ".mpeg": true, ".flv": true, ".wmv": true,
}

var audioExts = map[string]bool{
	".mp3": true, ".wav": true, ".flac": true, ".ogg": true, ".oga": true,
	".m4a": true, ".aac": true, ".opus": true, ".wma": true,
}

var docExts = map[string]bool{
	".doc": true, ".docx": true, ".xls": true, ".xlsx": true, ".ppt": true,
	".pptx": true, ".odt": true, ".ods": true, ".odp": true, ".rtf": true, ".epub": true,
}

var diskExts = map[string]bool{
	".iso": true, ".img": true, ".qcow2": true, ".vmdk": true, ".vdi": true, ".dmg": true,
}

var fontExts = map[string]bool{
	".ttf": true, ".otf": true, ".woff": true, ".woff2": true, ".eot": true,
}

// Names without an extension that are still plain text.
var textNames = map[string]bool{
	"dockerfile": true, "makefile": true, "readme": true, "license": true,
	"changelog": true, "authors": true, "notice": true, "procfile": true,
	"caddyfile": true, "vagrantfile": true, "jenkinsfile": true, "gemfile": true,
}

// KindFor classifies a file by name.
func KindFor(name string, isDir bool) Kind {
	if isDir {
		return KindFolder
	}
	lower := strings.ToLower(name)
	ext := path.Ext(lower)
	base := strings.TrimSuffix(lower, ext)
	switch {
	case imageExts[ext]:
		return KindImage
	case videoExts[ext]:
		return KindVideo
	case audioExts[ext]:
		return KindAudio
	case ext == ".pdf":
		return KindPDF
	case archiveExts[ext]:
		// A .tar.gz is an archive either way, but keep .gz of a text file honest.
		return KindArchive
	case codeExts[ext]:
		return KindCode
	case textExts[ext]:
		return KindText
	case docExts[ext]:
		return KindDocument
	case diskExts[ext]:
		return KindDisk
	case fontExts[ext]:
		return KindFont
	case ext == "" && (textNames[lower] || textNames[base]):
		return KindText
	case strings.HasPrefix(lower, ".") && ext == "":
		return KindText
	}
	return KindOther
}

// MIMEFor guesses a content type from the file name.
func MIMEFor(name string, kind Kind) string {
	if kind == KindFolder {
		return "inode/directory"
	}
	ext := strings.ToLower(path.Ext(name))
	if t := mime.TypeByExtension(ext); t != "" {
		return t
	}
	switch kind {
	case KindText, KindCode:
		return "text/plain; charset=utf-8"
	case KindArchive:
		return "application/octet-stream"
	}
	return "application/octet-stream"
}

// previewable reports whether the browser can display the file inline.
func previewable(kind Kind) bool {
	switch kind {
	case KindImage, KindVideo, KindAudio, KindPDF, KindText, KindCode:
		return true
	}
	return false
}

// editable reports whether the built in editor should offer to open the file.
func editable(kind Kind, size, max int64) bool {
	if size > max {
		return false
	}
	return kind == KindText || kind == KindCode
}

// thumbnailable reports whether a preview image can be rendered server side.
func thumbnailable(kind Kind, name string) bool {
	if kind != KindImage {
		return false
	}
	switch strings.ToLower(path.Ext(name)) {
	case ".jpg", ".jpeg", ".png", ".gif", ".webp", ".bmp", ".tif", ".tiff":
		return true
	}
	return false
}

// modeString renders a Unix style permission string.
func modeString(m fs.FileMode) string { return m.String() }

// SortEntries orders a listing. Folders always lead unless mixed is set.
func SortEntries(entries []Entry, field, order string, foldersFirst bool) {
	desc := strings.EqualFold(order, "desc")
	less := func(i, j int) bool {
		a, b := entries[i], entries[j]
		if foldersFirst && a.IsDir != b.IsDir {
			return a.IsDir
		}
		var out bool
		switch strings.ToLower(field) {
		case "size":
			if a.Size == b.Size {
				out = strings.ToLower(a.Name) < strings.ToLower(b.Name)
			} else {
				out = a.Size < b.Size
			}
		case "modified", "date":
			if a.Modified.Equal(b.Modified) {
				out = strings.ToLower(a.Name) < strings.ToLower(b.Name)
			} else {
				out = a.Modified.Before(b.Modified)
			}
		case "kind", "type":
			if a.Kind == b.Kind {
				out = strings.ToLower(a.Name) < strings.ToLower(b.Name)
			} else {
				out = a.Kind < b.Kind
			}
		case "ext":
			if a.Ext == b.Ext {
				out = strings.ToLower(a.Name) < strings.ToLower(b.Name)
			} else {
				out = a.Ext < b.Ext
			}
		default:
			out = naturalLess(a.Name, b.Name)
		}
		if desc && (foldersFirst && a.IsDir == b.IsDir || !foldersFirst) {
			return !out
		}
		return out
	}
	sort.SliceStable(entries, less)
}

// naturalLess compares names so file10 sorts after file9.
func naturalLess(a, b string) bool {
	la, lb := strings.ToLower(a), strings.ToLower(b)
	i, j := 0, 0
	for i < len(la) && j < len(lb) {
		ca, cb := la[i], lb[j]
		if isDigit(ca) && isDigit(cb) {
			si, sj := i, j
			for i < len(la) && isDigit(la[i]) {
				i++
			}
			for j < len(lb) && isDigit(lb[j]) {
				j++
			}
			na := strings.TrimLeft(la[si:i], "0")
			nb := strings.TrimLeft(lb[sj:j], "0")
			if len(na) != len(nb) {
				return len(na) < len(nb)
			}
			if na != nb {
				return na < nb
			}
			continue
		}
		if ca != cb {
			return ca < cb
		}
		i++
		j++
	}
	return len(la)-i < len(lb)-j
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }
