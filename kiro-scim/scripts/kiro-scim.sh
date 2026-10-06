#!/usr/bin/env bash
#
# kiro-scim.sh — a plain bash version of the kiro-scim tool.
#
# Lists and deletes Kiro users or groups through Kiro's SCIM endpoint
# (AWS IAM Identity Center SCIM 2.0). Reprovisioning is out of scope: Entra ID
# is the source of truth and re-pushes deleted resources on its next sync.
#
# This mirrors the Go tool's safety model:
#   - reads KIRO_SCIM_ENDPOINT and KIRO_SCIM_TOKEN from a .env file beside the
#     script (process environment overrides the file);
#   - refuses non-https endpoints and regions other than us-east-1/eu-central-1;
#   - follows IAM Identity Center cursor pagination;
#   - delete is a DRY RUN unless --confirm is given;
#   - writes a timestamped JSON snapshot before deleting anything;
#   - never prints the token.
#
# Requirements: bash, curl, jq. (macOS/Linux only — not Windows.)
#
# Usage:
#   kiro-scim.sh list   users|groups
#   kiro-scim.sh delete users|groups [--confirm] [--yes]

set -euo pipefail

# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

PROG="$(basename "$0")"

# Directory the script lives in (so .env is read from beside it, like the Go
# tool reads .env beside the binary).
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ENV_FILE="${SCRIPT_DIR}/.env"

die() {
  echo "error: $*" >&2
  exit 1
}

usage() {
  cat >&2 <<EOF
${PROG} manages Kiro users and groups through Kiro's SCIM endpoint.

Usage:
  ${PROG} list   users|groups
  ${PROG} delete users|groups [--confirm] [--yes]

Delete is a dry run unless --confirm is given. --yes skips the final prompt.

Configuration is read from a .env file beside this script:
  KIRO_SCIM_ENDPOINT  SCIM 2.0 base URL (https, region us-east-1 or eu-central-1)
  KIRO_SCIM_TOKEN     SCIM bearer token
EOF
}

require_tools() {
  for t in curl jq; do
    command -v "$t" >/dev/null 2>&1 || die "required tool '$t' is not installed"
  done
}

# load_config populates ENDPOINT and TOKEN from the environment, falling back to
# the .env file. It validates https and the region allowlist. The token is
# never printed.
load_config() {
  local file_endpoint="" file_token=""

  if [[ -f "$ENV_FILE" ]]; then
    # Parse KEY=VALUE lines, ignoring comments/blank lines and stripping one
    # pair of surrounding quotes. We only read the two keys we care about.
    while IFS= read -r line || [[ -n "$line" ]]; do
      line="${line#"${line%%[![:space:]]*}"}" # ltrim
      [[ -z "$line" || "${line:0:1}" == "#" ]] && continue
      local key="${line%%=*}"
      local val="${line#*=}"
      key="$(printf '%s' "$key" | tr -d '[:space:]')"
      # trim surrounding whitespace from value
      val="${val#"${val%%[![:space:]]*}"}"
      val="${val%"${val##*[![:space:]]}"}"
      # strip a single pair of matching quotes
      if [[ ${#val} -ge 2 && ( ( "${val:0:1}" == '"' && "${val: -1}" == '"' ) || ( "${val:0:1}" == "'" && "${val: -1}" == "'" ) ) ]]; then
        val="${val:1:${#val}-2}"
      fi
      case "$key" in
        KIRO_SCIM_ENDPOINT) file_endpoint="$val" ;;
        KIRO_SCIM_TOKEN)    file_token="$val" ;;
      esac
    done < "$ENV_FILE"
  fi

  # Process environment overrides the file.
  ENDPOINT="${KIRO_SCIM_ENDPOINT:-$file_endpoint}"
  TOKEN="${KIRO_SCIM_TOKEN:-$file_token}"

  local missing=()
  [[ -z "$ENDPOINT" ]] && missing+=("KIRO_SCIM_ENDPOINT")
  [[ -z "$TOKEN" ]] && missing+=("KIRO_SCIM_TOKEN")
  if (( ${#missing[@]} > 0 )); then
    die "missing required configuration: ${missing[*]} (set it in ${ENV_FILE} or the environment)"
  fi

  # Trim a trailing slash for consistent path joins.
  ENDPOINT="${ENDPOINT%/}"

  # Must be https.
  [[ "$ENDPOINT" == https://* ]] || \
    die "KIRO_SCIM_ENDPOINT must be an https URL; refusing to send the token over an unencrypted connection"

  # Derive and validate region from host scim.<region>.amazonaws.com.
  local host="${ENDPOINT#https://}"; host="${host%%/*}"
  local region=""
  if [[ "$host" == scim.*.amazonaws.com ]]; then
    region="${host#scim.}"
    region="${region%.amazonaws.com}"
  fi
  case "$region" in
    us-east-1|eu-central-1) REGION="$region" ;;
    "") die "KIRO_SCIM_ENDPOINT host '${host}' is not a recognized Kiro SCIM endpoint (expected scim.<region>.amazonaws.com)" ;;
    *)  die "region '${region}' is not supported; Kiro SCIM is available only in us-east-1 and eu-central-1" ;;
  esac
}

# scim_get performs an authenticated GET and prints the body on success. On a
# non-2xx response it dies with the status (never printing the token).
# Args: full URL
scim_get() {
  local url="$1" tmp status
  tmp="$(mktemp)"
  status="$(curl -sS -o "$tmp" -w '%{http_code}' \
    -H "Authorization: Bearer ${TOKEN}" \
    -H "Accept: application/scim+json" \
    "$url")" || { rm -f "$tmp"; die "network error contacting SCIM endpoint"; }
  if [[ "$status" -lt 200 || "$status" -ge 300 ]]; then
    local detail; detail="$(jq -r '.detail // .message // empty' < "$tmp" 2>/dev/null || true)"
    rm -f "$tmp"
    die "SCIM request failed (HTTP ${status})${detail:+: $detail}"
  fi
  cat "$tmp"; rm -f "$tmp"
}

# scim_delete issues DELETE for a resource id. Prints nothing on success;
# returns non-zero on failure. Args: resource(Users|Groups) id
scim_delete() {
  local resource="$1" id="$2" status
  # URL-encode the id path segment with jq's @uri.
  local enc_id; enc_id="$(printf '%s' "$id" | jq -sRr @uri)"
  status="$(curl -sS -o /dev/null -w '%{http_code}' -X DELETE \
    -H "Authorization: Bearer ${TOKEN}" \
    -H "Accept: application/scim+json" \
    "${ENDPOINT}/${resource}/${enc_id}")" || return 1
  [[ "$status" == "200" || "$status" == "204" ]]
}

# list_raw follows cursor pagination for a resource and prints a single JSON
# array of all resource objects to stdout. Args: resource(Users|Groups)
list_raw() {
  local resource="$1" cursor="" page=0
  local acc="[]"
  while (( page < 10000 )); do
    local url body next
    # The first request sends an empty cursor; later ones send nextCursor.
    url="${ENDPOINT}/${resource}?cursor=$(printf '%s' "$cursor" | jq -sRr @uri)"
    body="$(scim_get "$url")"
    acc="$(jq -c --argjson acc "$acc" '$acc + (.Resources // [])' <<<"$body")"
    next="$(jq -r '.nextCursor // empty' <<<"$body")"
    [[ -z "$next" ]] && { printf '%s' "$acc"; return 0; }
    cursor="$next"
    page=$((page+1))
  done
  die "listing ${resource}: exceeded maximum pages (possible pagination loop)"
}

# ---------------------------------------------------------------------------
# Commands
# ---------------------------------------------------------------------------

# resource_path maps the user-facing word to the SCIM path segment.
resource_path() {
  case "$1" in
    users)  echo "Users" ;;
    groups) echo "Groups" ;;
    *) return 1 ;;
  esac
}

cmd_list() {
  local word="${1:-}"
  local resource; resource="$(resource_path "$word")" || die "expected resource 'users' or 'groups'"
  load_config
  local json; json="$(list_raw "$resource")"
  local count; count="$(jq 'length' <<<"$json")"
  if [[ "$count" -eq 0 ]]; then
    echo "no ${word} found"
    return 0
  fi
  if [[ "$word" == "users" ]]; then
    printf '%-4s %-25s %-25s %-30s %s\n' "#" "USERNAME" "DISPLAY NAME" "EMAIL" "ACTIVE"
    jq -r '
      to_entries[] |
      "\(.key+1)\t\(.value.userName // "-")\t\(.value.displayName // "-")\t\((.value.emails // [] | (map(select(.primary)) + .) | .[0].value) // "-")\t\(if .value.active then "yes" else "no" end)"
    ' <<<"$json" | while IFS=$'\t' read -r n u d e a; do
      printf '%-4s %-25s %-25s %-30s %s\n' "$n" "$u" "$d" "$e" "$a"
    done
  else
    printf '%-4s %-30s %s\n' "#" "DISPLAY NAME" "ID"
    jq -r '
      to_entries[] |
      "\(.key+1)\t\(.value.displayName // "-")\t\(.value.id)"
    ' <<<"$json" | while IFS=$'\t' read -r n d i; do
      printf '%-4s %-30s %s\n' "$n" "$d" "$i"
    done
  fi
}

cmd_delete() {
  local word="${1:-}"; shift || true
  local resource; resource="$(resource_path "$word")" || die "expected resource 'users' or 'groups'"

  local confirm=0 yes=0
  for arg in "$@"; do
    case "$arg" in
      --confirm) confirm=1 ;;
      --yes)     yes=1 ;;
      *) die "unknown flag: $arg" ;;
    esac
  done

  load_config
  local json; json="$(list_raw "$resource")"
  local count; count="$(jq 'length' <<<"$json")"
  if [[ "$count" -eq 0 ]]; then
    echo "no ${word} found; nothing to delete"
    return 0
  fi

  # Build a numbered, human-readable label for each entry.
  local labels
  if [[ "$word" == "users" ]]; then
    labels="$(jq -r '
      to_entries[] |
      "\(.key+1)) \(.value.userName // "?")\((.value.emails // [] | (map(select(.primary)) + .) | .[0].value) as $e | if $e then " <\($e)>" else "" end)"
    ' <<<"$json")"
  else
    labels="$(jq -r '
      to_entries[] |
      "\(.key+1)) \(.value.displayName // "(no name)") (\(.value.id))"
    ' <<<"$json")"
  fi

  echo "Available ${word}:"
  echo "$labels"
  echo
  echo "Enter the numbers to delete, separated by spaces (empty to cancel):"
  if [[ ! -t 0 ]]; then
    die "delete requires an interactive terminal (no TTY detected)"
  fi
  read -r -p "> " selection

  # Collect selected indexes (1-based) into an array, validating each.
  local -a picks=()
  for tok in $selection; do
    [[ "$tok" =~ ^[0-9]+$ ]] || die "invalid selection '$tok'"
    (( tok >= 1 && tok <= count )) || die "selection '$tok' out of range (1-${count})"
    picks+=("$((tok-1))")
  done
  if (( ${#picks[@]} == 0 )); then
    echo "nothing selected; nothing was deleted"
    return 0
  fi

  # Resolve selected ids and labels.
  echo
  echo "Selected ${#picks[@]} ${word} for deletion:"
  local -a ids=()
  for idx in "${picks[@]}"; do
    local id lbl
    id="$(jq -r --argjson i "$idx" '.[$i].id' <<<"$json")"
    if [[ "$word" == "users" ]]; then
      lbl="$(jq -r --argjson i "$idx" '.[$i].userName' <<<"$json")"
    else
      lbl="$(jq -r --argjson i "$idx" '.[$i].displayName // .[$i].id' <<<"$json")"
    fi
    ids+=("$id")
    echo "  - ${lbl} (${id})"
  done

  if (( confirm == 0 )); then
    echo
    echo "DRY RUN: would delete ${#ids[@]} ${word}. Re-run with --confirm to delete."
    return 0
  fi

  if (( yes == 0 )); then
    read -r -p "Permanently delete these ${#ids[@]} ${word}? [y/N]: " ans
    case "$(printf '%s' "$ans" | tr '[:upper:]' '[:lower:]')" in
      y|yes) ;;
      *) echo "cancelled; nothing was deleted"; return 0 ;;
    esac
  fi

  # Snapshot the full selected records before deleting anything.
  local ts snapshot
  ts="$(date -u +%Y%m%d-%H%M%S)"
  snapshot="${PWD}/kiro-scim-deleted-${word}-${ts}.json"
  local idx_json; idx_json="$(printf '%s\n' "${picks[@]}" | jq -s '.')"
  if ! jq -n \
      --arg ts "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
      --arg resource "$word" \
      --argjson all "$json" \
      --argjson idxs "$idx_json" \
      '{timestamp:$ts, resource:$resource, count:($idxs|length), records:[ $idxs[] as $i | $all[$i] ]}' \
      > "$snapshot"; then
    die "writing pre-delete snapshot failed (aborting before any deletion)"
  fi
  echo
  echo "Wrote pre-delete snapshot: ${snapshot}"

  # Delete each, continuing past individual failures.
  local failures=0 i
  for i in "${!ids[@]}"; do
    if scim_delete "$resource" "${ids[$i]}"; then
      echo "  deleted ${ids[$i]}"
    else
      failures=$((failures+1))
      echo "  FAILED  ${ids[$i]}"
    fi
  done

  local succeeded=$(( ${#ids[@]} - failures ))
  echo
  echo "Done: ${succeeded} deleted, ${failures} failed."
  echo
  echo "Reminders:"
  echo "  - SCIM deletion does NOT release Kiro subscription seats; remove them"
  echo "    manually in the Kiro admin console."
  echo "  - Entra ID will reprovision these ${word} on its next sync."

  (( failures == 0 )) || exit 1
}

# ---------------------------------------------------------------------------
# Entry point
# ---------------------------------------------------------------------------

main() {
  require_tools
  local cmd="${1:-}"
  case "$cmd" in
    -h|--help|help|"") usage; [[ -z "$cmd" ]] && exit 2 || exit 0 ;;
    list)   shift; cmd_list "$@" ;;
    delete) shift; cmd_delete "$@" ;;
    *) echo "unknown command '${cmd}'" >&2; echo >&2; usage; exit 2 ;;
  esac
}

main "$@"
