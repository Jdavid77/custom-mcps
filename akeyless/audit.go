package main

import (
	"context"
	"strings"
)

// protectedPrefix holds items the OMV NAS uses outside the cluster, so the
// ExternalSecret check cannot tell whether they are unused.
const protectedPrefix = "/cert-manager/"

type secretStore interface {
	List(ctx context.Context, path string) ([]item, error)
	Delete(ctx context.Context, name string) error
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
	all, err := a.store.List(ctx, "/")
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
