package main

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"net/http"
	"os"
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

// listAkeylessItems lists every Akeyless item under path, in API order.
// list-items returns one folder level (items + subfolders), so walk the tree.
func listAkeylessItems(ctx context.Context, path string) ([]item, error) {
	var auth struct {
		Token string `json:"token"`
	}
	err := akeylessPost(ctx, "/auth", map[string]string{
		"access-type": "access_key",
		"access-id":   os.Getenv("AKEYLESS_ACCESS_ID"),
		"access-key":  os.Getenv("AKEYLESS_ACCESS_KEY"),
	}, &auth)
	if err != nil {
		return nil, err
	}
	var found []item
	foundNames := map[string]bool{}
	// queued holds every folder ever pushed, so a folder is walked once
	pending, queued := []string{path}, map[string]bool{path: true}
	for len(pending) > 0 {
		folder := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		pageToken := ""
		for {
			body := map[string]string{"token": auth.Token, "path": folder}
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
				return nil, err
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
	return found, nil
}
