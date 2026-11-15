package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	akeylessAPI = "https://api.akeyless.io"
	k8sAPI      = "https://kubernetes.default.svc"
	saDir       = "/var/run/secrets/kubernetes.io/serviceaccount"
)

type item struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

var akeylessClient = &http.Client{Timeout: 30 * time.Second}

// bearer adds the ServiceAccount token, re-read per request since the kubelet rotates it.
type bearer struct{ next http.RoundTripper }

func (b bearer) RoundTrip(req *http.Request) (*http.Response, error) {
	token, err := os.ReadFile(saDir + "/token")
	if err != nil {
		return nil, err
	}
	req = req.Clone(req.Context())
	req.Header.Set("Authorization", "Bearer "+string(token))
	return b.next.RoundTrip(req)
}

var k8sClient = sync.OnceValues(func() (*http.Client, error) {
	ca, err := os.ReadFile(saDir + "/ca.crt")
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(ca) {
		return nil, errors.New("no certificates in " + saDir + "/ca.crt")
	}
	transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}}
	return &http.Client{Timeout: 30 * time.Second, Transport: bearer{transport}}, nil
})

// send runs req and decodes the JSON response into out.
func send(c *http.Client, req *http.Request, out any) error {
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 500))
		return fmt.Errorf("%s returned %d: %s", req.URL, resp.StatusCode, body)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

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

func k8sList(ctx context.Context, version, resource string, out any) error {
	c, err := k8sClient()
	if err != nil {
		return err
	}
	url := fmt.Sprintf("%s/apis/external-secrets.io/%s/%s", k8sAPI, version, resource)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	return send(c, req, out)
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

func normalizeKey(key string) string {
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
	found, err := listAkeylessItems(ctx, in.Path)
	return nil, listOut{Items: found}, err
}

type unusedOut struct {
	TotalItems     int    `json:"total_items"`
	ReferencedKeys int    `json:"referenced_keys"`
	Unused         []item `json:"unused"`
}

func findUnused(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, unusedOut, error) {
	used := map[string]bool{}
	var externalSecrets struct {
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
	if err := k8sList(ctx, "v1", "externalsecrets", &externalSecrets); err != nil {
		return nil, unusedOut{}, err
	}
	for _, es := range externalSecrets.Items {
		for _, d := range es.Spec.Data {
			used[normalizeKey(d.RemoteRef.Key)] = true
		}
		for _, d := range es.Spec.DataFrom {
			if d.Extract != nil {
				used[normalizeKey(d.Extract.Key)] = true
			}
		}
	}
	var pushSecrets struct {
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
	if err := k8sList(ctx, "v1alpha1", "pushsecrets", &pushSecrets); err != nil {
		return nil, unusedOut{}, err
	}
	for _, ps := range pushSecrets.Items {
		for _, d := range ps.Spec.Data {
			used[normalizeKey(d.Match.RemoteRef.RemoteKey)] = true
		}
	}
	all, err := listAkeylessItems(ctx, "/")
	if err != nil {
		return nil, unusedOut{}, err
	}
	out := unusedOut{TotalItems: len(all), ReferencedKeys: len(used), Unused: []item{}}
	for _, it := range all {
		if !used[normalizeKey(it.Name)] {
			out.Unused = append(out.Unused, it)
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
	mux := http.NewServeMux()
	mux.Handle("/mcp", mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{Stateless: true}))
	srv := &http.Server{Addr: ":8080", Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	log.Fatal(srv.ListenAndServe())
}
