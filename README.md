# Custom MCP's

> [!WARNING]
> **AI Slop** — These MCP's are "AI Slop". The code works for my use case. Deploy at your own risk.

Small MCP servers for the home-cluster kagent agents. Each folder is one server, built into `ghcr.io/jdavid77/<name>-mcp` and served over streamable HTTP (stateless) on `:8080/mcp`.

| MCP | Tools | Env |
|---|---|---|
| `akeyless` | `list_items`, `find_unused_secrets` | `AKEYLESS_ACCESS_ID`, `AKEYLESS_ACCESS_KEY`, optional `AKEYLESS_API_URL` (default `https://api.akeyless.io`); needs a ServiceAccount that can list ExternalSecrets |
| `pushover` | `send_notification` | `PUSHOVER_TOKEN`, `PUSHOVER_USER_KEY` |

Build one locally:

```sh
docker build --build-arg MCP=akeyless -t akeyless-mcp .
```

To add a new MCP, create `<name>/main.go` and add a `<name>` path filter in `.github/workflows/build.yaml`. CI only builds the MCPs whose folder changed (or all of them when `go.mod`, `go.sum` or the `Dockerfile` change). Each push to `main` pushes the image as `latest` and `vYYYY.MM.DD.NN`, counting on from the newest tag already in GHCR.
