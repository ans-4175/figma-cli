---
name: figma-cli
description: Dissect Figma designs — node structure, layout, colors, text, components, comments, versions via REST API + remote MCP daemon. Use when the user shares a Figma URL and wants the design explained, audited, compared, or turned into specs/ASCII wireframe/code.
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
| Zoom into a specific frame | `understand <url> --depth 8` or `--n <id>` | Pull node-id from the user's URL |
| Raw JSON structure | `node <url> --depth 6` | For precise specs (exact gap, padding, colors) |
| List of pages + seat role | `file <url>` | **Check `Role:` → determines rate-limit discipline** |
| File components & styles | `components <url>` | Instances in a section are scanned from the outline |
| Collaboration context | `comments` / `versions` / `dev-resources` | Tier 2–3, looser quotas |
| MCP capabilities | `mcp tools` then `mcp call --tool T` | OAuth on first use; mind the quota notes |
| Setup health check | `doctor` | Token, daemon, REST |

Every command has `-h` for flag details. Token resolution order: `--token` >
`--token-file` > named account > env `FIGMA_TOKEN`.

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
2. `figma-cli node <url> --depth 8` on the node you're implementing —
   grab exact values: itemSpacing, padding, cornerRadius, hex fill,
   fontFamily/fontSize.
3. Check whether the element already exists in the codebase first —
   update what's there, don't generate a duplicate.
4. Reuse identified components (map ⧉ instances to codebase components),
   don't hardcode them again.
5. `get_code` (MCP) is for Dev/Full seats only, and only when genuinely
   needed — read tools drain the viewer/collab monthly quota.

## Workflow 3 — MCP (optional)

1. `figma-cli mcp tools` — this also triggers OAuth (opens a browser,
   once).
2. `figma-cli mcp call --tool <name> --args '{...}' [url] [nodeID]` —
   fileKey/nodeId arguments are auto-filled from the URL.
3. `mcp status` / `mcp reset` to check/clear OAuth per alias.
4. Remember: MCP read tools follow the same REST Tier 1 rules + daily cap.
   Write tools (add_figma_file, whoami, etc.) don't consume the read
   quota.

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
