package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type item struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

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
	used, err := usedKeys(ctx)
	if err != nil {
		return nil, unusedOut{}, err
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
