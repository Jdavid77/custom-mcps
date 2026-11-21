package main

import (
	"cmp"
	"context"
	"slices"
	"strings"
)

// protectedPrefix holds items the OMV NAS uses outside the cluster, so the
// ExternalSecret check cannot tell whether they are unused.
const protectedPrefix = "/cert-manager/"

type secretStore interface {
	// List returns every item and every folder under path.
	List(ctx context.Context, path string) ([]item, []string, error)
	Delete(ctx context.Context, name string) error
	DeleteFolder(ctx context.Context, name string) error
}

// referenceSource returns the Akeyless keys the cluster references, as written
// in the references (with or without a leading "/").
type referenceSource interface {
	Keys(ctx context.Context) (map[string]bool, error)
}

// audit compares the items in a secret store with the keys the cluster references.
type audit struct {
	store secretStore
	refs  referenceSource
}

type unusedItem struct {
	Name      string `json:"name"`
	Type      string `json:"type"`
	Protected bool   `json:"protected"`
}

type unusedReport struct {
	TotalItems     int          `json:"total_items"`
	ReferencedKeys int          `json:"referenced_keys"`
	Unused         []unusedItem `json:"unused"`
}

type skippedItem struct {
	Name   string `json:"name"`
	Reason string `json:"reason"`
}

type deleteReport struct {
	Deleted []string      `json:"deleted"`
	Skipped []skippedItem `json:"skipped"`
	Failed  []skippedItem `json:"failed"`
}

func normalizeKey(key string) string {
	return "/" + strings.TrimLeft(key, "/")
}

// Unused returns every item under / that no reference points at.
func (a audit) Unused(ctx context.Context) (unusedReport, error) {
	refs, err := a.refs.Keys(ctx)
	if err != nil {
		return unusedReport{}, err
	}
	used := map[string]bool{}
	for key := range refs {
		used[normalizeKey(key)] = true
	}
	all, _, err := a.store.List(ctx, "/")
	if err != nil {
		return unusedReport{}, err
	}
	report := unusedReport{TotalItems: len(all), ReferencedKeys: len(used), Unused: []unusedItem{}}
	for _, it := range all {
		key := normalizeKey(it.Name)
		if !used[key] {
			report.Unused = append(report.Unused, unusedItem{it.Name, it.Type, strings.HasPrefix(key, protectedPrefix)})
		}
	}
	return report, nil
}

// DeleteUnused deletes only names that Unused reports right now and that are
// not protected, so a wrong or stale name from the model cannot remove a
// secret in use. One failed delete does not stop the others.
func (a audit) DeleteUnused(ctx context.Context, names []string) (deleteReport, error) {
	report, err := a.Unused(ctx)
	if err != nil {
		return deleteReport{}, err
	}
	unused := map[string]unusedItem{}
	for _, it := range report.Unused {
		unused[normalizeKey(it.Name)] = it
	}
	out := deleteReport{Deleted: []string{}, Skipped: []skippedItem{}, Failed: []skippedItem{}}
	for _, name := range names {
		key := normalizeKey(name)
		it, ok := unused[key]
		switch {
		case strings.HasPrefix(key, protectedPrefix):
			out.Skipped = append(out.Skipped, skippedItem{name, "protected: may be used by OMV"})
		case !ok:
			out.Skipped = append(out.Skipped, skippedItem{name, "not unused: still referenced, missing or listed twice"})
		default:
			delete(unused, key)
			if err := a.store.Delete(ctx, it.Name); err != nil {
				out.Failed = append(out.Failed, skippedItem{it.Name, err.Error()})
				continue
			}
			out.Deleted = append(out.Deleted, it.Name)
		}
	}
	return out, nil
}

// normalizeFolder writes a folder as "/a/b", so "/" becomes "".
func normalizeFolder(name string) string {
	return strings.TrimRight(normalizeKey(name), "/")
}

// protectedFolder reports whether a folder is /cert-manager or under it.
func protectedFolder(folder string) bool {
	return strings.HasPrefix(folder+"/", protectedPrefix)
}

func depth(folder string) int {
	return strings.Count(folder, "/")
}

type emptyFoldersReport struct {
	Folders []string `json:"folders"`
}

// emptyFolders maps each normalised folder that holds no item at any depth
// to its name as Akeyless lists it, leaving out / and protected folders.
func (a audit) emptyFolders(ctx context.Context) (map[string]string, error) {
	all, folders, err := a.store.List(ctx, "/")
	if err != nil {
		return nil, err
	}
	empty := map[string]string{}
	for _, f := range folders {
		if key := normalizeFolder(f); key != "" && !protectedFolder(key) {
			empty[key] = f
		}
	}
	for _, it := range all {
		key := normalizeKey(it.Name)
		for f := range empty {
			if strings.HasPrefix(key, f+"/") {
				delete(empty, f)
			}
		}
	}
	return empty, nil
}

// EmptyFolders returns the folders with no item under them, deepest first.
func (a audit) EmptyFolders(ctx context.Context) (emptyFoldersReport, error) {
	empty, err := a.emptyFolders(ctx)
	if err != nil {
		return emptyFoldersReport{}, err
	}
	keys := make([]string, 0, len(empty))
	for k := range empty {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	sortDeepestFirst(keys, func(k string) string { return k })
	report := emptyFoldersReport{Folders: []string{}}
	for _, k := range keys {
		report.Folders = append(report.Folders, empty[k])
	}
	return report, nil
}

// DeleteEmptyFolders deletes only folders that EmptyFolders reports right
// now, deepest first so a parent goes after its children. The store refuses
// a folder that is not empty at the moment of deletion.
func (a audit) DeleteEmptyFolders(ctx context.Context, names []string) (deleteReport, error) {
	empty, err := a.emptyFolders(ctx)
	if err != nil {
		return deleteReport{}, err
	}
	names = slices.Clone(names)
	sortDeepestFirst(names, normalizeFolder)
	out := deleteReport{Deleted: []string{}, Skipped: []skippedItem{}, Failed: []skippedItem{}}
	for _, name := range names {
		key := normalizeFolder(name)
		f, ok := empty[key]
		switch {
		case key == "":
			out.Skipped = append(out.Skipped, skippedItem{name, "the root folder is never deleted"})
		case protectedFolder(key):
			out.Skipped = append(out.Skipped, skippedItem{name, "protected: may be used by OMV"})
		case !ok:
			out.Skipped = append(out.Skipped, skippedItem{name, "not empty: has items, missing or listed twice"})
		default:
			delete(empty, key)
			if err := a.store.DeleteFolder(ctx, f); err != nil {
				out.Failed = append(out.Failed, skippedItem{f, err.Error()})
				continue
			}
			out.Deleted = append(out.Deleted, f)
		}
	}
	return out, nil
}

func sortDeepestFirst(s []string, key func(string) string) {
	slices.SortStableFunc(s, func(x, y string) int {
		return cmp.Compare(depth(key(y)), depth(key(x)))
	})
}
