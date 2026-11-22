package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Jdavid77/custom-mcps/internal/mcplog"
)

type notification struct {
	Title   string `json:"title"`
	Message string `json:"message"`
}

type result struct {
	Status string `json:"status"`
}

var (
	client = &http.Client{Timeout: 10 * time.Second}
	logger = mcplog.New("pushover")
)

func truncate(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

func send(ctx context.Context, _ *mcp.CallToolRequest, in notification) (*mcp.CallToolResult, result, error) {
	title, message := truncate(in.Title, 250), truncate(in.Message, 1024)
	// The message is logged as sent, so a notification that renders badly on the phone can be compared with
	// what the agent wrote
	log := logger.With(
		"title", title,
		"message", message,
		"message_length", len([]rune(in.Message)),
		"truncated", title != in.Title || message != in.Message,
	)
	form := url.Values{
		"token":   {os.Getenv("PUSHOVER_TOKEN")},
		"user":    {os.Getenv("PUSHOVER_USER_KEY")},
		"title":   {title},
		"message": {message},
		"html":    {"1"},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.pushover.net/1/messages.json", strings.NewReader(form.Encode()))
	if err != nil {
		return nil, result{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := client.Do(req)
	if err != nil {
		log.ErrorContext(ctx, "notification not sent", "error", err)
		return nil, result{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 500))
	if resp.StatusCode != http.StatusOK {
		log.ErrorContext(ctx, "notification not sent", "status", resp.StatusCode, "response", string(body))
		return nil, result{}, fmt.Errorf("pushover returned %d: %s", resp.StatusCode, body)
	}
	// Pushover's request ID is what its support and its API ask for when tracing a message
	var sent struct {
		Request string `json:"request"`
	}
	_ = json.Unmarshal(body, &sent)
	log.InfoContext(ctx, "notification sent", "pushover_request", sent.Request)
	return nil, result{Status: "sent"}, nil
}

func main() {
	server := mcp.NewServer(&mcp.Implementation{Name: "pushover"}, &mcp.ServerOptions{Logger: mcplog.SDK("pushover")})
	server.AddReceivingMiddleware(mcplog.Middleware(logger))
	mcp.AddTool(server, &mcp.Tool{
		Name:        "send_notification",
		Description: "Send a push notification to the cluster owner's phone via Pushover. The message is rendered as Pushover HTML: only <b>, <i>, <u>, <font color=\"#rrggbb\"> and <a href=\"...\"> work, newlines are kept, and a literal <, > or & must be written as &lt;, &gt; or &amp;. Markdown is not rendered.",
	}, send)
	mux := http.NewServeMux()
	mux.Handle("/mcp", mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{Stateless: true, Logger: mcplog.SDK("pushover")}))
	srv := &http.Server{Addr: ":8080", Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	logger.Info("listening", "addr", srv.Addr)
	logger.Error("server stopped", "error", srv.ListenAndServe())
	os.Exit(1)
}
