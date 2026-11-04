# figma-cli

Your coding agent can't see Figma. It has no eyes. It just squints at the URL you pasted and hallucinates a design system.

`figma-cli` fixes that — it hands your terminal (and whatever LLM lives in it) actual X-ray vision into Figma files: layer trees, colors, auto-layout, components, comments, version history, dev resources, even the remote MCP tools (`get_code`, `get_image`, etc.) — all from the command line, no browser tab required.

Talks to two APIs so it can be greedy about context:
- **REST API** (`api.figma.com`) — fast, structured, your PAT does the talking.
- **Remote MCP** (`mcp.figma.com`) — explicit foreground OAuth login + PKCE S256; a lightweight daemon reuses and refreshes persisted tokens. Figma must also permit this client.

Runs natively everywhere a terminal does: **macOS (Intel & Apple Silicon), Linux (amd64 & arm64), Windows (amd64 & arm64)**. Single static binary, zero external dependencies — just Go's standard library doing all the heavy lifting.

## Install

Grab the binary for your OS from [Releases](../../releases). No installer, no package manager drama, no "please restart your computer."

```bash
# macOS / Linux
chmod +x figma-cli-<os>-<arch>
sudo mv figma-cli-<os>-<arch> /usr/local/bin/figma-cli
```

```powershell
# Windows — drop figma-cli-windows-<arch>.exe somewhere in PATH,
# or just rename it to figma-cli.exe and call it a day.
```

## Quick start

### PAT → REST (default, no browser)

Have the user create a least-privilege Figma PAT and save it in a private file.
Do not paste secrets into argv, chat, logs, or source control.

```bash
# Flags must precede positional URLs.
figma-cli understand --token-file "$HOME/.config/figma-cli/token" \
  'https://www.figma.com/design/<fileKey>/Name?node-id=1-2'

# Optional: persist the path for subsequent REST commands.
figma-cli config set token-file "$HOME/.config/figma-cli/token"
figma-cli --skills
```

The file can contain raw token text or `FIGMA_TOKEN=...`. On Unix, use mode
0600 in a private directory; on Windows, restrict the ACL to the intended user.
Scopes and file access still apply. PAT expiration requires user rotation.
REST reads do not imply permission or API support to create canvas nodes.

### OAuth → remote MCP (separate credentials)

**These commands describe the patched source build, not the older v1.0.0
binary.** After upgrading, stop the old daemon when no command is active.

```bash
figma-cli daemon stop
figma-cli mcp login --account default
# The user logs in and explicitly consents in their browser.
figma-cli mcp status --account default
figma-cli mcp tools --account default --json
```

A PAT does not log MCP in, and MCP OAuth does not provide REST credentials.
Login performs discovery, DCR, PKCE S256, and a state-validated, single-use
loopback callback. Saved access/refresh tokens are reused across process
restarts and refreshed automatically; failures are surfaced instead of silently
opening a browser or deleting credentials. Login/refresh/reset use a
cross-process lock. Never remove a stale lock without checking its owner.

Figma allowlists remote-MCP registrations by exact client name. Like Hermes
Agent's documented default, login registers under `Claude Code` and says so
in its output; your own login and consent are still required, and no other
client's credentials are used. The allowlist can change anytime — the Figma
MCP catalog remains the official path. Discover actual tools/schemas before
claiming canvas edits; write tools additionally need file permission, seat,
and plan.

For a headless Hermes host, login must run under the agent's own user/HOME
with persistent private storage. A browser on a different machine needs a
private SSH forward for the printed loopback callback port; never expose the
callback publicly. Laptop login is not server login. Setup, troubleshooting,
revocation, and tool-use guardrails are included in `figma-cli --skills`.
Deploying to Hermes remains a separate approval.

`figma-cli doctor` checks both setup and REST connectivity; it is not proof
that MCP OAuth or canvas writes work.

## Build from source

Needs Go ≥ 1.23. No `go mod download` required — this thing has zero dependencies, which is either impressive minimalism or mild masochism, depending on who you ask.

```bash
go build -o figma-cli .
```

Cross-compiling is stupidly easy because Go just... does that:

```bash
GOOS=windows GOARCH=amd64 go build -o figma-cli-windows-amd64.exe .
```

## Credentials & config

Tokens and runtime state live in `~/.figma-cli/` (the user's profile on Windows), resolved with `os.UserHomeDir()`. REST PAT precedence: `--token` > `--token-file` > configured `token_file` > named/default PAT account > `FIGMA_TOKEN`. A configured token file wins over `--account`. Prefer referencing an existing private token file. MCP OAuth is independent, stored at `mcp-auth/<alias>.json`, with private temporary-file replacement and rotation locks. `mcp reset --account default` removes local credentials only; remote revocation is done in Figma account settings.

## Commands

Run `figma-cli help` for the full menu. Short version: it understands files, dumps nodes, lists components/styles, reads comments, walks version history, surfaces dev resources, and drives the Figma MCP server — all so your coding agent stops guessing what "that blue-ish button, you know the one" looks like.
