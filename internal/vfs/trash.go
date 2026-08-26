package vfs

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// TrashRecord describes one item that lives in the recycle bin. The caller
// persists it, usually as a store.TrashItem row, and hands it back to restore
// or purge the item later.
type TrashRecord struct {
	// Name is the original base name of the item.
	Name string
	// OriginalPath is the virtual path the item was deleted from.
	OriginalPath string
	// StoredPath is the absolute on disk path inside the trash directory.
	StoredPath string
	// IsDir reports whether the item is a directory.
	IsDir bool
	// Size is the recursive byte size measured before the move.
	Size int64
}

// trashSlotMode keeps a deleted item as private as the bin itself.
const trashSlotMode fs.FileMode = 0o700

// MoveToTrash moves a path into the Storix recycle bin and returns the record
// the caller should persist. Mount roots are refused, since deleting the mount
// itself would take the user's whole tree with it.
//
// The source side is always read through the guarded root. The destination is
// the Storix owned trash directory, which no mount may reach, so plain os
// calls are used there.
func (v *VFS) MoveToTrash(ctx context.Context, scope Scope, p string) (*TrashRecord, error) {
	if v.opts.TrashDir == "" {
		return nil, errors.New("vfs: trash directory is not configured")
	}
	loc, err := v.ResolveWritable(scope, p)
	if err != nil {
		return nil, err
	}
	if loc.Rel == "." {
		return nil, fmt.Errorf("%w: a mount root cannot be deleted", ErrForbidden)
	}
	info, err := loc.Root.Lstat(loc.Rel)
	if err != nil {
		return nil, mapErr(err)
	}

	name := path.Base(loc.Virtual)
	if err := ValidName(name); err != nil {
		return nil, err
	}
	size, _ := measure(loc.Root, loc.Rel, info)

	id, err := trashID()
	if err != nil {
		return nil, err
	}
	stored := v.TrashItemPath(id, name)
	if !v.insideTrash(stored) {
		return nil, ErrForbidden
	}
	slot := filepath.Dir(stored)
	if err := os.MkdirAll(slot, trashSlotMode); err != nil {
		return nil, fmt.Errorf("vfs: prepare trash slot: %w", err)
	}

	rec := &TrashRecord{
		Name:         name,
		OriginalPath: loc.Virtual,
		StoredPath:   stored,
		IsDir:        info.IsDir(),
		Size:         size,
	}

	// The fast path is a plain rename, which is instant even for a large tree.
	// It only works while both sides sit on the same file system, so any error
	// falls through to a streaming copy that reads through the guarded root.
	if err := os.Rename(loc.OSPath(), stored); err == nil {
		return rec, nil
	}

	if err := copyIntoSlot(ctx, loc, info, slot, name); err != nil {
		_ = os.RemoveAll(slot)
		return nil, err
	}
	if err := loc.Root.RemoveAll(loc.Rel); err != nil {
		// The copy landed but the original survived. Drop the copy so the item
		// is not listed twice and report the real failure.
		_ = os.RemoveAll(slot)
		return nil, mapErr(err)
	}
	return rec, nil
}

// copyIntoSlot streams one item from a guarded mount into a trash slot.
func copyIntoSlot(ctx context.Context, loc *Location, info fs.FileInfo, slot, name string) error {
	slotRoot, err := os.OpenRoot(slot)
	if err != nil {
		return fmt.Errorf("vfs: open trash slot: %w", err)
	}
	defer slotRoot.Close()
	return copyTree(ctx, loc.Root, loc.Rel, slotRoot, name, info, &progressCounter{})
}

// RestoreFromTrash puts an item back where it came from and returns the
// virtual path it now occupies. The parent directory is recreated when it
// disappeared while the item sat in the bin.
//
// ConflictSkip has no useful meaning for a single restore, so it is treated
// like ConflictFail and reports ErrExists.
func (v *VFS) RestoreFromTrash(ctx context.Context, scope Scope, rec TrashRecord, policy Conflict) (string, error) {
	if !v.insideTrash(rec.StoredPath) {
		return "", ErrForbidden
	}
	info, err := os.Lstat(rec.StoredPath)
	if err != nil {
		return "", mapErr(err)
	}
	if policy == "" {
		policy = ConflictRename
	}

	original := Clean(rec.OriginalPath)
	if original == "" {
		return "", ErrInvalidName
	}
	name := path.Base(original)
	if err := ValidName(name); err != nil {
		name = rec.Name
		if err := ValidName(name); err != nil {
			return "", err
		}
	}

	parentLoc, err := v.ResolveWritable(scope, path.Dir(original))
	if err != nil {
		return "", err
	}
	if parentLoc.Rel != "." {
		if err := parentLoc.Root.MkdirAll(parentLoc.Rel, 0o755); err != nil {
			return "", mapErr(err)
		}
	}

	finalName, skip, err := v.resolveConflict(parentLoc, name, policy)
	if err != nil {
		return "", err
	}
	if skip {
		return "", ErrExists
	}
	target, err := v.ResolveWritable(scope, path.Join(parentLoc.Virtual, finalName))
	if err != nil {
		return "", err
	}
	if policy == ConflictOverwrite {
		if err := target.Root.RemoveAll(target.Rel); err != nil && !errors.Is(err, os.ErrNotExist) {
			return "", mapErr(err)
		}
	}

	if err := os.Rename(rec.StoredPath, target.OSPath()); err != nil {
		slot := filepath.Dir(rec.StoredPath)
		slotRoot, openErr := os.OpenRoot(slot)
		if openErr != nil {
			return "", fmt.Errorf("vfs: open trash slot: %w", openErr)
		}
		copyErr := copyTree(ctx, slotRoot, filepath.Base(rec.StoredPath), target.Root, target.Rel, info, &progressCounter{})
		slotRoot.Close()
		if copyErr != nil {
			return "", copyErr
		}
		if err := os.RemoveAll(rec.StoredPath); err != nil {
			return "", fmt.Errorf("vfs: clear trash slot: %w", err)
		}
	}

	// The slot directory only ever holds this one item, so it is empty now.
	v.removeSlot(rec.StoredPath)
	return target.Virtual, nil
}

// PurgeTrash deletes a trashed item for good, together with the slot
// directory that held it.
func (v *VFS) PurgeTrash(rec TrashRecord) error {
	if !v.insideTrash(rec.StoredPath) {
		return ErrForbidden
	}
	if err := os.RemoveAll(rec.StoredPath); err != nil {
		return mapErr(err)
	}
	v.removeSlot(rec.StoredPath)
	return nil
}

// removeSlot drops the per item directory once its content is gone. A slot
// that is not empty, for instance because a concurrent restore is still
// running, is left alone by os.Remove.
func (v *VFS) removeSlot(stored string) {
	slot := filepath.Dir(stored)
	if !v.insideTrash(slot) {
		return
	}
	_ = os.Remove(slot)
}

// TrashUsage reports how many bytes the recycle bin currently holds.
func (v *VFS) TrashUsage() (int64, error) {
	dir := v.opts.TrashDir
	if dir == "" {
		return 0, nil
	}
	var total int64
	err := filepath.WalkDir(filepath.FromSlash(dir), func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			// A slot can vanish under a concurrent purge. Keep counting.
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			return err
		}
		if d.IsDir() || !d.Type().IsRegular() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		total += info.Size()
		return nil
	})
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, nil
		}
		return total, fmt.Errorf("vfs: measure trash: %w", err)
	}
	return total, nil
}

// TrashItemPath builds the on disk path of a trashed item from its slot id and
// its original name. It returns an empty string when either part is unusable,
// which callers must treat as a refusal.
func (v *VFS) TrashItemPath(id, name string) string {
	if v.opts.TrashDir == "" {
		return ""
	}
	id = trashComponent(id)
	name = trashComponent(name)
	if id == "" || name == "" {
		return ""
	}
	return filepath.Join(filepath.FromSlash(v.opts.TrashDir), id, name)
}

// trashComponent reduces a value to a single safe path element.
func trashComponent(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || strings.ContainsRune(value, 0) {
		return ""
	}
	value = filepath.Base(filepath.FromSlash(strings.ReplaceAll(value, "\\", "/")))
	switch value {
	case "", ".", "..", string(filepath.Separator):
		return ""
	}
	return value
}

// insideTrash reports whether a path sits strictly below the trash directory.
// The bin root itself is refused so no call can wipe every slot at once.
func (v *VFS) insideTrash(p string) bool {
	if v.opts.TrashDir == "" || strings.TrimSpace(p) == "" {
		return false
	}
	root := Clean(v.opts.TrashDir)
	cleaned := Clean(p)
	return cleaned != root && Contains(root, cleaned)
}

// trashID returns the random identifier of a new trash slot. A slot per item
// keeps two files with the same name from colliding in the bin.
func trashID() (string, error) {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", fmt.Errorf("vfs: generate trash id: %w", err)
	}
	return hex.EncodeToString(buf[:]), nil
}
