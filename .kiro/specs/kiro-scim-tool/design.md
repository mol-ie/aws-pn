# Design Document

## Overview

`kiro-scim` is a small, portable command-line tool written in Go that manages
Kiro users and groups through Kiro's SCIM endpoint (AWS IAM Identity Center
SCIM 2.0). It supports two commands, each taking a resource word (`users` or
`groups`):

- `list users` / `list groups` — retrieve and display the users or groups
  currently provisioned in Kiro.
- `delete users` / `delete groups` — retrieve the resources, let the operator
  interactively multi-select a subset, snapshot them to disk, and delete the
  selected resources from Kiro.

Reprovisioning is out of scope: Microsoft Entra ID is the source of truth and
re-pushes deleted users and groups to Kiro on its next SCIM sync.

The tool reads configuration (SCIM endpoint URL and bearer token) from a `.env`
file located next to the binary. It compiles to a single standalone executable
for Windows, macOS, and Linux with no runtime to install.

### Design goals

- **Safe by default.** Deletion is destructive against a live identity
  directory, so `delete` is a dry run unless the operator passes an explicit
  confirmation flag, and every selected resource is snapshotted to disk before
  any delete.
- **Lean.** Standard library for configuration, HTTP, and SCIM. Exactly one
  third-party dependency, for the cross-platform interactive multi-select.
- **Portable.** Pure Go, no cgo, no OS-specific calls. Cross-compiles trivially.
- **Secret-safe.** The bearer token is never printed, logged, or written to the
  snapshot files.

## Architecture

The tool is organized as a `main` package plus a few focused internal packages.
Flow for each command:

```
            +-------------------+
            |   main (cmd)      |   parse args, pick subcommand
            +---------+---------+
                      |
            +---------v---------+
            |   config          |   load .env beside binary, validate
            |                   |   https + region allowlist
            +---------+---------+
                      |
            +---------v---------+
            |   scim.Client     |   HTTP calls: List/Delete for Users and
            |                   |   Groups (cursor paging); bearer auth;
            |                   |   error mapping
            +---------+---------+
                      |
         +------------+-------------+
         |                          |
+--------v--------+       +---------v---------+
|  list command   |       |  delete command   |
|  (users|groups) |       |  (users|groups)   |
|  render table   |       |  select -> confirm |
|                 |       |  -> snapshot ->    |
|                 |       |  delete -> summary |
+-----------------+       +-------------------+
```

### Package layout

```
kiro-scim/
  go.mod
  main.go                 // entry point: arg parsing, subcommand dispatch, exit codes
  internal/
    config/
      config.go           // .env discovery/parsing, validation (https, region allowlist)
      config_test.go
    scim/
      client.go           // SCIM HTTP client: List/Delete for Users and Groups
      types.go            // SCIM User / Group / ListResponse / error structs
      client_test.go
    ui/
      table.go            // human-readable table rendering (users and groups)
      select.go           // interactive multi-select wrapper + TTY detection
    app/
      list.go             // list command logic (users|groups)
      delete.go           // delete command logic (snapshot, confirm, delete loop)
      app_test.go
```

Rationale: keeping `config`, `scim`, and `app` as separate packages makes the
HTTP/SCIM behavior and the `.env`/validation logic unit-testable without a TTY
and without real network calls. The `ui` package isolates the one third-party
dependency and the terminal interaction, which are the hardest parts to unit
test, behind small interfaces.

## Components and Interfaces

### config package

Responsible for locating and parsing the `.env` file and producing a validated
`Config`.

```go
type Config struct {
    Endpoint string // normalized SCIM base URL, no trailing slash, https
    Region   string // "us-east-1" or "eu-central-1", derived from Endpoint host
    token    string // unexported, and additionally redacted by String/GoString
}

// Token returns the bearer token.
func (c Config) Token() string

// String and GoString redact the token. An unexported field alone is NOT
// enough: fmt prints unexported fields for %v/%+v/%#v. Implementing
// fmt.Stringer (covers %v/%+v/%s) and fmt.GoStringer (covers %#v) is what
// actually keeps the token out of formatted output.
func (c Config) String() string
func (c Config) GoString() string

// Load discovers the .env file next to the executable, merges it under the
// process environment (process env wins), validates, and returns a Config.
func Load() (Config, error)
```

Key behaviors:

- **Executable directory discovery:** use `os.Executable()` then
  `filepath.Dir(...)` (resolving symlinks with `filepath.EvalSymlinks` where
  possible). This is cross-platform. The `.env` is looked up there.
- **.env parsing (stdlib only):** read the file line by line; skip blank lines
  and lines starting with `#`; split on the first `=`; trim whitespace; strip a
  single pair of surrounding matching quotes (`"` or `'`) from the value. No
  third-party dotenv library.
- **Precedence:** if `os.Getenv(key)` is non-empty it overrides the `.env` value.
- **Required keys:** `KIRO_SCIM_ENDPOINT`, `KIRO_SCIM_TOKEN`. Missing keys →
  descriptive error naming the key(s) and the expected `.env` path.
- **Validation:**
  - endpoint must parse as a URL and have scheme `https` (else refuse).
  - host must match `scim.<region>.amazonaws.com`; `<region>` is extracted and
    checked against the allowlist `{us-east-1, eu-central-1}`. Anything else →
    error naming the allowed regions.
  - endpoint is normalized (trailing slash trimmed) for consistent path joins.

### scim package

A thin SCIM 2.0 client for IAM Identity Center. Only the operations the tool
needs are implemented.

```go
type Client struct {
    baseURL    string
    token      string
    httpClient *http.Client // injectable for tests; default has a timeout
}

func NewClient(baseURL, token string, hc *http.Client) *Client

// ListUsers retrieves every user, following cursor-based pagination.
func (c *Client) ListUsers(ctx context.Context) ([]User, error)

// ListGroups retrieves every group, following cursor-based pagination.
func (c *Client) ListGroups(ctx context.Context) ([]Group, error)

// DeleteUser deletes a single user by SCIM id.
func (c *Client) DeleteUser(ctx context.Context, id string) error

// DeleteGroup deletes a single group by SCIM id.
func (c *Client) DeleteGroup(ctx context.Context, id string) error
```

`ListUsers`/`ListGroups` and `DeleteUser`/`DeleteGroup` are thin wrappers over
two shared, unexported helpers so the cursor-pagination loop, auth, and error
mapping are written once and differ only by the SCIM resource path
(`"Users"` vs `"Groups"`):

```go
// listRaw walks cursor pagination for the given resource ("Users"/"Groups")
// and returns every resource's raw JSON.
func (c *Client) listRaw(ctx context.Context, resource string) ([]json.RawMessage, error)

// deleteByID issues DELETE {base}/{resource}/{id}.
func (c *Client) deleteByID(ctx context.Context, resource, id string) error
```

SCIM types (`types.go`), modeled on the IAM Identity Center List responses.
Only fields the tool displays or snapshots are given named fields; the full raw
record is preserved for the snapshot via `json.RawMessage`.

```go
type User struct {
    ID          string      `json:"id"`
    UserName    string      `json:"userName"`
    DisplayName string      `json:"displayName,omitempty"`
    Active      bool        `json:"active"`
    Emails      []Email     `json:"emails,omitempty"`
    Raw         json.RawMessage `json:"-"` // full original JSON, used for snapshot
}

type Group struct {
    ID          string          `json:"id"`
    DisplayName string          `json:"displayName,omitempty"`
    ExternalID  string          `json:"externalId,omitempty"`
    Raw         json.RawMessage `json:"-"` // full original JSON, used for snapshot
}

// Note: ListGroups returns an empty "members" array for every group (an IAM
// Identity Center quirk), so Group intentionally does not model members — a
// list call cannot report membership.

type Email struct {
    Value   string `json:"value"`
    Type    string `json:"type,omitempty"`
    Primary bool   `json:"primary,omitempty"`
}

type listResponse struct {
    Resources  []json.RawMessage `json:"Resources"`
    NextCursor string            `json:"nextCursor,omitempty"`
}
```

Key behaviors:

- **Auth:** every request sets `Authorization: Bearer <token>` and
  `Accept: application/scim+json`. A non-sensitive `User-Agent` is set.
- **Cursor pagination (critical, IAM Identity Center specific):** identical for
  both `/Users` and `/Groups`.
  - First request: `GET {base}/{resource}?cursor` (empty cursor value), where
    `{resource}` is `Users` or `Groups`.
  - Decode `Resources` (each element kept as `json.RawMessage` so the full
    record is available for the snapshot, and also unmarshaled into `User` or
    `Group` for display fields).
  - If `nextCursor` is present and non-empty, request
    `GET {base}/{resource}?cursor=<nextCursor>` and repeat; otherwise stop.
  - `startIndex` is deliberately NOT used — it is unsupported by the service.
  - A page-count safety cap (e.g. 10,000 pages) guards against a server that
    never stops returning a cursor.
- **URL building:** use `net/url` with `url.Values` for query parameters so the
  cursor is correctly encoded. Paths always use forward slashes.
- **Error mapping:** non-2xx responses are read and mapped to a Go error that
  includes the HTTP status and, when the body is a SCIM/AWS error object, its
  `detail`/message — but never the request's Authorization header. Recognize the
  documented statuses (400/401/403/429/500) and give actionable messages (e.g.
  401 → "token is invalid or expired"). Delete treats `204` and `200` as success.
- **Timeouts/context:** the default `http.Client` has a sensible timeout; all
  calls accept a `context.Context`.

### ui package

- `table.go`:
  - `WriteUsersTable(w io.Writer, []User)` renders columns `#`, `USERNAME`,
    `DISPLAY NAME`, `EMAIL`, `ACTIVE`.
  - `WriteGroupsTable(w io.Writer, []Group)` renders columns `#`,
    `DISPLAY NAME`, `ID`.
  - Both take an `io.Writer` (so callers write straight to stdout and tests use
    a buffer), use `text/tabwriter` from the stdlib, show a dash for missing
    optional fields, and use a 1-based `#`. No dependency.
- `select.go`: wraps the interactive multi-select behind a small interface so
  the command logic can be tested with a fake.

```go
type Selector interface {
    // Select presents the choices and returns the indexes the operator chose.
    Select(prompt string, options []string) ([]int, error)
}
```

The real implementation uses the chosen third-party library (see Dependencies).
Before presenting a prompt it checks that both stdin and stdout are terminals
(TTY detection). If not a TTY, it returns a sentinel error (`ErrNoTTY`) that the
delete command turns into the "requires an interactive terminal" message.
Cancellation (Ctrl+C / Esc) is returned as `ErrAborted`.

### app package

Both commands take a resource type. To avoid duplicating the list/select/
snapshot/delete flow for users and groups, each resource type is reduced to a
small set of values the generic flow needs: a type label, display labels for the
picker, raw records for the snapshot, and ids for deletion.

```go
type resourceKind int

const (
    kindUsers resourceKind = iota
    kindGroups
)

// item is the generic view of one user or group used by the delete flow.
type item struct {
    id    string          // SCIM id, used for DELETE
    label string          // human label shown in the picker and summary
    raw   json.RawMessage // full original record, used for the snapshot
}
```

- `list.go`: `List(kind)` → load config → build client → `ListUsers` or
  `ListGroups` → if empty, print "no users found" / "no groups found" and
  return; else render `UsersTable` or `GroupsTable`.
- `delete.go`: `Delete(kind, confirm, yes)` → load config → build client →
  list the resources of `kind`; if empty, report and stop. Map each resource to
  an `item` (user label `userName <email>`; group label `displayName (id)`),
  call `Selector.Select`. Handle no-selection, abort, and no-TTY cases. Show the
  chosen entries and the dry-run / confirm decision:
  - If not confirmed (no `--confirm` flag): print "DRY RUN — would delete N
    users/groups" with the list and return success without deleting.
  - If confirmed: write the snapshot file first (abort on write failure), then
    loop deleting each entry by id via the kind-appropriate delete call,
    collecting per-entry results, printing a final summary, and appending the two
    reminders (seats not released; Entra will reprovision). Return non-zero if any
    delete failed.

### main package

- Minimal hand-rolled two-level dispatch using the stdlib `flag` package: first
  word is the command (`list` / `delete`), second word is the resource
  (`users` / `groups`), then flags. One `flag.FlagSet` per command. No CLI
  framework needed.
- Grammar:
  - `kiro-scim list users` / `kiro-scim list groups`
  - `kiro-scim delete users [--confirm] [--yes]` /
    `kiro-scim delete groups [--confirm] [--yes]`
- `kiro-scim` with no args, or `-h`/`--help`, prints top-level usage. A command
  with a missing/invalid resource word prints that command's usage and exits
  non-zero. Each command supports `--help`.
- Flags:
  - `delete ... --confirm` (bool, default false): perform real deletions.
  - `delete ... --yes` (bool, default false, optional): skip the final
    interactive "type to confirm" guard for use together with `--confirm` in
    supervised automation. Without it, confirmed deletes still ask once more.
    (Even with `--yes`, a TTY is still required for the multi-select itself.)
- Translates returned errors into messages on stderr and process exit codes.

## Data Models

### .env file (next to binary)

```
# Kiro SCIM configuration
KIRO_SCIM_ENDPOINT=https://scim.us-east-1.amazonaws.com/<tenant-id>/scim/v2
KIRO_SCIM_TOKEN=<bearer-token>
```

### Snapshot file

Written to the current working directory before any deletion. The file name
includes the resource type:

```
kiro-scim-deleted-users-20260115-142530.json
kiro-scim-deleted-groups-20260115-142530.json
```

Contents: a JSON array of the full original SCIM records (`item.raw`) for every
resource selected for deletion, plus a small header object with timestamp,
resource type (users/groups), endpoint (token-free), region, and
operator-selected count. The token is never included.

## Error Handling

| Situation | Behavior | Exit |
|---|---|---|
| `.env` missing and env vars unset | Error naming missing keys + expected path | non-zero |
| Endpoint not https | Refuse, explain | non-zero |
| Endpoint region not allowed / unparseable host | Refuse, name allowed regions | non-zero |
| SCIM list fails (network / non-2xx / bad JSON) | Report status + diagnostic (no token) | non-zero |
| `delete` with no TTY | Explain interactive terminal required | non-zero |
| Operator selects nothing / aborts | Report, do nothing | zero (nothing-selected), zero (abort) |
| Snapshot write fails | Abort before any delete | non-zero |
| Some deletes fail | Continue others, summarize, | non-zero |
| Dry run completes | Show would-delete list | zero |
| Confirmed delete, all succeed | Summary + reminders | zero |

All error output goes to stderr. The Authorization header / token value never
appears in any message, snapshot, or log.

## Testing Strategy

- **config:** table-driven unit tests for `.env` parsing (comments, quotes,
  whitespace, precedence), https rejection, region extraction and allowlist
  (valid us-east-1 / eu-central-1; rejected other regions; malformed host).
- **scim:** tests using `httptest.Server` to simulate, for both `/Users` and
  `/Groups`:
  - single-page and multi-page cursor pagination (assert the first call sends an
    empty `cursor`, subsequent calls send `nextCursor`, `startIndex` is never
    sent, and the correct resource path is requested);
  - error status mapping (400/401/403/404/429/500) with token-free messages;
  - delete success (204/200) and failure.
  - a test asserting the token never appears in any error string.
- **app/delete:** inject a fake `Selector` and a stub SCIM client and run the
  generic flow for both kinds to exercise: no-selection, abort, dry-run (no
  delete calls made), confirmed delete with snapshot written (name reflects the
  kind), and partial-failure summary + exit behavior. Snapshot content asserted
  to exclude the token.
- **ui/table:** golden-style assertions on `UsersTable` (users with and without
  display name/email) and `GroupsTable`.

Interactive selection against a real terminal is validated manually (documented
in the build/run notes), since TTY behavior is not unit-testable.

## Dependencies

- **Standard library** for everything except the interactive picker.
- **One third-party dependency** for the cross-platform multi-select prompt:
  `github.com/AlecAivazis/survey/v2` (lightweight, long-established, provides the
  checkbox multi-select and TTY handling across Windows/macOS/Linux). It is
  isolated behind the `ui.Selector` interface, so it can be swapped without
  touching command logic. It is pure Go and compiles into the single binary (no
  cgo), preserving trivial cross-compilation. Its version is pinned in
  `go.mod`/`go.sum`.

## Building and Distribution

The tool builds to a single self-contained native executable per platform. Go
must be installed to **build**; nothing is required to **run** the resulting
binary beyond the binary itself and its adjacent `.env` file.

Build from any one machine (pure Go → cross-compilation needs no C toolchain):

```bash
# From the module root (kiro-scim/)

# Windows (x86-64)
GOOS=windows GOARCH=amd64 go build -o dist/kiro-scim.exe .

# macOS (Apple Silicon)
GOOS=darwin  GOARCH=arm64 go build -o dist/kiro-scim-darwin-arm64 .

# macOS (Intel)
GOOS=darwin  GOARCH=amd64 go build -o dist/kiro-scim-darwin-amd64 .

# Linux (x86-64)
GOOS=linux   GOARCH=amd64 go build -o dist/kiro-scim-linux-amd64 .
```

On Windows PowerShell, set the variables with `$env:GOOS` / `$env:GOARCH`
instead of the inline `VAR=value` form.

Distribution: ship the platform binary together with a `.env` file (or a
`.env.example` for the operator to fill in) in the same directory. The operator
runs, for example, `./kiro-scim list users` (or `list groups`), then
`./kiro-scim delete users` (dry run) and `./kiro-scim delete users --confirm` to
actually delete; likewise `delete groups`.

Reproducibility note: add `-trimpath` and `-ldflags "-s -w"` for smaller,
path-independent release binaries if desired.
