package vfs

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// ErrInvalidRule reports a rename rule that cannot be applied as written, such
// as a regular expression that does not compile. It describes the rule itself,
// while ErrInvalidName describes a single name the rule produced.
var ErrInvalidRule = errors.New("vfs: invalid rename rule")

// RenameMode selects how a bulk rename builds the new name.
type RenameMode string

// The rename modes the interface offers.
const (
	RenameReplace RenameMode = "replace"
	RenamePrefix  RenameMode = "prefix"
	RenameSuffix  RenameMode = "suffix"
	RenameNumber  RenameMode = "number"
	RenameCase    RenameMode = "case"
)

// Reasons shown next to a row that cannot be renamed.
const (
	rnReasonInvalid  = "that name is not allowed"
	rnReasonReserved = "that name is reserved by Storix"
	rnReasonBatch    = "another item in this batch takes that name"
	rnReasonExists   = "a file with that name is already here"
)

// rnMaxPadding caps the zero padding of the counter, so a rule cannot ask for
// a name made of thousands of zeroes.
const rnMaxPadding = 12

// RenameRule describes one bulk rename. Only the fields the chosen mode uses
// are read, so the interface can leave the rest at their zero value.
type RenameRule struct {
	Mode RenameMode `json:"mode"`

	// Replace mode. Find is a plain substring unless Regex is set, and the
	// match ignores case unless CaseSensitive is set.
	Find          string `json:"find"`
	Replace       string `json:"replace"`
	Regex         bool   `json:"regex"`
	CaseSensitive bool   `json:"caseSensitive"`

	// Prefix and suffix modes add Text.
	Text string `json:"text"`

	// Number mode. Pattern takes {n} for the counter and {name} for the
	// original base name, for instance "holiday-{n}". Start defaults to 1 and
	// Padding pads the counter with zeroes, so a padding of 3 gives 001.
	Start   int    `json:"start"`
	Padding int    `json:"padding"`
	Pattern string `json:"pattern"`

	// Case mode: lower, upper or title. The extension is always lowercased.
	Casing string `json:"casing"`

	// KeepExtension leaves the extension alone, so a suffix lands before it
	// and "photo.jpg" plus "-edited" becomes "photo-edited.jpg".
	KeepExtension bool `json:"keepExtension"`
}

// RenameChange is one row of a bulk rename, before or after it ran.
type RenameChange struct {
	Path      string `json:"path"`
	From      string `json:"from"`
	To        string `json:"to"`
	Conflict  bool   `json:"conflict"`
	Unchanged bool   `json:"unchanged"`
	Reason    string `json:"reason,omitempty"`
}

// RenamePreview is the dry run the interface shows before anything moves.
type RenamePreview struct {
	Changes   []RenameChange `json:"changes"`
	Conflicts int            `json:"conflicts"`
	Unchanged int            `json:"unchanged"`
	Valid     int            `json:"valid"`
}

// rnPlan is one prepared rename inside a single guarded root.
type rnPlan struct {
	loc       *Location
	parentRel string
	// currentRel is where the item sits right now, which is the source path
	// until the batch parks it under a temporary name.
	currentRel string
	targetRel  string
	parked     bool
	// checked caches the on disk lookup, so repeated conflict passes over a
	// large batch do not repeat a syscall per item.
	checked bool
	taken   bool
	change  RenameChange
}

// PreviewRename works out what a rule would do to a selection without touching
// anything on disk. Items that cannot be renamed are reported row by row, so
// the interface can show the user exactly which ones need attention.
func (v *VFS) PreviewRename(scope Scope, paths []string, rule RenameRule) (*RenamePreview, error) {
	plans, err := v.rnPlanBatch(scope, paths, rule)
	if err != nil {
		return nil, err
	}
	out := &RenamePreview{Changes: make([]RenameChange, 0, len(plans))}
	for _, p := range plans {
		out.Changes = append(out.Changes, p.change)
		switch {
		case p.change.Conflict:
			out.Conflicts++
		case p.change.Unchanged:
			out.Unchanged++
		default:
			out.Valid++
		}
	}
	return out, nil
}

// ApplyRename performs a bulk rename and reports how many items moved along
// with the rows that could not. The batch is ordered so a name still held by
// another item is freed first, through a temporary name when the wanted names
// form a cycle, and every rename goes through the guarded root, so nothing can
// land outside the mount and no file is quietly written over.
func (v *VFS) ApplyRename(ctx context.Context, scope Scope, paths []string, rule RenameRule) (int, []RenameChange, error) {
	plans, err := v.rnPlanBatch(scope, paths, rule)
	if err != nil {
		return 0, nil, err
	}

	failures := make([]RenameChange, 0)
	pending := make([]*rnPlan, 0, len(plans))
	held := make(map[string]bool, len(plans))
	for _, p := range plans {
		switch {
		case p.change.Conflict:
			failures = append(failures, p.change)
		case p.change.Unchanged:
			// A file is never renamed onto itself.
		default:
			pending = append(pending, p)
			held[rnKey(p.loc, p.change.From)] = true
		}
	}

	renamed := 0
	for len(pending) > 0 {
		if err := ctx.Err(); err != nil {
			return renamed, failures, err
		}
		next := make([]*rnPlan, 0, len(pending))
		moved := false
		for _, p := range pending {
			if held[rnKey(p.loc, p.change.To)] {
				// The wanted name still belongs to another item in this batch.
				next = append(next, p)
				continue
			}
			if err := p.loc.Root.Rename(p.currentRel, p.targetRel); err != nil {
				rnRestore(p)
				failures = append(failures, rnRefused(p, rnMessage(err)))
				delete(held, rnKey(p.loc, p.change.From))
				moved = true
				continue
			}
			delete(held, rnKey(p.loc, p.change.From))
			renamed++
			moved = true
		}
		pending = next
		if moved || len(pending) == 0 {
			continue
		}
		// Nothing could move, so the remaining items want names each other
		// still holds. Park the first one under a temporary name to break the
		// cycle; the next pass then has somewhere to go.
		p := pending[0]
		if err := v.rnPark(p); err != nil {
			failures = append(failures, rnRefused(p, rnMessage(err)))
			pending = pending[1:]
		}
		delete(held, rnKey(p.loc, p.change.From))
	}
	return renamed, failures, nil
}

// rnPlanBatch resolves a selection, works out the new name for every item and
// marks the rows that cannot be renamed.
func (v *VFS) rnPlanBatch(scope Scope, paths []string, rule RenameRule) ([]*rnPlan, error) {
	rewrite, err := rnRewriter(rule)
	if err != nil {
		return nil, err
	}
	plans := make([]*rnPlan, 0, len(paths))
	for i, raw := range paths {
		loc, err := v.ResolveWritable(scope, raw)
		if err != nil {
			return nil, err
		}
		if loc.Rel == "." {
			return nil, fmt.Errorf("%w: a mounted folder cannot be renamed here", ErrForbidden)
		}
		from := path.Base(loc.Virtual)
		p := &rnPlan{
			loc:        loc,
			parentRel:  rnParentRel(loc.Rel),
			currentRel: loc.Rel,
			change:     RenameChange{Path: loc.Virtual, From: from, To: rewrite(from, i)},
		}
		switch {
		case p.change.To == p.change.From:
			p.change.Unchanged = true
		case ValidName(p.change.To) != nil:
			rnRefuse(p, rnReasonInvalid)
		case IsInternal(p.change.To):
			rnRefuse(p, rnReasonReserved)
		default:
			p.targetRel = joinRel(p.parentRel, p.change.To)
		}
		plans = append(plans, p)
	}
	if err := v.rnResolveConflicts(plans); err != nil {
		return nil, err
	}
	return plans, nil
}

// rnResolveConflicts marks every row whose new name is already taken, either by
// something on disk or by another row of the same batch. A name held by an item
// that is itself being renamed away is free to take, which is what makes a swap
// work. Refusing one row can take the name it would have freed off the table
// again, so the pass repeats until nothing new is refused.
func (v *VFS) rnResolveConflicts(plans []*rnPlan) error {
	for {
		vacating := make(map[string]bool, len(plans))
		for _, p := range plans {
			if p.change.Conflict || p.change.Unchanged {
				continue
			}
			vacating[rnKey(p.loc, p.change.From)] = true
		}
		claimed := make(map[string]bool, len(plans))
		refused := false
		for _, p := range plans {
			if p.change.Conflict || p.change.Unchanged {
				continue
			}
			key := rnKey(p.loc, p.change.To)
			if claimed[key] {
				rnRefuse(p, rnReasonBatch)
				refused = true
				continue
			}
			taken, err := v.rnTaken(p)
			if err != nil {
				return err
			}
			if taken && !vacating[key] {
				rnRefuse(p, rnReasonExists)
				refused = true
				continue
			}
			claimed[key] = true
		}
		if !refused {
			return nil
		}
	}
}

// rnTaken reports whether the new name already exists on disk. A change of case
// alone points back at the source itself on a case insensitive file system,
// which is a rename to perform rather than a collision.
func (v *VFS) rnTaken(p *rnPlan) (bool, error) {
	if p.checked {
		return p.taken, nil
	}
	info, err := p.loc.Root.Lstat(p.targetRel)
	switch {
	case errors.Is(err, os.ErrNotExist):
		p.checked, p.taken = true, false
		return false, nil
	case err != nil:
		return false, mapErr(err)
	}
	p.checked, p.taken = true, true
	if self, err := p.loc.Root.Lstat(p.currentRel); err == nil && os.SameFile(info, self) {
		p.taken = false
	}
	return p.taken, nil
}

// rnPark moves an item aside under a temporary name so the item that wants its
// name can take it. The name carries the Storix prefix, so a batch interrupted
// at the wrong moment leaves something recognisable rather than a stray file.
func (v *VFS) rnPark(p *rnPlan) error {
	for i := 0; i < 100; i++ {
		rel := joinRel(p.parentRel, fmt.Sprintf("%srename-%d-%d", InternalPrefix, time.Now().UnixNano(), i))
		if _, err := p.loc.Root.Lstat(rel); err == nil {
			continue
		} else if !errors.Is(err, os.ErrNotExist) {
			return mapErr(err)
		}
		if err := p.loc.Root.Rename(p.currentRel, rel); err != nil {
			return mapErr(err)
		}
		p.currentRel = rel
		p.parked = true
		return nil
	}
	return ErrExists
}

// rnRestore puts a parked item back under its own name after a failure, best
// effort, so a batch that goes wrong does not leave a temporary name behind.
func rnRestore(p *rnPlan) {
	if !p.parked {
		return
	}
	if err := p.loc.Root.Rename(p.currentRel, p.loc.Rel); err == nil {
		p.currentRel = p.loc.Rel
		p.parked = false
	}
}

// rnRefuse marks one row as a conflict with the reason to show.
func rnRefuse(p *rnPlan, reason string) {
	p.change.Conflict = true
	p.change.Reason = reason
}

// rnRefused marks a row and returns it, for the failure report.
func rnRefused(p *rnPlan, reason string) RenameChange {
	rnRefuse(p, reason)
	return p.change
}

// rnKey identifies a name inside one folder of one mount.
func rnKey(loc *Location, name string) string {
	return loc.Mount.Path + "\x00" + joinRel(rnParentRel(loc.Rel), name)
}

// rnParentRel is the folder holding a relative path, "." at the mount itself.
func rnParentRel(rel string) string {
	dir := path.Dir(rel)
	if dir == "" || dir == "/" {
		return "."
	}
	return dir
}

// rnRewriter checks a rule and returns the function that builds a new name from
// an old one. index is the position of the item in the batch, counting from
// zero, which is what the counter of the numbering mode follows.
func rnRewriter(rule RenameRule) (func(name string, index int) string, error) {
	switch rule.Mode {
	case RenameReplace:
		return rnReplacer(rule)

	case RenamePrefix:
		if rule.Text == "" {
			return nil, fmt.Errorf("%w: enter the text to add", ErrInvalidRule)
		}
		return func(name string, _ int) string { return rule.Text + name }, nil

	case RenameSuffix:
		if rule.Text == "" {
			return nil, fmt.Errorf("%w: enter the text to add", ErrInvalidRule)
		}
		return func(name string, _ int) string {
			return rnOnPart(name, rule.KeepExtension, func(part string) string { return part + rule.Text })
		}, nil

	case RenameNumber:
		return rnNumberer(rule)

	case RenameCase:
		casing := strings.ToLower(strings.TrimSpace(rule.Casing))
		switch casing {
		case "lower", "upper", "title":
		default:
			return nil, fmt.Errorf("%w: choose lower, upper or title", ErrInvalidRule)
		}
		return func(name string, _ int) string {
			base, ext := rnSplitName(name)
			switch casing {
			case "upper":
				base = strings.ToUpper(base)
			case "title":
				base = rnTitle(base)
			default:
				base = strings.ToLower(base)
			}
			return base + strings.ToLower(ext)
		}, nil

	case "":
		return nil, fmt.Errorf("%w: choose what the rename should do", ErrInvalidRule)
	}
	return nil, fmt.Errorf("%w: %q is not a rename this server knows", ErrInvalidRule, string(rule.Mode))
}

// rnReplacer builds the find and replace rewriter.
func rnReplacer(rule RenameRule) (func(name string, index int) string, error) {
	if rule.Find == "" {
		return nil, fmt.Errorf("%w: enter the text to look for", ErrInvalidRule)
	}
	expr := rule.Find
	if !rule.Regex {
		if rule.CaseSensitive {
			return func(name string, _ int) string {
				return rnOnPart(name, rule.KeepExtension, func(part string) string {
					return strings.ReplaceAll(part, rule.Find, rule.Replace)
				})
			}, nil
		}
		// A quoted expression is the safe way to fold case over a plain
		// substring, since lowering both sides can change their byte length.
		expr = regexp.QuoteMeta(rule.Find)
	}
	if !rule.CaseSensitive {
		expr = "(?i)" + expr
	}
	re, err := regexp.Compile(expr)
	if err != nil {
		// The message quotes the expression as the user typed it, so the case
		// folding this layer adds does not turn up in the explanation.
		if _, plain := regexp.Compile(rule.Find); plain != nil {
			err = plain
		}
		detail := strings.TrimPrefix(err.Error(), "error parsing regexp: ")
		return nil, fmt.Errorf("%w: %s is not a valid search expression (%s)", ErrInvalidRule, strconv.Quote(rule.Find), detail)
	}
	if !rule.Regex {
		return func(name string, _ int) string {
			return rnOnPart(name, rule.KeepExtension, func(part string) string {
				return re.ReplaceAllLiteralString(part, rule.Replace)
			})
		}, nil
	}
	return func(name string, _ int) string {
		return rnOnPart(name, rule.KeepExtension, func(part string) string {
			return re.ReplaceAllString(part, rule.Replace)
		})
	}, nil
}

// rnNumberer builds the counting rewriter.
func rnNumberer(rule RenameRule) (func(name string, index int) string, error) {
	pattern := rule.Pattern
	if strings.TrimSpace(pattern) == "" {
		pattern = "{n}"
	}
	if !strings.Contains(pattern, "{n}") {
		return nil, fmt.Errorf("%w: the numbering pattern needs {n} where the counter goes", ErrInvalidRule)
	}
	start := rule.Start
	if start == 0 {
		start = 1
	}
	padding := min(max(rule.Padding, 0), rnMaxPadding)
	return func(name string, index int) string {
		base, ext := rnSplitName(name)
		counter := strconv.Itoa(start + index)
		if padding > 0 {
			counter = fmt.Sprintf("%0*d", padding, start+index)
		}
		// One pass, so a base name that itself reads like a placeholder is not
		// substituted a second time.
		out := strings.NewReplacer("{n}", counter, "{name}", base).Replace(pattern)
		// The pattern names the base, so the original extension follows it.
		// Numbering a folder of photos must never turn IMG_001.JPG into a
		// bare holiday-001 that the system no longer knows how to open. A
		// pattern that supplies its own extension wins.
		if ext != "" && path.Ext(out) == "" {
			out += ext
		}
		return out
	}, nil
}

// rnOnPart applies a text change to the whole name, or to the base name alone
// when the rule keeps the extension.
func rnOnPart(name string, keepExt bool, fn func(string) string) string {
	if !keepExt {
		return fn(name)
	}
	base, ext := rnSplitName(name)
	return fn(base) + ext
}

// rnSplitName splits a name into its base and its extension. A leading dot
// belongs to the name, so ".env" is a base name without an extension.
func rnSplitName(name string) (string, string) {
	trimmed := strings.TrimPrefix(name, ".")
	i := strings.LastIndex(trimmed, ".")
	if i <= 0 {
		return name, ""
	}
	ext := trimmed[i:]
	return name[:len(name)-len(ext)], ext
}

// rnTitle capitalizes the first letter of every word and lowers the rest.
// Digits and apostrophes stay inside the word, so "don't" keeps its shape.
func rnTitle(s string) string {
	out := []rune(strings.ToLower(s))
	start := true
	for i, r := range out {
		switch {
		case unicode.IsLetter(r):
			if start {
				out[i] = unicode.ToUpper(r)
			}
			start = false
		case unicode.IsDigit(r) || r == '\'':
			start = false
		default:
			start = true
		}
	}
	return string(out)
}

// rnMessage renders a failure as a short phrase for one row of the report,
// without leaking server paths or driver text.
func rnMessage(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, ErrNotFound), errors.Is(err, os.ErrNotExist):
		return "not found"
	case errors.Is(err, ErrExists), errors.Is(err, os.ErrExist):
		return rnReasonExists
	case errors.Is(err, ErrDenied), errors.Is(err, os.ErrPermission):
		return "permission denied"
	case errors.Is(err, ErrReadOnly):
		return "read only location"
	case errors.Is(err, ErrForbidden):
		return "outside the area you can access"
	case errors.Is(err, ErrInvalidName):
		return rnReasonInvalid
	}
	return "could not be renamed"
}
