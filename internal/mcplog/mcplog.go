// Package mcplog gives every MCP the same structured logs: JSON lines on
// stdout, and one line per MCP request with the method, tool, duration and
// outcome. Tool arguments are not logged here, since each MCP knows which of
// its arguments are safe to log.
package mcplog

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// New returns a JSON logger tagged with the MCP's name. LOG_LEVEL (debug,
// info, warn, error) sets the level, info by default.
func New(name string) *slog.Logger {
	return newLogger(name, level())
}

// SDK returns the logger to give the go-sdk. In stateless mode the SDK logs a
// session connect and disconnect at info for every request, health probes
// included, so below debug it only keeps the SDK's warnings and errors.
func SDK(name string) *slog.Logger {
	l := level()
	if l > slog.LevelDebug && l < slog.LevelWarn {
		l = slog.LevelWarn
	}
	return newLogger(name, l)
}

func level() slog.Level {
	var l slog.Level
	if err := l.UnmarshalText([]byte(strings.TrimSpace(os.Getenv("LOG_LEVEL")))); err != nil {
		return slog.LevelInfo
	}
	return l
}

func newLogger(name string, l slog.Level) *slog.Logger {
	h := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: l})
	return slog.New(h).With("mcp", name)
}

// Middleware logs each MCP request once it is handled. A tool that fails is
// logged at error level, whether it returned an error or a result with
// isError set; everything else is info, except the protocol chatter
// (initialize, tools/list, notifications), which is debug.
func Middleware(logger *slog.Logger) mcp.Middleware {
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			start := time.Now()
			res, err := next(ctx, method, req)
			attrs := []slog.Attr{
				slog.String("method", method),
				slog.Int64("duration_ms", time.Since(start).Milliseconds()),
			}
			level := slog.LevelDebug
			if p, ok := req.GetParams().(*mcp.CallToolParamsRaw); ok {
				attrs = append(attrs, slog.String("tool", p.Name))
				level = slog.LevelInfo
			}
			// A protocol error (an unknown tool, say) comes back as a nil *CallToolResult with err set
			if r, ok := res.(*mcp.CallToolResult); ok && r != nil && r.IsError {
				level = slog.LevelError
				attrs = append(attrs, slog.String("error", toolError(r)))
			}
			if err != nil {
				level = slog.LevelError
				attrs = append(attrs, slog.String("error", err.Error()))
			}
			logger.LogAttrs(ctx, level, "mcp request", attrs...)
			return res, err
		}
	}
}

// toolError returns the text the SDK puts in a failed tool's result.
func toolError(r *mcp.CallToolResult) string {
	for _, c := range r.Content {
		if t, ok := c.(*mcp.TextContent); ok {
			return t.Text
		}
	}
	return ""
}
