---
name: figma-cli
description: Set up and use Figma CLI through two separate auth engines — PAT for REST reads and explicit OAuth/PKCE for remote MCP tools. Understand designs, inspect layers/components, troubleshoot auth, and use server-exposed canvas tools only with approved access and scope.
---

# figma-cli

CLI for reading Figma designs. Two engines: REST API (PAT) for structure,
remote MCP (OAuth, via daemon) for extra capabilities. The primary output is
a **text outline** — no image rendering (GET image is Tier 1, the tightest
quota there is).

## Quick reference

| Need | Command | Notes |
| --- | --- | --- |
| Understand a design from a URL | `understand <url>` | ⭐ Always start here |
| Zoom into a specific frame | `understand --depth 8 <url>` or `understand --n <id> <url>` | Flags must precede positional URLs |
| Raw JSON structure | `node --depth 6 <url>` | For precise specs (exact gap, padding, colors) |
| List of pages + seat role | `file <url>` | **Check `Role:` → determines rate-limit discipline** |
| File components & styles | `components <url>` | Instances in a section are scanned from the outline |
| Collaboration context | `comments` / `versions` / `dev-resources` | Tier 2–3, looser quotas |
| MCP setup / capabilities | `mcp login`, then `mcp tools --json` | Foreground OAuth consent first; no PAT fallback |
| Setup health check | `doctor` | Token, daemon, REST |

Read this skill with `figma-cli --skills` (aliases: `--skill`, `skill`).
Every command has `-h` for flag details. **Put flags before positional URLs**;
Go flag parsing stops at the first positional argument.

## Choose the authentication engine first

| Engine | Commands | Credential | Browser needed? | Canvas edits? |
| --- | --- | --- | --- | --- |
| REST | `understand`, `file`, `node`, `components`, `comments`, `versions`, `dev-resources` | Personal Access Token (PAT), header `X-Figma-Token` | No | No page/layer/component creation via REST |
| Remote MCP | `mcp login/tools/call/status/reset` | Separate OAuth access + refresh tokens | User login/consent during explicit `mcp login` | Only if the server exposes a write tool and the user has the required access/seat/plan |

A PAT does **not** authenticate MCP. MCP OAuth does **not** supply a PAT to
REST commands. Identically named PAT and MCP account aliases are independent.
Never send credentials to an LLM, research provider, source control, or logs.

## Setup A — PAT for REST (default)

1. The user creates a PAT in Figma Settings → Security → Personal access
   tokens. Select only required scopes, such as `file_content:read` for
   nodes, and metadata/library/comment/version read scopes when needed.
   Token scopes never grant file access the account does not already have.
2. Have the user store the token in an owner-only file. Do not print or
   paste its contents into chat. Raw token text and a
   `FIGMA_TOKEN=...` line are both accepted.
3. Use that file in-place; flags go before the URL:

```bash
# One command, without changing the configured token source:
figma-cli understand --token-file "$HOME/.config/goers/figma-token" '<Figma URL>'

# Optional persistent configuration (stores the path, not another token copy):
figma-cli config set token-file "$HOME/.config/goers/figma-token"
figma-cli config get
figma-cli understand '<Figma URL>'
```

On Unix, the credential file should have mode 0600 inside a private directory.
On Windows, restrict its ACL to the intended user. Do not change permissions
of unrelated/shared directories automatically.

REST token precedence: `--token` > `--token-file` > configured `token_file` >
named/default PAT account > environment `FIGMA_TOKEN`. A configured token
file therefore wins over `--account`; use `config clear token-file` only
when the user intentionally switches to saved PAT accounts or the environment.
The CLI also supports `accounts add/list/use/remove`, `FIGMA_TOKEN`, and
`--token`; agents should prefer `--token-file` rather than putting a raw
secret in argv or shell history.

PAT expiration/revocation requires user rotation; PATs do not use MCP refresh
tokens. REST 401 means re-check the credential, 403 means check scopes and
file access, and 429 means respect Retry-After. Do not bypass limits with
another account or endpoint. REST exceptions such as variable/comment writes
do not constitute a canvas node-write API; the current REST CLI commands
listed above are reads.

## Workflow 1 — Understand a design from a URL (primary)

### Step 0: Read the URL
- `fileKey` and `node-id` come straight from the user's URL
  (`.../design/<fileKey>/Name?node-id=451-36249` → node `451:36249`).
- A URL without a node-id means the user wants a file overview, not a
  single frame.

### Step 1: Check access & context
- Run `figma-cli file <url>`.
- Pay attention to **`Role:`** — `viewer`/`collab` means Tier 1 limits
  apply per MONTH (±6–20 calls) for GET file/nodes/image. Discipline in
  Steps 2–4 becomes mandatory.
- Note the file name + lastModified for context.

### Step 2: Outline the target frame
- `figma-cli understand <url>` (follow the node-id from the user's URL).
- Default depth is 14; the outline truncates at 2000 lines if the node is
  huge (a full page/CANVAS) → don't force it: break it up — grab
  `--n <child-id>` per important section, or `--depth 3–4` for a rough map
  first.
- While reading the outline, extract: section hierarchy → auto-layout
  (direction, gap, padding) → fill colors (hex) → text + font →
  components (⧉) → hidden elements.

### Step 3: Components & design system
- `figma-cli components <url>` — components/sets/styles owned by the file.
- Components shown in the outline (⧉ instance) may come from an external
  team library — an empty file components endpoint ≠ no components used.
- Map instance → component name for reporting ("Button used 12×").

### Step 4: Additional context (optional, looser tier)
- `comments <url>` — designer intent, feedback, decisions.
- `versions <url>` — history & direction of iteration.
- `dev-resources <url>` — related code/docs/Storybook links.

### Step 5: Synthesis
- Summarize: layout system, color palette, typography, recurring
  components, design intent. Include key measurements (px) and any
  readable tokens.
- If a visual is requested: build an ASCII wireframe from the outline
  (don't guess — ground it in outline data; call out anything not
  visible).

## Workflow 2 — Design → spec/code

1. Run Workflow 1 through Step 3 first.
2. `figma-cli node --depth 8 <url>` on the node you're implementing —
   grab exact values: itemSpacing, padding, cornerRadius, hex fill,
   fontFamily/fontSize.
3. Check whether the element already exists in the codebase first —
   update what's there, don't generate a duplicate.
4. Reuse identified components (map ⧉ instances to codebase components),
   don't hardcode them again.
5. `get_code` (MCP) is for Dev/Full seats only, and only when genuinely
   needed — read tools drain the viewer/collab monthly quota.

## Setup B / Workflow 3 — Remote MCP OAuth

This flow requires the patched source build; the old released v1.0.0 binary
has no explicit `mcp login` command and must not be described as patched.

### Eligibility and explicit login

The endpoint is `https://mcp.figma.com/mcp`. Figma allowlists remote-MCP
registrations by exact client name. Following the documented Hermes Agent
compatibility default, DCR registers under `Claude Code` and the login
output says so. Login and consent remain the user's own; no other client's
credentials or tokens are used. Figma can change the allowlist anytime —
catalog listing remains the official path. Writing additionally requires
edit permission and the applicable seat/plan.

```bash
# After upgrading the binary, stop the OLD daemon when no command is active:
figma-cli daemon stop

# Run in an interactive user terminal, NOT an unattended agent job:
figma-cli mcp login --account default
# User signs in and explicitly allows access in the browser.

figma-cli mcp status --account default
figma-cli mcp tools --account default --json
```

Login runs discovery → Dynamic Client Registration (DCR) → user consent
with PKCE S256 → validated loopback callback → code exchange → private token
storage. One listener/port is held open throughout; callback state is checked,
and callbacks are single-use. The agent must not perform the user's login or
consent for them. `mcp tools/call` never launch an interactive login in the
background; missing/invalid credentials return an actionable login error.

A DCR rejection stops before browser login; the allowlisted name may stop
working if Figma tightens it. Never retry registration in a loop and never
borrow another client's credentials or tokens. Do not promise live canvas
writes until login, tool discovery, and an approved test mutation succeed.

### Token reuse and unattended operation

OAuth credentials are stored separately per alias at
`~/.figma-cli/mcp-auth/<alias>.json`. Directory/file modes are 0700/0600
on Unix; protect the user profile with appropriate ACLs on Windows.
Account aliases use letters, digits, underscore, or hyphen only.

The daemon reuses saved tokens across CLI calls and process restarts. It
refreshes before access-token expiry and persists rotated refresh tokens.
An unexpected HTTP 401 triggers at most one refresh + retry. Read operations
retry transient 502/503/504 and 429 (honoring Retry-After, capped at 30s) up
to three attempts; 403 and network timeouts are not retried. Mutations such
as use_figma, generate_*, create_*, send_*, and uploads are NEVER auto-retried:
the server may have applied the change before the failed response, and a
replay could duplicate it. Check the file state, then retry deliberately.
A failed refresh/save is reported, not silently treated as success or used
to trigger a browser login. The last saved credentials are not deleted.
A persistence error after provider-side rotation may require a new login.

Private `<alias>.json.lock` files serialize login/refresh/reset across processes.
If a lock remains after a crash, verify the owning process is no longer active
before manually removing that specific stale lock. Never delete locks blindly.
Credentials are written via a private temporary file and rename.

`mcp reset --account default` deletes LOCAL OAuth credentials and the local
session; it does not revoke the authorization at Figma. Use Figma account
settings to revoke remotely. Reset/logout is an explicit user action.

### Remote / Hermes host setup

Install and validate the new binary locally first. Do not deploy to Hermes
without explicit approval. On the target host, run login as the **same Unix
user/HOME** that will run the agent, with persistent private storage.
A login on the laptop does not log the server in; do not automatically copy
credentials between hosts/users. The Figma desktop app need not stay open.

There is no device-code flow in this CLI. The callback binds to
`127.0.0.1:<PORT>` on the host running login. If the user opens the login
URL on their laptop for a headless server, they need a private SSH tunnel
for the displayed callback port before opening the URL, for example:

```bash
# On the user's laptop; replace PORT with redirect_uri's actual port:
ssh -N -L 127.0.0.1:PORT:127.0.0.1:PORT user@hermes-host
```

Do not bind the OAuth callback to 0.0.0.0 or publish it through ingress.
The user must finish consent within the five-minute callback window.
If DCR is rejected, no browser URL/callback phase is reached.

### Discover before using tools

Inspect `mcp tools --json` for exact names and inputSchema. Never assume
tool names or schemas. Then call only a tool the server actually exposes:

```bash
figma-cli mcp call --account default --tool '<actual-tool-name>' \
  --args '<JSON matching inputSchema>' '<Figma URL>'
```

fileKey/nodeId can be filled from the URL when omitted from the JSON.
Prefer REST for ordinary reads. If the user approves a canvas-write test,
use the actual write tool (official docs currently describe `use_figma`),
restrict changes to the approved file, create/edit a small named page,
frame/layer, and component, then verify the returned node types/IDs and
persisted result. Do not count a screenshot, comment, or tool listing as
proof that native canvas editing works.

Read tools have seat/plan quotas. Some tools are quota-exempt; consult
current official limits, and never infer authorization from quota exemption.

Official references (checked 2026-11-04):
- https://developers.figma.com/docs/rest-api/scopes/
- https://developers.figma.com/docs/figma-mcp-server/remote-server-installation/
- https://developers.figma.com/docs/figma-mcp-server/write-to-canvas/
- https://developers.figma.com/docs/figma-mcp-server/rate-limits-access/

## General instructions

- **Respect 429s**: the error carries `Retry-After` (seconds) and
  `X-Figma-Rate-Limit-Type` (low=viewer/collab, high=dev/full). Don't
  retry early; don't hop to another Tier 1 endpoint as a "workaround."
- **Conserve Tier 1** (GET file/nodes/image): think in batches before
  calling in batches — one `understand` call analyzed deeply beats many
  small `node` calls.
- `Role: viewer` in the `file` output = apply monthly-budget discipline,
  and don't suggest rendering/get_image to the user.
- Valid URL shape: `figma.com/(design|file|proto|board|site)/<fileKey>...`.
  node-id `451-36249` ≡ `451:36249`.
- Runtime dir `~/.figma-cli/`: `accounts.json` (multi-account PAT, 0600),
  `config.json` (`token_file`), `mcp-auth/<alias>.json` (OAuth),
  `daemon.sock/pid/log`.
- If you need an endpoint not yet in the CLI, check the official docs:
  REST reference https://www.figma.com/developers/api ·
  rate limits https://developers.figma.com/docs/rest-api/rate-limits/ ·
  MCP https://developers.figma.com/docs/figma-mcp-server/
