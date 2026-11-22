package mcplog

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// callTool runs one tools/call through a server using Middleware and returns
// the log lines it wrote for tools/call.
func callTool(t *testing.T, name string) []map[string]any {
	t.Helper()
	ctx := context.Background()
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	server := mcp.NewServer(&mcp.Implementation{Name: "test"}, nil)
	server.AddReceivingMiddleware(Middleware(logger))
	mcp.AddTool(server, &mcp.Tool{Name: "ok"}, func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, struct{}, error) {
		return nil, struct{}{}, nil
	})
	mcp.AddTool(server, &mcp.Tool{Name: "fails"}, func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, struct{}, error) {
		return nil, struct{}{}, errors.New("akeyless returned 401")
	})

	st, ct := mcp.NewInMemoryTransports()
	if _, err := server.Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "client"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cs.Close() }()
	if _, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: name}); err != nil {
		t.Fatal(err)
	}

	var lines []map[string]any
	for _, l := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatalf("log line is not JSON: %q", l)
		}
		if m["method"] == "tools/call" {
			lines = append(lines, m)
		}
	}
	if len(lines) != 1 {
		t.Fatalf("want one tools/call line, got %d: %s", len(lines), buf.String())
	}
	return lines
}

func TestMiddlewareLogsToolCall(t *testing.T) {
	line := callTool(t, "ok")[0]
	if line["level"] != "INFO" || line["tool"] != "ok" || line["error"] != nil {
		t.Errorf("unexpected line: %v", line)
	}
	if _, ok := line["duration"]; !ok {
		t.Errorf("no duration: %v", line)
	}
}

func TestMiddlewareLogsFailedToolAsError(t *testing.T) {
	line := callTool(t, "fails")[0]
	if line["level"] != "ERROR" || line["tool"] != "fails" || !strings.Contains(line["error"].(string), "akeyless returned 401") {
		t.Errorf("unexpected line: %v", line)
	}
}
