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
	defer func() { _ = resp.Body.Close() }()
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
	store, err := newAkeylessStore(ctx)
	if err != nil {
		return nil, listOut{}, err
	}
	found, err := store.List(ctx, in.Path)
	return nil, listOut{Items: found}, err
}

// newAudit logs in to Akeyless once, for the length of one tool call.
func newAudit(ctx context.Context) (audit, error) {
	store, err := newAkeylessStore(ctx)
	return audit{store: store, refs: externalSecretRefs{}}, err
}

func findUnused(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, unusedReport, error) {
	a, err := newAudit(ctx)
	if err != nil {
		return nil, unusedReport{}, err
	}
	report, err := a.Unused(ctx)
	return nil, report, err
}

type deleteIn struct {
	Names []string `json:"names" jsonschema:"item names returned by find_unused_secrets"`
}

func deleteUnused(ctx context.Context, _ *mcp.CallToolRequest, in deleteIn) (*mcp.CallToolResult, deleteReport, error) {
	a, err := newAudit(ctx)
	if err != nil {
		return nil, deleteReport{}, err
	}
	report, err := a.DeleteUnused(ctx, in.Names)
	return nil, report, err
}

func main() {
	server := mcp.NewServer(&mcp.Implementation{Name: "akeyless"}, nil)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_items",
		Description: "List Akeyless item names (not values) under a path, including subfolders.",
	}, listItems)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "find_unused_secrets",
		Description: "Compare every Akeyless item under / with the keys referenced by ExternalSecrets in the cluster, and return the items nothing references. Items under /cert-manager/ come back with protected: true, since they may be used outside the cluster, and delete_unused_secrets never deletes them.",
	}, findUnused)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "delete_unused_secrets",
		Description: "Delete, in one call, Akeyless items that find_unused_secrets reports as unused. The server checks each name again first and skips any that an ExternalSecret references, that does not exist, or that is under /cert-manager/. Treat deletion as permanent.",
	}, deleteUnused)
	mux := http.NewServeMux()
	mux.Handle("/mcp", mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{Stateless: true}))
	srv := &http.Server{Addr: ":8080", Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	log.Fatal(srv.ListenAndServe())
}
