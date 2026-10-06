# kiro-scim

A small, portable command-line tool for managing Kiro **users** and **groups**
through Kiro's SCIM endpoint (AWS IAM Identity Center SCIM 2.0).

It supports a "clean re-sync" operations workflow: list the users or groups
currently provisioned in Kiro, interactively select a subset, and delete them.
**Reprovisioning is out of scope** — Microsoft Entra ID is the source of truth
and re-pushes the deleted users and groups to Kiro on its next SCIM sync.

## Requirements to run

Nothing. The tool is a single self-contained native executable. You only need
the binary and a `.env` file next to it. (Go is required only to *build* it, not
to run it.)

## Configuration

The tool reads its configuration from a `.env` file located **in the same
directory as the binary**. Copy `.env.example` to `.env` and fill it in:

```
KIRO_SCIM_ENDPOINT=https://scim.us-east-1.amazonaws.com/<tenant-id>/scim/v2
KIRO_SCIM_TOKEN=<bearer-token>
```

- `KIRO_SCIM_ENDPOINT` — the SCIM base URL from the Kiro admin console
  (Settings → Identity Management → SCIM Endpoint). It must be an `https` URL in
  one of Kiro's supported regions: **`us-east-1`** or **`eu-central-1`**. The
  tool refuses any other region or a non-HTTPS URL.
- `KIRO_SCIM_TOKEN` — the SCIM bearer token (Settings → Identity Management →
  Access Tokens). This is a long-lived, password-equivalent credential. It is
  never printed, logged, or written to snapshot files. Keep `.env` out of
  version control (the included `.gitignore` already excludes it).

An environment variable of the same name overrides the value in `.env`.

## Usage

```
kiro-scim list   users|groups
kiro-scim delete users|groups [--confirm] [--yes]
```

### List

```
kiro-scim list users
kiro-scim list groups
```

Prints a human-readable table of the users or groups currently in Kiro.

### Delete

Delete is **interactive** and **safe by default**:

```
# Dry run (default): pick resources and see what WOULD be deleted. Deletes nothing.
kiro-scim delete users

# Actually delete the selected resources.
kiro-scim delete users --confirm

# Delete without the extra "are you sure?" prompt (for supervised automation).
kiro-scim delete groups --confirm --yes
```

Behavior:

1. Lists the resources and presents a checkbox multi-select (space to toggle,
   enter to confirm). Requires an interactive terminal.
2. Shows the chosen resources.
3. Without `--confirm`, prints a dry-run summary and stops.
4. With `--confirm`, asks for a final confirmation (unless `--yes`), then writes
   a timestamped JSON **snapshot** of every selected resource's full SCIM record
   before deleting anything:
   - `kiro-scim-deleted-users-YYYYMMDD-HHMMSS.json`
   - `kiro-scim-deleted-groups-YYYYMMDD-HHMMSS.json`
   The snapshot is an audit record; it never contains the token. If it cannot be
   written, nothing is deleted.
5. Deletes each selected resource, continuing past individual failures, and
   prints a success/failure summary.

### Important: deletion does not free seats or stop reprovisioning

- SCIM deletion does **not** release Kiro subscription **seats**. Remove seats
  manually in the Kiro admin console.
- **Entra ID will reprovision** the deleted users and groups on its next sync,
  because it is the source of truth. This tool exists to force that clean
  re-sync, not to permanently remove identities.

### Exit codes

- `0` — the operation fully succeeded (including a completed dry run, an empty
  selection, or an aborted prompt).
- `1` — a configuration error, a SCIM request failure, or one or more failed
  deletions.
- `2` — a usage error (unknown command, missing/invalid resource word).

## Building

Go is required to build. Everything is pure Go (no cgo), so cross-compilation
needs no C toolchain — you can build all platforms from one machine:

```bash
# From the module root (this directory).

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

For smaller, path-independent release binaries, add `-trimpath -ldflags "-s -w"`.

## Development

```bash
go build ./...
go vet ./...
go test ./...
```

## Distribution

Ship the platform binary together with a filled-in `.env` (or `.env.example`
for the operator to complete) in the same directory.

## Plain bash alternative

`scripts/kiro-scim.sh` is a dependency-light bash version of the tool for when
you'd rather not ship a binary. It mirrors the Go tool's behavior and safety
model: `.env` beside the script, https + region allowlist, cursor pagination,
dry-run by default, a pre-delete snapshot, and the token is never printed.

Differences from the Go binary:

- **macOS/Linux only** (no Windows).
- Requires `curl` and `jq` installed.
- Interactive selection is by typing the numbers to delete (e.g. `1 3 5`)
  rather than a checkbox UI.

Usage is the same shape:

```bash
# Put a .env next to the script (scripts/.env), then:
scripts/kiro-scim.sh list users
scripts/kiro-scim.sh delete users              # dry run
scripts/kiro-scim.sh delete users --confirm    # actually delete
scripts/kiro-scim.sh delete groups --confirm --yes
```

The snapshot is written to the current working directory, same as the binary.
