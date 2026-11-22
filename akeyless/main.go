package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Jdavid77/custom-mcps/internal/mcplog"
)

var logger = mcplog.New("akeyless")

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
	found, _, err := store.List(ctx, in.Path)
	if err == nil {
		logger.InfoContext(ctx, "listed items", "path", in.Path, "count", len(found))
	}
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
	if err == nil {
		names := make([]string, len(report.Unused))
		for i, u := range report.Unused {
			names[i] = u.Name
		}
		logger.InfoContext(ctx, "found unused secrets", "total_items", report.TotalItems, "referenced_keys", report.ReferencedKeys, "unused", names)
	}
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
	logDeletes(ctx, "item", in.Names, report)
	return nil, report, err
}

func findEmptyFolders(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, emptyFoldersReport, error) {
	a, err := newAudit(ctx)
	if err != nil {
		return nil, emptyFoldersReport{}, err
	}
	report, err := a.EmptyFolders(ctx)
	if err == nil {
		logger.InfoContext(ctx, "found empty folders", "folders", report.Folders)
	}
	return nil, report, err
}

type deleteFoldersIn struct {
	Names []string `json:"names" jsonschema:"folder names returned by find_empty_folders"`
}

func deleteEmptyFolders(ctx context.Context, _ *mcp.CallToolRequest, in deleteFoldersIn) (*mcp.CallToolResult, deleteReport, error) {
	a, err := newAudit(ctx)
	if err != nil {
		return nil, deleteReport{}, err
	}
	report, err := a.DeleteEmptyFolders(ctx, in.Names)
	logDeletes(ctx, "folder", in.Names, report)
	return nil, report, err
}

// logDeletes writes one line per name, so the logs keep an audit trail of what an agent asked to delete and
// what happened to each name. Deletions are logged at warn, since they cannot be undone
func logDeletes(ctx context.Context, kind string, requested []string, report deleteReport) {
	logger.InfoContext(ctx, "delete requested", "kind", kind, "names", requested)
	for _, name := range report.Deleted {
		logger.WarnContext(ctx, "deleted", "kind", kind, "name", name)
	}
	for _, s := range report.Skipped {
		logger.InfoContext(ctx, "delete skipped", "kind", kind, "name", s.Name, "reason", s.Reason)
	}
	for _, f := range report.Failed {
		logger.ErrorContext(ctx, "delete failed", "kind", kind, "name", f.Name, "error", f.Reason)
	}
}

func main() {
	server := mcp.NewServer(&mcp.Implementation{Name: "akeyless"}, &mcp.ServerOptions{Logger: mcplog.SDK("akeyless")})
	server.AddReceivingMiddleware(mcplog.Middleware(logger))
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
	mcp.AddTool(server, &mcp.Tool{
		Name:        "find_empty_folders",
		Description: "Return the Akeyless folders that hold no item at any depth, deepest first. / and folders under /cert-manager are never listed.",
	}, findEmptyFolders)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "delete_empty_folders",
		Description: "Delete, in one call, Akeyless folders that find_empty_folders reports, deepest first. The server checks each folder again and skips any that is not empty, does not exist, is / or is under /cert-manager.",
	}, deleteEmptyFolders)
	mux := http.NewServeMux()
	mux.Handle("/mcp", mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{Stateless: true, Logger: mcplog.SDK("akeyless")}))
	srv := &http.Server{Addr: ":8080", Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	logger.Info("listening", "addr", srv.Addr)
	logger.Error("server stopped", "error", srv.ListenAndServe())
	os.Exit(1)
}
