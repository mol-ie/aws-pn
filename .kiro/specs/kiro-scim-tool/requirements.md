# Requirements Document

## Introduction

This feature is a portable, command-line tool written in Go that manages Kiro
users through Kiro's SCIM endpoint. Kiro's SCIM endpoint is a standard SCIM 2.0
interface fronting AWS IAM Identity Center, authenticated with a bearer token.

The tool supports a "clean re-sync" operations workflow: an administrator lists
the users or groups currently provisioned in Kiro, interactively selects a
subset, and deletes those resources from Kiro. Reprovisioning is intentionally
**out of scope** for this tool — Microsoft Entra ID is the source of truth and
re-pushes the deleted users and groups to Kiro via its own SCIM provisioning
after they are removed.

Both resource types are addressed with the same two commands via an explicit
resource word: `list users` / `list groups` and `delete users` / `delete
groups`.

The tool is a single Go codebase that cross-compiles to Windows, macOS, and
Linux. It reads its configuration (SCIM endpoint URL and bearer token) from a
`.env` file located next to the binary.

### Key facts and constraints (from Kiro documentation)

- Kiro's SCIM endpoint is AWS IAM Identity Center SCIM 2.0. It is accessed over
  HTTPS with an `Authorization: Bearer <token>` header. The base URL has the
  shape `https://scim.<region>.amazonaws.com/<tenant_id>/scim/v2/`.
- The service exposes both a `/Users` resource and a `/Groups` resource. Both
  behave the same way for listing and deletion.
- IAM Identity Center's `ListUsers` and `ListGroups` use **cursor-based**
  pagination, not `startIndex`. The first page is requested with an empty
  `cursor` query parameter (`.../Users?cursor` or `.../Groups?cursor`), and
  subsequent pages use the `nextCursor` value from the previous response.
  `startIndex` is not supported. A maximum of 100 results is returned per page.
- On `ListGroups`, the `members` array is always returned empty (an IAM Identity
  Center quirk); member counts cannot be shown from a plain list call.
- Errors are returned as AWS-style exceptions with standard HTTP status codes
  (for example 400 ValidationException, 401 UnauthorizedException,
  403 AccessDeniedException, 429 ThrottlingException, 500 InternalServerException).
- The SCIM token is a long-lived, password-equivalent credential and must be
  handled with care. It must never be logged or printed.
- Deleting a user via SCIM does **not** release that user's Kiro subscription
  (seat). Subscription removal is a separate, manual action in the Kiro admin
  console. The tool must make this clear to the operator. Deleting a group
  likewise does not release the seats of its members.
- Deletion is a destructive operation against the live identity directory, so
  the tool defaults to a safe mode and requires explicit confirmation before
  deleting.

## Requirements

### Requirement 1: Configuration from a .env file beside the binary

**User Story:** As an administrator, I want the tool to read the SCIM endpoint
and token from a `.env` file located next to the binary, so that I do not have
to pass secrets on the command line or remember long arguments.

#### Acceptance Criteria

1. WHEN the tool starts THEN the tool SHALL look for a `.env` file in the same
   directory as the running executable.
2. The `.env` file SHALL support at least two keys: `KIRO_SCIM_ENDPOINT` (the
   SCIM 2.0 base URL) and `KIRO_SCIM_TOKEN` (the bearer token).
3. WHEN an environment variable of the same name is already set in the process
   environment THEN the process environment value SHALL take precedence over the
   `.env` file value.
4. WHEN the `.env` file is missing AND the required values are not present in the
   process environment THEN the tool SHALL exit with a non-zero status and a
   clear message naming the missing key(s) and the expected `.env` location.
5. The `.env` parser SHALL ignore blank lines and lines beginning with `#`, and
   SHALL trim surrounding whitespace and optional surrounding quotes from values.
6. The tool SHALL NOT print, log, or echo the token value at any verbosity level.
7. IF the configured endpoint is not an `https://` URL THEN the tool SHALL refuse
   to run and SHALL exit with a clear error, to avoid sending the bearer token
   over an unencrypted connection.
8. Kiro's SCIM is available only in the AWS regions `us-east-1` and
   `eu-central-1`. The tool SHALL determine the region from the host portion of
   the configured endpoint (`scim.<region>.amazonaws.com`) and SHALL accept only
   those two regions.
9. IF the configured endpoint's region is not one of the two allowed regions, OR
   the host does not match the expected `scim.<region>.amazonaws.com` shape from
   which a region can be determined THEN the tool SHALL refuse to run and SHALL
   exit with a clear error that names the allowed regions.

### Requirement 2: List Kiro users and groups

**User Story:** As an administrator, I want to list the users or groups
currently provisioned in Kiro, so that I can see what exists before deciding
what to delete.

#### Acceptance Criteria

1. WHEN the operator runs `list users` THEN the tool SHALL retrieve users from
   the SCIM `GET /Users` resource; WHEN the operator runs `list groups` THEN the
   tool SHALL retrieve groups from the SCIM `GET /Groups` resource.
2. IF the operator runs `list` without a valid resource word (`users` or
   `groups`) THEN the tool SHALL print usage for the `list` command and exit with
   a non-zero status.
3. WHEN the directory contains more resources than fit in a single SCIM response
   THEN the tool SHALL follow IAM Identity Center cursor-based pagination: it
   SHALL make the first request with an empty `cursor` query parameter and SHALL
   continue requesting pages using the `nextCursor` value from each response
   until no `nextCursor` is returned.
4. WHEN users are retrieved THEN the tool SHALL present them in a human-readable
   table showing at least: a row index, user name, display name (if present),
   primary email (if present), and active status.
5. WHEN groups are retrieved THEN the tool SHALL present them in a human-readable
   table showing at least: a row index, display name, and the group id.
6. WHEN the selected resource type has zero entries THEN the tool SHALL print a
   clear "no users found" / "no groups found" message and exit with status zero.
7. IF a SCIM request fails (non-2xx response, network error, or malformed body)
   THEN the tool SHALL report the failure with the HTTP status and a short
   diagnostic, and SHALL exit with a non-zero status.
8. The tool SHALL NOT include the bearer token in any output or error message.

### Requirement 3: Interactively select users or groups to delete

**User Story:** As an administrator, I want to interactively pick which users or
groups to delete from a list, so that I act on exactly the resources I intend to
and avoid mistyping identifiers.

#### Acceptance Criteria

1. WHEN the operator runs `delete users` or `delete groups` THEN the tool SHALL
   first retrieve the current resources of that type (same retrieval behavior as
   the corresponding `list` command).
2. IF the operator runs `delete` without a valid resource word (`users` or
   `groups`) THEN the tool SHALL print usage for the `delete` command and exit
   with a non-zero status.
3. WHEN resources are retrieved THEN the tool SHALL present an interactive
   multi-select list (checkbox style) allowing the operator to toggle one or more
   entries and confirm the selection.
4. WHEN the operator confirms with nothing selected THEN the tool SHALL exit
   without deleting anything and SHALL report that nothing was selected.
5. WHEN the operator cancels or aborts the interactive prompt (for example via
   Ctrl+C or Esc) THEN the tool SHALL exit without deleting anything.
6. IF the session is not an interactive terminal (no TTY) THEN the tool SHALL NOT
   attempt interactive selection; it SHALL exit with a clear message explaining
   that `delete` requires an interactive terminal.
7. Each selectable entry SHALL show enough identifying detail that the operator
   can distinguish entries: for users, the user name and email where available;
   for groups, the display name and group id.

### Requirement 4: Safe deletion with confirmation and snapshot

**User Story:** As an administrator, I want deletions to be deliberate and
recoverable as an audit record, so that I do not accidentally remove users or
groups and I retain a record of exactly what was removed.

#### Acceptance Criteria

1. WHEN entries have been selected THEN the tool SHALL display the full list of
   resources about to be deleted and SHALL require an explicit confirmation step
   before performing any deletion.
2. The tool SHALL default to a non-destructive mode: WHEN run without an explicit
   confirmation flag THEN the tool SHALL show what it would delete (dry run) and
   SHALL NOT call the SCIM `DELETE` operation.
3. WHEN deletion is confirmed THEN BEFORE issuing any `DELETE` call the tool SHALL
   write a snapshot of every selected resource's full SCIM record to a timestamped
   JSON file in the working directory, and SHALL report the snapshot file path.
   The snapshot file name SHALL indicate the resource type (users or groups).
4. WHEN the snapshot file cannot be written THEN the tool SHALL abort before
   deleting any resource.
5. WHEN deletion proceeds THEN for each selected resource the tool SHALL call SCIM
   `DELETE /Users/{id}` (for users) or `DELETE /Groups/{id}` (for groups) and
   SHALL report per-entry success or failure.
6. IF an individual delete fails THEN the tool SHALL continue attempting the
   remaining entries and SHALL report a summary of successes and failures at the
   end, exiting non-zero if any deletion failed.
7. WHEN deletion completes THEN the tool SHALL remind the operator that (a) the
   corresponding Kiro subscription/seats are NOT released by SCIM deletion and
   must be removed manually in the Kiro console, and (b) Entra ID will reprovision
   the deleted users and groups on its next sync.

### Requirement 5: Portability and lean footprint

**User Story:** As an administrator, I want a single tool that runs on Windows,
macOS, and Linux, so that I can use it regardless of my workstation.

#### Acceptance Criteria

1. The tool SHALL be a single Go codebase that compiles to a standalone
   executable for Windows, macOS, and Linux without source changes.
2. The tool SHALL avoid OS-specific behavior; file paths SHALL be constructed
   with the Go standard library's path handling, and SCIM URL paths SHALL always
   use forward slashes.
3. The tool SHALL rely on the Go standard library for configuration parsing,
   HTTP, and SCIM handling, and SHALL limit third-party dependencies to what is
   required for cross-platform interactive terminal selection.
4. The tool SHALL locate the executable's own directory in a cross-platform way
   in order to find the adjacent `.env` file.

### Requirement 6: Diagnostics and exit codes

**User Story:** As an administrator, I want clear output and meaningful exit
codes, so that I can tell what happened and so the tool behaves well in scripts.

#### Acceptance Criteria

1. The tool SHALL exit with status zero only when the requested operation fully
   succeeded (including a dry run that completed, and a confirmed delete in which
   every selected resource was deleted).
2. The tool SHALL exit with a non-zero status on configuration errors, SCIM
   request failures, or any failed deletion.
3. Error messages SHALL be written to standard error and SHALL be understandable
   to an operator who is not familiar with the tool's internals.
4. The tool SHALL provide `--help` output describing each command and its flags.
