# figma-cli

Your coding agent can't see Figma. It has no eyes. It just squints at the URL you pasted and hallucinates a design system.

`figma-cli` fixes that — it hands your terminal (and whatever LLM lives in it) actual X-ray vision into Figma files: layer trees, colors, auto-layout, components, comments, version history, dev resources, even the remote MCP tools (`get_code`, `get_image`, etc.) — all from the command line, no browser tab required.

Talks to two APIs so it can be greedy about context:
- **REST API** (`api.figma.com`) — fast, structured, your PAT does the talking.
- **Remote MCP** (`mcp.figma.com`) — OAuth + PKCE, held open by a lightweight background daemon so you're not re-authing every single call.

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

```bash
figma-cli accounts add default figd_xxxxxxxxxxxx
figma-cli understand https://www.figma.com/design/<fileKey>/Name?node-id=1-2
figma-cli doctor   # because trust, but verify
```

That last command checks your accounts, tokens, daemon, and REST connectivity in one shot — the CLI equivalent of a doctor tapping your knee with a little hammer.

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

Tokens and runtime state live in `~/.figma-cli/` (`%USERPROFILE%\.figma-cli\` on Windows) — resolved automatically per-OS via `os.UserHomeDir()`, never hardcoded, never committed, never your problem to think about again. Token resolution order: `--token` flag > `--token-file` > saved account > `FIGMA_TOKEN` env var. Pick your poison.

## Commands

Run `figma-cli help` for the full menu. Short version: it understands files, dumps nodes, lists components/styles, reads comments, walks version history, surfaces dev resources, and drives the Figma MCP server — all so your coding agent stops guessing what "that blue-ish button, you know the one" looks like.
