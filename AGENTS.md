# AGENTS.md

Operational notes for anyone (human or agent) working on this repo. If you're
looking for *how to use* figma-cli as an end user, see `README.md` and
`skills/figma-cli.md` instead. This file is about the codebase itself.

## Mental model: two engines, one CLI

figma-cli talks to Figma through **two completely separate transports**,
picked per-command:

```
                         ┌─────────────────┐
  figma-cli <cmd>  ───►  │  REST commands   │──► https://api.figma.com/v1
                         │  (rest.go,       │    (X-Figma-Token header,
                         │   commands.go)   │     synchronous HTTP GET)
                         └─────────────────┘

  figma-cli mcp *  ───►  ┌─────────────────┐     unix/named-pipe socket
                         │  daemon client   │──►  ~/.figma-cli/daemon.sock
                         │  (daemon.go)     │
                         └────────┬────────┘
                                  │ auto-spawns if not running
                                  ▼
                         ┌─────────────────┐
                         │ figma-cli serve  │──► https://mcp.figma.com/mcp
                         │ (background proc)│    (OAuth Bearer token,
                         │  holds MCP       │     Streamable HTTP / SSE,
                         │  sessions        │     JSON-RPC 2.0)
                         └─────────────────┘
```

**REST path** (`understand`, `file`, `node`, `components`, `comments`,
`versions`, `dev-resources`): stateless, one HTTP call per invocation, auth
via Personal Access Token (`X-Figma-Token` header). This is the cheap, fast,
default path — no daemon involved at all.

**MCP path** (`mcp tools`, `mcp call`): talks to Figma's *remote* MCP server
over Streamable HTTP, which requires OAuth 2.0 + PKCE (browser flow) instead
of a static token. Because OAuth tokens are annoying to re-negotiate on every
single CLI invocation, this path runs through a **background daemon**
(`figma-cli serve`) that:

- holds one live MCP session (`mcpclient.go`) per account alias
- refreshes OAuth tokens transparently (`oauth.go`)
- exits itself after 15 min idle (`idleTTL` in `daemon.go`)
- is auto-spawned by the client on first MCP command (`ensureDaemon()`),
  detached from the calling terminal (see build-tag section below)

Client ↔ daemon protocol: newline-delimited JSON over a Unix domain socket
(`~/.figma-cli/daemon.sock`). Go's `net.Dial("unix", ...)` / `net.Listen`
work on Windows too (AF_UNIX since Win10 1803+), so this same code path runs
unmodified cross-platform — no separate Windows IPC implementation needed.

## Why some files are split by OS (build tags)

Almost the entire codebase is portable stdlib Go. The **only** OS-specific
bit is *how the daemon process gets detached* from the terminal that spawned
it, isolated into two files selected at compile time via Go build tags:

| File | Build tag | What it does |
|---|---|---|
| `daemon_unix.go` | `//go:build !windows` | `syscall.SysProcAttr{Setsid: true}` — classic Unix session-detach |
| `daemon_windows.go` | `//go:build windows` | `CREATE_NEW_PROCESS_GROUP \| DETACHED_PROCESS` flags — Windows has no setsid equivalent |

Both expose the same function signature `detachDaemonProcess(cmd *exec.Cmd)`,
called from `ensureDaemon()` in `daemon.go`. This is the *only* reason
`GOOS=windows go build` used to fail before these files existed — everything
else (path handling via `filepath.Join`, home dir via `os.UserHomeDir()`,
signals, sockets, HTTP, OAuth browser-open) already worked unmodified on
Windows.

If you add new OS-specific behavior in the future, follow the same pattern:
isolate it behind a build-tagged file pair, keep the calling code identical.

## Credentials & runtime state

Never hardcoded, never in the repo. Resolved at runtime via
`os.UserHomeDir()` (`accounts.go`):

| OS | Runtime dir |
|---|---|
| macOS / Linux | `$HOME/.figma-cli/` |
| Windows | `%USERPROFILE%\.figma-cli\` |

Contents: `accounts.json` (PAT per alias, mode 0600), `config.json`
(token-file override), `mcp-auth/<alias>.json` (OAuth tokens),
`daemon.sock` / `daemon.pid` / `daemon.log`.

Token resolution order (see `resolveToken()` in `accounts.go`):
`--token` flag > `--token-file` (flag or config) > named `--account` /
default account > env `FIGMA_TOKEN`.

## Build matrix

Zero external Go dependencies (`go.mod` is stdlib-only) — no `go.sum`,
nothing to vendor, cross-compilation is just `GOOS`/`GOARCH` env vars.

Supported targets (see `.github/workflows/release.yml`):

| GOOS | GOARCH |
|---|---|
| darwin | amd64, arm64 |
| linux | amd64, arm64 |
| windows | amd64, arm64 |

Build flags used in CI: `-trimpath -ldflags="-s -w -X main.version=<ver>"`.

- `-trimpath` strips local filesystem paths from the binary
- `-s -w` strips debug symbols (smaller binary, ~6MB vs ~9MB)
- `-X main.version=...` injects the release tag into the `version` var in
  `main.go` (declared as `var version = "dev"`, **must stay a `var`**, not a
  `const` — `-ldflags -X` can only override package-level string variables)

To reproduce a CI build locally:

```bash
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build \
  -trimpath -ldflags="-s -w -X main.version=1.0.0" \
  -o figma-cli-windows-amd64.exe .
```

## How releases work

The `Release` workflow (`.github/workflows/release.yml`) triggers on any tag
push matching `v*`, or manually via `workflow_dispatch`.

```
git tag -a vX.Y.Z -m "figma-cli vX.Y.Z"
git push origin vX.Y.Z
```

That's it — no other steps needed. The workflow then:

1. **`build` job** (matrix, 6 parallel runs): checkout → setup Go 1.23 →
   cross-compile → package (`.tar.gz` for macOS/Linux, `.zip` for Windows,
   because Windows users expect zip) → upload as workflow artifacts.
2. **`checksums` job** (depends on all 6 builds): downloads every artifact,
   runs `sha256sum * > SHA256SUMS.txt`, then creates a GitHub Release via
   `softprops/action-gh-release` with all 6 archives + the checksum file
   attached, release notes auto-generated from commits since the last tag.

Whole pipeline takes ~50s end to end. The release is published immediately
(not draft, not prerelease) — there's no manual approval gate. If you want a
dry run first, use `workflow_dispatch` with a throwaway tag like
`v0.0.0-test`, verify assets, then delete that release + tag before cutting
the real one.

### Version numbering

No enforced scheme yet — just tag `vMAJOR.MINOR.PATCH`. The `v` prefix is
stripped before being embedded as `main.version`, so tag `v1.2.3` → binary
reports `figma-cli 1.2.3`.

### Verifying a release download

```bash
gh release download vX.Y.Z -R ans-4175/figma-cli
shasum -a 256 -c SHA256SUMS.txt
file figma-cli-windows-amd64.exe   # expect: PE32+ executable (console) x86-64, for MS Windows
```

## File map (quick orientation)

| File | Responsibility |
|---|---|
| `main.go` | CLI entrypoint, command dispatch, `usage()` text, `version` var |
| `commands.go` | REST command implementations (understand/file/node/components/...) + accounts/mcp/doctor subcommands |
| `rest.go` | Figma REST API client, URL parsing (`parseFigmaRef`), outline builder |
| `accounts.go` | Multi-account PAT storage (`~/.figma-cli/accounts.json`), token resolution |
| `config.go` | Lightweight config (`token_file` override) |
| `daemon.go` | Daemon protocol (client + server), session cache, idle watchdog |
| `daemon_unix.go` / `daemon_windows.go` | OS-specific process detachment (see build-tags section above) |
| `mcpclient.go` | Minimal MCP client: JSON-RPC over Streamable HTTP, SSE parsing |
| `oauth.go` | OAuth 2.0 + PKCE flow for Figma remote MCP (discovery, DCR, browser auth, token refresh) |
| `skill.go` | Embeds `skills/figma-cli.md` into the binary (`go:embed`) for `figma-cli skill` |
| `util.go` | Small cross-file helpers |
