# Custom MCP's

> [!WARNING]
> **AI Slop** — These MCP's are "AI Slop". The code works for my use case. Deploy at your own risk.

Small Go MCP servers used by the [kagent](https://kagent.dev) agents in my [home-cluster](https://github.com/Jdavid77/home-cluster).

Every server:

- lives in its own folder and ships as `ghcr.io/jdavid77/<name>-mcp`
- speaks streamable HTTP (stateless) on `:8080/mcp`
- is a single static binary on distroless, running as non-root

## MCPs

### akeyless

Lists [Akeyless](https://www.akeyless.io) item names (never values), finds the ones nothing in the cluster uses, and deletes them.

| Tool | What it does |
|---|---|
| `list_items` | Lists item names and types under a path (default `/`), walking subfolders |
| `find_unused_secrets` | Returns the items under `/` that no ExternalSecret references, marking those under `/cert-manager/` as `protected` (used outside the cluster) |
| `delete_unused_secrets` | Deletes a list of names in one call, skipping any that `find_unused_secrets` doesn't report as unused right now and anything `protected` |
| `find_empty_folders` | Returns the folders with no item at any depth, deepest first, leaving out `/` and `/cert-manager` |
| `delete_empty_folders` | Deletes a list of folders in one call, deepest first, skipping any that isn't empty right now, `/` and `/cert-manager`. Before each delete it lists the folder again and refuses if anything is in it |

| Env | |
|---|---|
| `AKEYLESS_ACCESS_ID` | Access ID of an API key with `list` on the paths to scan, plus `delete` for `delete_unused_secrets` and `delete_empty_folders` |
| `AKEYLESS_ACCESS_KEY` | Its access key |
| `AKEYLESS_API_URL` | Optional, defaults to `https://api.akeyless.io` |

`find_unused_secrets` reads ExternalSecrets from the Kubernetes API, so the pod's ServiceAccount needs:

```yaml
rules:
  - apiGroups: ["external-secrets.io"]
    resources: ["externalsecrets"]
    verbs: ["list"]
```

### pushover

Sends notifications through [Pushover](https://pushover.net).

| Tool | What it does |
|---|---|
| `send_notification` | Sends a `title` and `message` (cut to Pushover's 250 / 1024 character limits). The message is sent with Pushover's HTML formatting on |

| Env | |
|---|---|
| `PUSHOVER_TOKEN` | Application token |
| `PUSHOVER_USER_KEY` | User key to notify |

## Running locally

```sh
docker build --build-arg MCP=pushover -t pushover-mcp .
docker run --rm -p 8080:8080 -e PUSHOVER_TOKEN=... -e PUSHOVER_USER_KEY=... pushover-mcp
```

Then point an MCP client (e.g. the [MCP Inspector](https://github.com/modelcontextprotocol/inspector)) at `http://localhost:8080/mcp`.

## Releases

Pushes to `main` build only the MCPs whose folder changed, or all of them when `go.mod`, `go.sum` or the `Dockerfile` change. Each build is tagged `latest` and `vYYYY.MM.DD.NN`, counting on from the newest tag already in GHCR. Pull requests lint and build without pushing.

## Adding an MCP

1. Create `<name>/main.go`.
2. Add a `<name>` path filter in [`.github/workflows/build.yaml`](.github/workflows/build.yaml).
3. Add a section to this README.

## License

[MIT](LICENSE)
