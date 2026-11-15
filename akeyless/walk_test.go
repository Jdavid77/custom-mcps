package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"
)

// Akeyless can return next_page even on the last page, so the walk must stop
// once a page adds nothing new, and must not re-walk queued folders.
func TestListAkeylessItemsWalksTreeWithStickyNextPage(t *testing.T) {
	tree := map[string]struct{ items, folders []string }{
		"/":    {[]string{"/x"}, []string{"/a", "/b"}},
		"/a":   {[]string{"/a/1", "/a/2"}, []string{"/a/c"}},
		"/a/c": {[]string{"/a/c/1"}, nil},
		"/b":   {[]string{"/b/1"}, nil},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/auth" {
			json.NewEncoder(w).Encode(map[string]string{"token": "t"})
			return
		}
		var body map[string]string
		json.NewDecoder(r.Body).Decode(&body)
		node := tree[body["path"]]
		var items []map[string]string
		for _, name := range node.items {
			items = append(items, map[string]string{"item_name": name, "item_type": "STATIC_SECRET"})
		}
		json.NewEncoder(w).Encode(map[string]any{"items": items, "folders": node.folders, "next_page": "always"})
	}))
	defer srv.Close()
	akeylessAPI = srv.URL

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	first, err := listAkeylessItems(ctx, "/")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, it := range first {
		names = append(names, it.Name)
	}
	if want := []string{"/a/1", "/a/2", "/a/c/1", "/b/1", "/x"}; !slices.Equal(slices.Sorted(slices.Values(names)), want) {
		t.Fatalf("got %v, want %v", names, want)
	}
	second, err := listAkeylessItems(ctx, "/")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(first, second) {
		t.Fatalf("order changed between runs: %v vs %v", first, second)
	}
}
