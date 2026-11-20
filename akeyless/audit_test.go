package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
)

type fakeStore struct {
	items   []item
	listErr error
	failOn  map[string]bool
	deleted []string
}

func (f *fakeStore) List(context.Context, string) ([]item, error) {
	return f.items, f.listErr
}

func (f *fakeStore) Delete(_ context.Context, name string) error {
	if f.failOn[name] {
		return errors.New("denied")
	}
	f.deleted = append(f.deleted, name)
	return nil
}

type fakeRefs struct {
	keys []string
	err  error
}

func (f fakeRefs) Keys(context.Context) (map[string]bool, error) {
	keys := map[string]bool{}
	for _, k := range f.keys {
		keys[k] = true
	}
	return keys, f.err
}

func items(names ...string) []item {
	var out []item
	for _, n := range names {
		out = append(out, item{n, "STATIC_SECRET"})
	}
	return out
}

func TestUnused(t *testing.T) {
	store := &fakeStore{items: items("/apps/used", "/apps/written-without-slash", "/apps/orphan", "/cert-manager/cf")}
	refs := fakeRefs{keys: []string{"/apps/used", "apps/written-without-slash"}}

	report, err := audit{store, refs}.Unused(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []unusedItem{
		{"/apps/orphan", "STATIC_SECRET", false},
		{"/cert-manager/cf", "STATIC_SECRET", true},
	}
	if !slices.Equal(report.Unused, want) {
		t.Errorf("unused = %v, want %v", report.Unused, want)
	}
	if report.TotalItems != 4 || report.ReferencedKeys != 2 {
		t.Errorf("totals = %d/%d, want 4/2", report.TotalItems, report.ReferencedKeys)
	}
}

func TestUnusedReferenceError(t *testing.T) {
	store := &fakeStore{items: items("/apps/orphan")}
	report, err := audit{store, fakeRefs{err: errors.New("k8s down")}}.Unused(context.Background())
	if err == nil {
		t.Fatal("want error")
	}
	if len(report.Unused) != 0 {
		t.Errorf("unused = %v, want none", report.Unused)
	}
}

func TestDeleteUnused(t *testing.T) {
	all := items("/apps/used", "/apps/orphan", "/apps/other-orphan", "/cert-manager/cf")
	refs := fakeRefs{keys: []string{"apps/used"}}
	tests := []struct {
		name        string
		names       []string
		failOn      map[string]bool
		wantDeleted []string
		wantSkipped []string
		wantFailed  []string
	}{
		{name: "deletes unused", names: []string{"/apps/orphan", "apps/other-orphan"}, wantDeleted: []string{"/apps/orphan", "/apps/other-orphan"}},
		{name: "skips referenced", names: []string{"/apps/used"}, wantSkipped: []string{"/apps/used"}},
		{name: "skips missing", names: []string{"/apps/ghost"}, wantSkipped: []string{"/apps/ghost"}},
		{name: "deletes a duplicate once", names: []string{"/apps/orphan", "/apps/orphan"}, wantDeleted: []string{"/apps/orphan"}, wantSkipped: []string{"/apps/orphan"}},
		{name: "never deletes protected", names: []string{"/cert-manager/cf", "cert-manager/cf"}, wantSkipped: []string{"/cert-manager/cf", "cert-manager/cf"}},
		{name: "one failure does not stop the rest", names: []string{"/apps/orphan", "/apps/other-orphan"}, failOn: map[string]bool{"/apps/orphan": true}, wantDeleted: []string{"/apps/other-orphan"}, wantFailed: []string{"/apps/orphan"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &fakeStore{items: all, failOn: tt.failOn}
			out, err := audit{store, refs}.DeleteUnused(context.Background(), tt.names)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(out.Deleted, orEmpty(tt.wantDeleted)) || !slices.Equal(store.deleted, tt.wantDeleted) {
				t.Errorf("deleted = %v (store saw %v), want %v", out.Deleted, store.deleted, tt.wantDeleted)
			}
			if got := names(out.Skipped); !slices.Equal(got, tt.wantSkipped) {
				t.Errorf("skipped = %v, want %v", got, tt.wantSkipped)
			}
			if got := names(out.Failed); !slices.Equal(got, tt.wantFailed) {
				t.Errorf("failed = %v, want %v", got, tt.wantFailed)
			}
		})
	}
}

func TestDeleteUnusedDeletesNothingOnError(t *testing.T) {
	for name, a := range map[string]func(*fakeStore) audit{
		"list fails": func(s *fakeStore) audit {
			s.listErr = errors.New("akeyless down")
			return audit{s, fakeRefs{}}
		},
		"references fail": func(s *fakeStore) audit {
			return audit{s, fakeRefs{err: errors.New("k8s down")}}
		},
	} {
		t.Run(name, func(t *testing.T) {
			store := &fakeStore{items: items("/apps/orphan")}
			if _, err := a(store).DeleteUnused(context.Background(), []string{"/apps/orphan"}); err == nil {
				t.Fatal("want error")
			}
			if len(store.deleted) != 0 {
				t.Errorf("deleted %v, want nothing", store.deleted)
			}
		})
	}
}

func names(items []skippedItem) []string {
	var out []string
	for _, it := range items {
		out = append(out, it.Name)
	}
	return out
}

func orEmpty(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func TestAkeylessStoreRefusesProtected(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		_, _ = w.Write([]byte("{}"))
	}))
	defer srv.Close()
	old := akeylessAPI
	akeylessAPI = srv.URL
	defer func() { akeylessAPI = old }()

	store := akeylessStore{token: "t"}
	for _, name := range []string{"/cert-manager/cf", "cert-manager/cf", "//cert-manager/cf"} {
		if err := store.Delete(context.Background(), name); err == nil {
			t.Errorf("Delete(%q) succeeded, want refusal", name)
		}
	}
	if calls != 0 {
		t.Errorf("store sent %d requests, want none", calls)
	}
	if err := store.Delete(context.Background(), "/apps/orphan"); err != nil || calls != 1 {
		t.Errorf("Delete of unprotected item: err=%v calls=%d, want nil/1", err, calls)
	}
}
