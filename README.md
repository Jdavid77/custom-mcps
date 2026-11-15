# custom-mcps

Small MCP servers for the home-cluster kagent agents. Each folder is one server, built into `ghcr.io/jdavid77/<name>-mcp` and served over streamable HTTP (stateless) on `:8080/mcp`.

| MCP | Tools | Env |
|---|---|---|
| `akeyless` | `list_items`, `find_unused_secrets` | `AKEYLESS_ACCESS_ID`, `AKEYLESS_ACCESS_KEY`; needs a ServiceAccount that can list ExternalSecrets and PushSecrets |
| `pushover` | `send_notification` | `PUSHOVER_TOKEN`, `PUSHOVER_USER_KEY` |

Build one locally:

```sh
docker build --build-arg MCP=akeyless -t akeyless-mcp .
```

To add a new MCP, create `<name>/main.go` and add `<name>` to the matrix in `.github/workflows/build.yaml`.
