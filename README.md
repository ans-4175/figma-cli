# figma-cli

**Your coding agent can't see Figma.** You paste a link, it nods politely, then invents a "blue-ish button" that exists in no design file anywhere.

`figma-cli` gives it glasses. One small program, run from the terminal, that lets your AI agent (Claude Code, Codex, Cursor, Hermes, whatever lives in your terminal) actually *read* a Figma file: the layers, the colors, the spacing, the text, the components, the comments, the version history. No browser tab, no screenshots pasted into chat, no guessing.

> Designer: "It's in Figma."
> Agent, before: "Sure! Here's a purple gradient you didn't ask for."
> Agent, after: "Card, 16px padding, 8px gap, `#1F6FEB`, Inter 14/20. Building it now."

---

## What can I use it for?

- **Design → code without the telephone game.** Paste a Figma frame link, ask your agent to build it. It reads the real layout, spacing and colors instead of eyeballing a screenshot.
- **"Explain this file to me."** Inherited a 40-page Figma file nobody documented? Ask the agent for a tour: pages, main screens, components, styles.
- **Design system audits.** "Which components and styles does this library have? Which screens break the rules?"
- **Handle designer comments.** Read every comment on a file, turn them into a to-do list, and (with your OK) reply to a thread once it's fixed.
- **"What changed?"** Walk the version history before a release or a review.
- **Find the right file.** List the files and folders in a team or project when all you have is "it's somewhere in the Marketing folder".
- **Dev handoff links.** Pull the Storybook / docs / code links attached to a design.
- **Edit the canvas (advanced, optional).** With the MCP setup below, an agent can use Figma's own remote tools — and, where your plan and permissions allow, create or change things on the canvas. Always with your approval.

---

## Install

Download the file for your computer from [Releases](../../releases). No installer, no package manager, no "please restart".

| You have | Download |
|---|---|
| Mac (M1/M2/M3/M4) | `figma-cli-darwin-arm64` |
| Mac (Intel) | `figma-cli-darwin-amd64` |
| Linux | `figma-cli-linux-amd64` (or `-arm64`) |
| Windows | `figma-cli-windows-amd64.exe` (or `-arm64`) |

**Mac / Linux** — make it runnable and put it on your PATH:

```bash
chmod +x figma-cli-<os>-<arch>
sudo mv figma-cli-<os>-<arch> /usr/local/bin/figma-cli
figma-cli help   # if this prints a menu, you're in
```

**Windows** — rename it to `figma-cli.exe` and drop it in a folder on your PATH.

---

## Setup: two keys, two doors

figma-cli talks to Figma in two ways. They use **different keys**, and one does not open the other's door.

| | 🔑 Personal Access Token (PAT) | 🎟️ MCP login |
|---|---|---|
| What it unlocks | Reading files: layers, colors, comments, versions, folders | Figma's own remote tools (code hints, images, canvas edits where allowed) |
| Do I need it? | **Yes — start here** | Optional, for power users |
| Feels like | Copy a key into a file, done | Log in through your browser, like any "Sign in with…" |

### 🔑 Setup 1 — Personal Access Token (5 minutes)

1. **Make the token.** In Figma: avatar → **Settings** → **Security** → **Personal access tokens** → *Generate new token*.
   Give it only what it needs. For most people, read-only is plenty:
   - `file_content:read` (required — this is the "see the design" one)
   - `file_comments:read`, `file_versions:read`, `file_dev_resources:read`, `library_content:read` (nice to have)
   - `folders:read` (if you want to browse team folders)
   - `file_comments:write` only if you want the agent to reply to comments

   Figma shows the token **once**. Copy it.

2. **Put it in a private file.** Not in chat, not in a Slack DM to yourself, not in the repo. Just a file only you can read.

   Mac / Linux (paste when asked; nothing will show while you type, that's normal):

   ```bash
   mkdir -p ~/.config/figma-cli && chmod 700 ~/.config/figma-cli
   printf 'Paste your Figma token, then Enter: '; read -rs T; echo
   printf '%s\n' "$T" > ~/.config/figma-cli/token; unset T
   chmod 600 ~/.config/figma-cli/token
   ```

   Windows: create `%USERPROFILE%\.config\figma-cli\token` in Notepad, paste the token, save. Make sure only your user account can open that file.

3. **Tell figma-cli where it is** (once, it remembers):

   ```bash
   figma-cli config set token-file ~/.config/figma-cli/token
   figma-cli doctor
   ```

   `doctor` checks your setup and pings Figma. Green-ish output = you're good.

> 🧯 Token leaked or expired? Delete it in Figma settings, make a new one, overwrite the file. Tokens don't renew themselves.

### 🎟️ Setup 2 — MCP login (optional)

This connects figma-cli to Figma's remote MCP server so your agent can use Figma's own tools. You log in with your normal Figma account in the browser — no token copying.

```bash
figma-cli daemon stop               # only if you upgraded from an older version
figma-cli mcp login                 # opens your browser — log in and click Allow
figma-cli mcp status                # should say you're logged in
figma-cli mcp tools                 # lists what Figma lets you use
```

A few honest footnotes:

- **You do the login, not your agent.** It's your account and your consent. Run `mcp login` yourself (in Claude Code you can type `! figma-cli mcp login`).
- **Figma decides who gets in.** Figma only allows approved apps to use its MCP server. figma-cli registers as `Claude Code` (it tells you so during login). Figma can change that list anytime; if login gets refused, that's Figma's call, not a broken install.
- **Editing the canvas needs more than a login:** edit access to the file and a Figma plan/seat that allows it.
- After that, it stays logged in and refreshes itself. To log out locally: `figma-cli mcp reset`. To fully revoke, remove the app in your Figma account settings.
- Need MCP on a remote server (e.g. a Hermes host)? See `figma-cli --skills` — the login must happen as the server's own user, not on your laptop.

---

## 🤖 Let your agent set it up

Already installed the binary? Paste one of these into your agent and let it hold your hand.

**Setup PAT (most people):**

```text
Help me set up figma-cli for reading Figma files.
First run `figma-cli --skills` and follow its "Setup A — PAT" section.
Rules: never ask me to paste the token into chat, never print it, never put it
in a command argument. Guide me to create a least-privilege PAT and save it to
a private file at ~/.config/figma-cli/token (mode 600), then run
`figma-cli config set token-file` and `figma-cli doctor` and tell me the result.
```

**Setup MCP login:**

```text
Help me connect figma-cli to Figma's remote MCP server.
Read `figma-cli --skills`, section "Setup B — Remote MCP OAuth".
Do NOT run `mcp login` or `mcp reset` yourself — tell me the exact command and
I'll run it and approve in my browser. Afterwards, run `figma-cli mcp status`
and `figma-cli mcp tools --json` and summarize which tools I actually have.
Don't promise canvas edits unless a write tool shows up in that list.
```

**Teach your agent to use it (put this in CLAUDE.md / AGENTS.md):**

```text
For any Figma link, use figma-cli. Run `figma-cli --skills` once to learn it.
Start every Figma task with `figma-cli understand '<url>'`. Read with REST,
edit the canvas only via MCP and only after I approve each change.
```

---

## 🚀 First try

Open any Figma frame, right-click it → **Copy link to selection**. Then:

```bash
figma-cli understand 'https://www.figma.com/design/<fileKey>/Name?node-id=1-2'
```

You'll get a readable outline of that frame: what's inside, how it's laid out, colors, fonts, text. That's exactly what your agent sees.

Then hand it over:

```text
Here's a Figma frame: <paste link>
Use figma-cli to understand it, then build it as a React component
with the exact spacing, colors and typography. Tell me anything ambiguous.
```

Other things worth trying on day one:

```text
Give me a tour of this Figma file: pages, main screens, and the components it uses. <link>
```
```text
List all open comments on this file and turn them into a checklist. <link>
```
```text
What changed in this file over the last few versions? <link>
```

---

## When things go sideways

| You see | Probably means |
|---|---|
| `403` | Your token can't open that file, or is missing a scope. Check you have access and the scope list above. |
| `401` | Token rejected — wrong, revoked, or missing the scope for that action. Read the message; it's not always "expired". |
| `404` | Wrong link, deleted node, or a file your account can't see. |
| `429` | Figma says slow down. Wait a bit; don't swap accounts to dodge it. |
| MCP login refused | Figma's MCP allowlist said no. Nothing to fix locally. |

Still stuck? `figma-cli doctor` first, then `figma-cli <command> -h`.

---

## For the curious (and the contributors)

- Full command list: `figma-cli help`
- The agent playbook (setup, workflows, guardrails): `figma-cli --skills`
- How it's built, releases, credential storage: [AGENTS.md](AGENTS.md)
- Which Figma API endpoints and scopes it uses: [docs/rest-api-endpoints.md](docs/rest-api-endpoints.md)
- Build it yourself (Go ≥ 1.23, zero dependencies — minimalism or masochism, you decide):

  ```bash
  go build -o figma-cli .
  ```

Runs on macOS, Linux and Windows, Intel and ARM. One file, nothing else to install.
