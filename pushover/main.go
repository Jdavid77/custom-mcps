package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type notification struct {
	Title   string `json:"title"`
	Message string `json:"message"`
}

type result struct {
	Status string `json:"status"`
}

var client = &http.Client{Timeout: 10 * time.Second}

func truncate(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

func send(ctx context.Context, _ *mcp.CallToolRequest, in notification) (*mcp.CallToolResult, result, error) {
	form := url.Values{
		"token":   {os.Getenv("PUSHOVER_TOKEN")},
		"user":    {os.Getenv("PUSHOVER_USER_KEY")},
		"title":   {truncate(in.Title, 250)},
		"message": {truncate(in.Message, 1024)},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.pushover.net/1/messages.json", strings.NewReader(form.Encode()))
	if err != nil {
		return nil, result{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := client.Do(req)
	if err != nil {
		return nil, result{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 500))
		return nil, result{}, fmt.Errorf("pushover returned %d: %s", resp.StatusCode, body)
	}
	return nil, result{Status: "sent"}, nil
}

func main() {
	server := mcp.NewServer(&mcp.Implementation{Name: "pushover"}, nil)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "send_notification",
		Description: "Send a push notification to the cluster owner's phone via Pushover.",
	}, send)
	mux := http.NewServeMux()
	mux.Handle("/mcp", mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{Stateless: true}))
	srv := &http.Server{Addr: ":8080", Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	log.Fatal(srv.ListenAndServe())
}
