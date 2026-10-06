// Command kiro-scim manages Kiro users and groups through Kiro's SCIM endpoint
// (AWS IAM Identity Center SCIM 2.0).
//
// It supports two commands, each taking a resource word (users or groups):
//
//	list   users|groups           List the users or groups provisioned in Kiro.
//	delete users|groups [flags]   Interactively select and delete users/groups.
//
// Reprovisioning is intentionally out of scope: Microsoft Entra ID is the
// source of truth and re-pushes deleted users and groups to Kiro on its next
// SCIM sync.
//
// Configuration (SCIM endpoint URL and bearer token) is read from a .env file
// located next to the binary. See .env.example.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"kiro-scim/internal/app"
)

// usage describes the command-line interface. It is printed when the tool is
// run with no command or with -h/--help.
const usage = `kiro-scim manages Kiro users and groups through Kiro's SCIM endpoint.

Usage:
  kiro-scim <command> <resource> [flags]

Commands:
  list    users|groups           List the users or groups provisioned in Kiro.
  delete  users|groups [flags]   Interactively select and delete users/groups.

Delete flags:
  --confirm   Perform real deletions (without it, delete is a dry run).
  --yes       Skip the final confirmation prompt (use with --confirm).

Configuration is read from a .env file next to the binary (see .env.example):
  KIRO_SCIM_ENDPOINT  SCIM 2.0 base URL (https, region us-east-1 or eu-central-1)
  KIRO_SCIM_TOKEN     SCIM bearer token
`

func main() {
	os.Exit(run(os.Args[1:]))
}

// run dispatches to a command and returns a process exit code. It is kept
// separate from main so it can be exercised in tests.
func run(args []string) int {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, usage)
		return 2
	}

	switch args[0] {
	case "-h", "--help", "help":
		fmt.Print(usage)
		return 0
	case "list":
		return runList(args[1:])
	case "delete":
		return runDelete(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", args[0], usage)
		return 2
	}
}

const listUsage = `Usage: kiro-scim list <users|groups>

Lists the users or groups currently provisioned in Kiro.
`

func runList(args []string) int {
	fs := flag.NewFlagSet("list", flag.ContinueOnError)
	fs.Usage = func() { fmt.Fprint(os.Stderr, listUsage) }
	if err := fs.Parse(args); err != nil {
		return 2
	}

	kind, ok := resourceArg(fs, listUsage)
	if !ok {
		return 2
	}

	if err := app.List(context.Background(), kind, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	return 0
}

const deleteUsage = `Usage: kiro-scim delete <users|groups> [--confirm] [--yes]

Interactively selects users or groups and deletes them from Kiro.
Without --confirm the command performs a dry run and deletes nothing.

Flags:
  --confirm   Perform real deletions.
  --yes       Skip the final confirmation prompt (requires --confirm).
`

func runDelete(args []string) int {
	fs := flag.NewFlagSet("delete", flag.ContinueOnError)
	fs.Usage = func() { fmt.Fprint(os.Stderr, deleteUsage) }
	confirm := fs.Bool("confirm", false, "perform real deletions")
	yes := fs.Bool("yes", false, "skip the final confirmation prompt")

	// The resource word must come before flags; parse it, then the flags.
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, deleteUsage)
		return 2
	}
	resource := args[0]
	kind, ok := app.ParseKind(resource)
	if !ok {
		fmt.Fprintf(os.Stderr, "delete: expected resource \"users\" or \"groups\", got %q\n\n%s", resource, deleteUsage)
		return 2
	}
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}

	if err := app.Delete(context.Background(), kind, *confirm, *yes); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	return 0
}

// resourceArg reads the single positional resource word from a parsed flag set
// and converts it to a Kind, printing usage on error.
func resourceArg(fs *flag.FlagSet, usageText string) (app.Kind, bool) {
	rest := fs.Args()
	if len(rest) == 0 {
		fmt.Fprintf(os.Stderr, "expected resource \"users\" or \"groups\"\n\n%s", usageText)
		return 0, false
	}
	kind, ok := app.ParseKind(rest[0])
	if !ok {
		fmt.Fprintf(os.Stderr, "expected resource \"users\" or \"groups\", got %q\n\n%s", rest[0], usageText)
		return 0, false
	}
	return kind, true
}
