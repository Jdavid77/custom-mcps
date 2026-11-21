package main

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"
)

// akeylessAPI can point at a self-hosted Akeyless gateway instead of the SaaS API.
var akeylessAPI = cmp.Or(os.Getenv("AKEYLESS_API_URL"), "https://api.akeyless.io")

var akeylessClient = &http.Client{Timeout: 30 * time.Second}

func akeylessPost(ctx context.Context, endpoint string, body, out any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, akeylessAPI+endpoint, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	return send(akeylessClient, req, out)
}

// akeylessStore is a secretStore backed by the Akeyless API, logged in once
// with the API key from the environment.
type akeylessStore struct{ token string }

func newAkeylessStore(ctx context.Context) (akeylessStore, error) {
	var auth struct {
		Token string `json:"token"`
	}
	err := akeylessPost(ctx, "/auth", map[string]string{
		"access-type": "access_key",
		"access-id":   os.Getenv("AKEYLESS_ACCESS_ID"),
		"access-key":  os.Getenv("AKEYLESS_ACCESS_KEY"),
	}, &auth)
	return akeylessStore{auth.Token}, err
}

// Delete refuses protected items itself, so no caller can delete them even if
// it skips the audit's checks.
func (s akeylessStore) Delete(ctx context.Context, name string) error {
	if strings.HasPrefix(normalizeKey(name), protectedPrefix) {
		return fmt.Errorf("refusing to delete protected item %s", name)
	}
	return akeylessPost(ctx, "/delete-item", map[string]string{"token": s.token, "name": name}, &struct{}{})
}

// DeleteFolder refuses /, protected folders and any folder that still holds
// an item or subfolder, since the API docs do not say whether folder-delete
// removes a folder's contents.
func (s akeylessStore) DeleteFolder(ctx context.Context, name string) error {
	folder := normalizeFolder(name)
	if folder == "" || protectedFolder(folder) {
		return fmt.Errorf("refusing to delete protected folder %q", name)
	}
	var page struct {
		Items   []json.RawMessage `json:"items"`
		Folders []string          `json:"folders"`
	}
	if err := akeylessPost(ctx, "/list-items", map[string]string{"token": s.token, "path": name}, &page); err != nil {
		return err
	}
	if len(page.Items) > 0 || len(page.Folders) > 0 {
		return fmt.Errorf("refusing to delete folder %s: it is not empty", name)
	}
	return akeylessPost(ctx, "/folder-delete", map[string]string{"token": s.token, "name": name}, &struct{}{})
}

// List lists every Akeyless item and folder under path, in API order.
// list-items returns one folder level (items + subfolders), so walk the tree.
func (s akeylessStore) List(ctx context.Context, path string) ([]item, []string, error) {
	var found []item
	var folders []string
	foundNames := map[string]bool{}
	// queued holds every folder ever pushed, so a folder is walked once
	pending, queued := []string{path}, map[string]bool{path: true}
	for len(pending) > 0 {
		folder := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		pageToken := ""
		for {
			body := map[string]string{"token": s.token, "path": folder}
			if pageToken != "" {
				body["pagination-token"] = pageToken
			}
			var page struct {
				Items []struct {
					Name string `json:"item_name"`
					Type string `json:"item_type"`
				} `json:"items"`
				Folders  []string `json:"folders"`
				NextPage string   `json:"next_page"`
			}
			if err := akeylessPost(ctx, "/list-items", body, &page); err != nil {
				return nil, nil, err
			}
			added := false
			for _, it := range page.Items {
				if !foundNames[it.Name] {
					foundNames[it.Name] = true
					found = append(found, item{it.Name, it.Type})
					added = true
				}
			}
			for _, sub := range page.Folders {
				if !queued[sub] {
					queued[sub] = true
					folders = append(folders, sub)
					pending = append(pending, sub)
					added = true
				}
			}
			pageToken = page.NextPage
			// next_page can come back set even on the last page
			if pageToken == "" || !added {
				break
			}
		}
	}
	return found, folders, nil
}
