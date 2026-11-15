package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	akeyless = "https://api.akeyless.io"
	k8s      = "https://kubernetes.default.svc"
	sa       = "/var/run/secrets/kubernetes.io/serviceaccount"
)

type item struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

var client = &http.Client{Timeout: 30 * time.Second}

func do(ctx context.Context, c *http.Client, method, url string, body any, header http.Header, out any) error {
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, r)
	if err != nil {
		return err
	}
	if header != nil {
		req.Header = header
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 500))
		return fmt.Errorf("%s returned %d: %s", url, resp.StatusCode, b)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// items lists every Akeyless item under path. list-items returns one folder
// level (items + subfolders), so walk the tree.
func items(ctx context.Context, path string) ([]item, error) {
	var auth struct {
		Token string `json:"token"`
	}
	err := do(ctx, client, http.MethodPost, akeyless+"/auth", map[string]string{
		"access-type": "access_key",
		"access-id":   os.Getenv("AKEYLESS_ACCESS_ID"),
		"access-key":  os.Getenv("AKEYLESS_ACCESS_KEY"),
	}, nil, &auth)
	if err != nil {
		return nil, err
	}
	found, seen, folders := map[string]item{}, map[string]bool{}, []string{path}
	for len(folders) > 0 {
		folder := folders[len(folders)-1]
		folders = folders[:len(folders)-1]
		if seen[folder] {
			continue
		}
		seen[folder] = true
		page := ""
		for {
			body := map[string]string{"token": auth.Token, "path": folder}
			if page != "" {
				body["pagination-token"] = page
			}
			var out struct {
				Items []struct {
					Name string `json:"item_name"`
					Type string `json:"item_type"`
				} `json:"items"`
				Folders  []string `json:"folders"`
				NextPage string   `json:"next_page"`
			}
			if err := do(ctx, client, http.MethodPost, akeyless+"/list-items", body, nil, &out); err != nil {
				return nil, err
			}
			added := false
			for _, i := range out.Items {
				if _, ok := found[i.Name]; !ok {
					found[i.Name] = item{i.Name, i.Type}
					added = true
				}
			}
			for _, f := range out.Folders {
				if !seen[f] {
					folders = append(folders, f)
					added = true
				}
			}
			page = out.NextPage
			// next_page can come back set even on the last page
			if page == "" || !added {
				break
			}
		}
	}
	list := make([]item, 0, len(found))
	for _, i := range found {
		list = append(list, i)
	}
	return list, nil
}

func k8sList(ctx context.Context, version, resource string, out any) error {
	token, err := os.ReadFile(sa + "/token")
	if err != nil {
		return err
	}
	ca, err := os.ReadFile(sa + "/ca.crt")
	if err != nil {
		return err
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(ca)
	c := &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}}}
	url := fmt.Sprintf("%s/apis/external-secrets.io/%s/%s", k8s, version, resource)
	return do(ctx, c, http.MethodGet, url, nil, http.Header{"Authorization": {"Bearer " + string(token)}}, out)
}

func norm(key string) string {
	return "/" + strings.TrimLeft(key, "/")
}

type listIn struct {
	Path string `json:"path,omitempty" jsonschema:"folder to list, defaults to /"`
}

type listOut struct {
	Items []item `json:"items"`
}

func listItems(ctx context.Context, _ *mcp.CallToolRequest, in listIn) (*mcp.CallToolResult, listOut, error) {
	if in.Path == "" {
		in.Path = "/"
	}
	list, err := items(ctx, in.Path)
	return nil, listOut{Items: list}, err
}

type unusedOut struct {
	TotalItems     int    `json:"total_items"`
	ReferencedKeys int    `json:"referenced_keys"`
	Unused         []item `json:"unused"`
}

func findUnused(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, unusedOut, error) {
	used := map[string]bool{}
	var es struct {
		Items []struct {
			Spec struct {
				Data []struct {
					RemoteRef struct {
						Key string `json:"key"`
					} `json:"remoteRef"`
				} `json:"data"`
				DataFrom []struct {
					Extract *struct {
						Key string `json:"key"`
					} `json:"extract"`
				} `json:"dataFrom"`
			} `json:"spec"`
		} `json:"items"`
	}
	if err := k8sList(ctx, "v1", "externalsecrets", &es); err != nil {
		return nil, unusedOut{}, err
	}
	for _, e := range es.Items {
		for _, d := range e.Spec.Data {
			used[norm(d.RemoteRef.Key)] = true
		}
		for _, d := range e.Spec.DataFrom {
			if d.Extract != nil {
				used[norm(d.Extract.Key)] = true
			}
		}
	}
	var ps struct {
		Items []struct {
			Spec struct {
				Data []struct {
					Match struct {
						RemoteRef struct {
							RemoteKey string `json:"remoteKey"`
						} `json:"remoteRef"`
					} `json:"match"`
				} `json:"data"`
			} `json:"spec"`
		} `json:"items"`
	}
	if err := k8sList(ctx, "v1alpha1", "pushsecrets", &ps); err != nil {
		return nil, unusedOut{}, err
	}
	for _, p := range ps.Items {
		for _, d := range p.Spec.Data {
			used[norm(d.Match.RemoteRef.RemoteKey)] = true
		}
	}
	all, err := items(ctx, "/")
	if err != nil {
		return nil, unusedOut{}, err
	}
	out := unusedOut{TotalItems: len(all), ReferencedKeys: len(used), Unused: []item{}}
	for _, i := range all {
		if !used[norm(i.Name)] {
			out.Unused = append(out.Unused, i)
		}
	}
	return nil, out, nil
}

func main() {
	server := mcp.NewServer(&mcp.Implementation{Name: "akeyless"}, nil)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_items",
		Description: "List Akeyless item names (not values) under a path, including subfolders.",
	}, listItems)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "find_unused_secrets",
		Description: "Compare every Akeyless item under / with the keys referenced by ExternalSecrets and PushSecrets in the cluster, and return the items nothing references.",
	}, findUnused)
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{Stateless: true})
	http.Handle("/mcp", handler)
	log.Fatal(http.ListenAndServe(":8080", nil))
}
