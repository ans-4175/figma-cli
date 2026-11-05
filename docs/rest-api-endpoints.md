# REST endpoint and scope audit

Retrieved at: **2026-11-05T00:06:30.014Z** (official docs; live folder read also verified).
Versions apply **per endpoint**, not to the entire API. The client origin is
`https://api.figma.com`; command paths specify `/v1` or `/v2` explicitly.

| Command / operation | Current GET endpoint | Required granular scope |
| --- | --- | --- |
| `understand`, `file` | `/v1/files/:key` | `file_content:read` |
| `understand` with node, `node` | `/v1/files/:key/nodes` | `file_content:read` |
| `components` | `/v1/files/:key/components` | `library_content:read` |
| `components` | `/v1/files/:key/component_sets` | `library_content:read` |
| `components` | `/v1/files/:key/styles` | `library_content:read` |
| `comments` | `/v1/files/:key/comments` | `file_comments:read` |
| `versions` | `/v1/files/:key/versions` | `file_versions:read` |
| `dev-resources` | `/v1/files/:key/dev_resources` | `file_dev_resources:read` |
| `doctor` REST check | `/v1/me` | `current_user:read` |
| `folder files` | `/v2/folders/:folder_id/files` | `folders:read` |
| `folder folders` | `/v2/folders/:folder_id/folders` | `folders:read` |
| `folder meta` | `/v2/folders/:folder_id/meta` | `folder_metadata:read` |
| `team folders` | `/v2/teams/:team_id/folders` | `folders:read` |

The existing v1 command endpoints remain documented and are **not deprecated**.
Folder discovery was missing from the CLI; it now uses current v2 endpoints.
No deprecated projects endpoint or automatic legacy fallback is implemented.

## Semantics and safety

- Folder lists return **direct children visible to the token owner**, not an
  exhaustive recursive crawl. Traverse subfolders explicitly within the approved
  scope. File lists optionally accept `--branch-data`.
- A folder/project URL identifies a folder ID, not a design file key.
  Legacy `project` URL paths are accepted as input but use the v2 folder API.
- `/v1/projects/:id/files` is deprecated. Its `projects:read` requirement must
  not prompt agents to request broader or deprecated access when `folders:read`
  already authorizes the modern endpoint.
- `files:read` is deprecated and broadly permissive; prefer granular scopes.
- `folder meta` needs its own `folder_metadata:read` scope. A successful
  listing does not establish access to that separate operation.
- Published library endpoints list **published** assets. Local/unpublished
  components and remote instances require node-tree inspection.
- 403 retains a bounded, token-redacted server explanation. It does not imply
  one cause or authorize switching accounts/endpoints to bypass access control.
- GET retries remain bounded; 401/403/404 do not retry. Redirects are refused
  rather than forwarding a PAT to an unexpected destination.
- MCP transport/OAuth discovery, canvas writes and publication are separate
  from REST API versioning. This patch does not add REST mutation commands.

## Evidence and official references

These pages were retrieved on 2026-11-05; see the endpoint table above for the
operations actually implemented, rather than assuming every documented API is
a CLI command.

- https://developers.figma.com/docs/rest-api/folders-endpoints/
- https://developers.figma.com/docs/rest-api/projects-endpoints/
- https://developers.figma.com/docs/rest-api/file-endpoints/
- https://developers.figma.com/docs/rest-api/component-endpoints/
- https://developers.figma.com/docs/rest-api/comments-endpoints/
- https://developers.figma.com/docs/rest-api/version-history-endpoints/
- https://developers.figma.com/docs/rest-api/dev-resources-endpoints/
- https://developers.figma.com/docs/rest-api/users-endpoints/
- https://developers.figma.com/docs/rest-api/scopes/

Live verification: the user-selected folder returned HTTP 200 via v2 using the
existing `folders:read` PAT, while the deprecated v1 project route rejected that
scope. No extra scope or alternate identity was needed. Credentials and signed
thumbnail URLs are not included in this source-controlled evidence.
