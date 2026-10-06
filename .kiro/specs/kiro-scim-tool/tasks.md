# Implementation Plan

- [x] 1. Initialize the Go module and project skeleton
  - Create `kiro-scim/go.mod` (module path `kiro-scim`, a current Go version).
  - Create the package directories from the design: `internal/config`,
    `internal/scim`, `internal/ui`, `internal/app`.
  - Add a `.env.example` with `KIRO_SCIM_ENDPOINT` and `KIRO_SCIM_TOKEN` keys.
  - Add a stub `main.go` that builds (prints usage) so `go build ./...` succeeds.
  - _Requirements: 5.1, 5.3_

- [x] 2. Implement SCIM data types
  - In `internal/scim/types.go`, define `User`, `Group`, `Email`, and the
    internal `listResponse` (with `Resources []json.RawMessage` and
    `NextCursor`).
  - Give `User` and `Group` a `Raw json.RawMessage` field (populated from the
    per-resource raw JSON). Add a `User` helper to return the primary email
    string. `Group` models `id`, `displayName`, `externalId` (no members).
  - _Requirements: 2.4, 2.5, 4.3_

- [x] 3. Implement configuration loading and validation
- [x] 3.1 Implement `.env` discovery and parsing
  - In `internal/config/config.go`, locate the executable directory via
    `os.Executable` + `filepath.Dir` (resolve symlinks when possible) and read
    the adjacent `.env`.
  - Parse with the stdlib: skip blank/`#` lines, split on first `=`, trim
    whitespace, strip one pair of surrounding quotes. Apply process-env override.
  - _Requirements: 1.1, 1.2, 1.3, 1.5_
- [x] 3.2 Implement validation and the `Config` type
  - Define `Config` with an unexported `token` and a `Token()` method.
  - Require both keys (error names missing key(s) + expected `.env` path).
  - Reject non-`https` endpoints; derive region from `scim.<region>.amazonaws.com`
    and accept only `us-east-1`/`eu-central-1`; normalize (trim trailing slash).
  - _Requirements: 1.4, 1.6, 1.7, 1.8, 1.9_
- [x] 3.3 Write config unit tests
  - Table-driven tests for parsing (comments, quotes, whitespace, precedence),
    missing keys, non-https rejection, region extraction/allowlist (valid both
    regions; rejected other region; malformed host).
  - Assert `Config` formatted with `%+v` does not reveal the token.
  - _Requirements: 1.2, 1.3, 1.5, 1.6, 1.7, 1.8, 1.9_

- [x] 4. Implement the SCIM client
- [x] 4.1 Implement shared cursor pagination and `ListUsers`/`ListGroups`
  - In `internal/scim/client.go`, add `NewClient` (injectable `*http.Client`
    with a default timeout) and an unexported `listRaw(ctx, resource)` helper
    that walks cursor pagination for a resource path (`"Users"`/`"Groups"`).
  - Set `Authorization: Bearer`, `Accept: application/scim+json`, a `User-Agent`.
  - First request sends an empty `cursor`; follow `nextCursor` until absent;
    never send `startIndex`. Keep each resource's raw JSON. Add a page-count
    safety cap.
  - Add `ListUsers(ctx)` and `ListGroups(ctx)` that call `listRaw` and unmarshal
    into `User`/`Group` (preserving `Raw`).
  - _Requirements: 2.1, 2.3_
- [x] 4.2 Implement `DeleteUser`/`DeleteGroup` and error mapping
  - Add an unexported `deleteByID(ctx, resource, id)` and the exported
    `DeleteUser`/`DeleteGroup` wrappers, treating 200/204 as success.
  - Map non-2xx responses (400/401/403/404/429/500) to actionable errors that
    include HTTP status but never the Authorization header/token.
  - _Requirements: 2.7, 2.8, 4.5_
- [x] 4.3 Write SCIM client tests with httptest
  - For both `/Users` and `/Groups`: single-page and multi-page pagination —
    assert first call has empty `cursor`, later calls carry `nextCursor`,
    `startIndex` is never present, and the correct resource path is requested.
  - Error-status mapping; delete success and failure for both resources.
  - Assert the token never appears in any returned error string.
  - _Requirements: 2.1, 2.3, 2.7, 2.8, 4.5_

- [x] 5. Implement the table renderers
  - In `internal/ui/table.go`, add `UsersTable([]User)` (columns `#`,
    `USERNAME`, `DISPLAY NAME`, `EMAIL`, `ACTIVE`) and `GroupsTable([]Group)`
    (columns `#`, `DISPLAY NAME`, `ID`) via `text/tabwriter`; handle missing
    fields.
  - Add tests asserting output for users with and without display name/email,
    and for groups.
  - _Requirements: 2.4, 2.5_

- [x] 6. Implement the interactive selector
  - In `internal/ui/select.go`, define the `Selector` interface and a real
    implementation using `github.com/AlecAivazis/survey/v2` (add the dependency,
    pin it in `go.mod`/`go.sum`).
  - Detect TTY on stdin and stdout; return `ErrNoTTY` when not interactive and
    `ErrAborted` on Ctrl+C/Esc.
  - _Requirements: 3.3, 3.5, 3.6, 3.7_

- [x] 7. Implement the `list` command
  - In `internal/app/list.go`: a `List(kind)` entry point that loads config,
    builds the client, calls `ListUsers` or `ListGroups`; prints "no users
    found" / "no groups found" when empty, otherwise renders the matching table.
  - Return errors (no token in messages); map to exit behavior in `main`.
  - _Requirements: 2.1, 2.4, 2.5, 2.6, 2.7, 2.8, 6.1, 6.2, 6.3_

- [x] 8. Implement the `delete` command
- [x] 8.1 Generic selection, dry-run, and confirmation flow
  - In `internal/app/delete.go`: a `Delete(kind, confirm, yes)` entry point that
    loads config, builds the client, lists resources of `kind`; stop if empty.
    Reduce each resource to a generic `item` (id, label, raw): user label
    `userName <email>`, group label `displayName (id)`. Call `Selector.Select`.
  - Handle no-selection and abort (exit zero, nothing deleted) and `ErrNoTTY`
    (clear message, non-zero). Show the chosen entries.
  - Dry run by default: without `--confirm`, print the would-delete list and
    return without deleting. With `--confirm`, require a final confirmation
    unless `--yes` is also set.
  - _Requirements: 3.1, 3.3, 3.4, 3.5, 3.6, 4.1, 4.2_
- [x] 8.2 Snapshot-before-delete and deletion loop
  - Before any delete, write a timestamped JSON snapshot whose name includes the
    kind (`kiro-scim-deleted-users-...` / `-groups-...`): header + array of each
    item's raw record, token-free; abort on write failure; report the path.
  - Delete each selected entry by id via the kind-appropriate call
    (`DeleteUser`/`DeleteGroup`), collecting per-entry success/failure; continue
    on individual failures; print a summary; append the two reminders (seats not
    released by SCIM; Entra reprovisions). Non-zero exit if any delete failed.
  - _Requirements: 4.3, 4.4, 4.5, 4.6, 4.7, 6.1, 6.2_
- [x] 8.3 Write delete-command tests
  - With a fake `Selector` and stub SCIM client, for both kinds: no-selection,
    abort, dry-run (asserts no delete calls), confirmed delete (asserts snapshot
    written with the kind-specific name and delete calls made), and
    partial-failure summary + exit behavior.
  - Assert the snapshot file excludes the token.
  - _Requirements: 3.4, 3.5, 4.2, 4.3, 4.4, 4.6_

- [x] 9. Wire up `main` with two-level dispatch and exit codes
  - In `main.go`: dispatch `list <users|groups>` and
    `delete <users|groups> [--confirm] [--yes]` via per-command `flag.FlagSet`;
    a missing/invalid resource word prints that command's usage and exits
    non-zero. Implement `--help`/usage at both levels.
  - Map returned errors to stderr messages and process exit codes per the design
    table (zero only on full success/dry-run/nothing-selected/abort).
  - _Requirements: 2.2, 3.2, 6.1, 6.2, 6.3, 6.4_

- [x] 10. Finalize build, cross-compilation, and docs
  - Run `go vet ./...` and `go build ./...`; verify cross-compiles for
    windows/amd64, darwin/arm64, darwin/amd64, linux/amd64.
  - Add a short `README.md` covering configuration (`.env` beside the binary),
    the `list users|groups` / `delete users|groups` usage, the
    dry-run/`--confirm` behavior, the manual seat removal + Entra reprovision
    reminders, and the build/cross-compile commands.
  - _Requirements: 5.1, 5.2, 5.4, 4.7_
