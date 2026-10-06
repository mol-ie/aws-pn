package app

import (
	"context"
	"fmt"
	"io"

	"kiro-scim/internal/config"
	"kiro-scim/internal/scim"
	"kiro-scim/internal/ui"
)

// List retrieves and prints the users or groups provisioned in Kiro. It loads
// configuration, builds the real SCIM client, and writes the table to out.
func List(ctx context.Context, kind Kind, out io.Writer) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	client := scim.NewClient(cfg.Endpoint, cfg.Token(), nil)
	return list(ctx, client, kind, out)
}

// list is the testable core: it lists resources of the given kind using the
// provided client and renders them (or a "none found" message) to out.
func list(ctx context.Context, client scimClient, kind Kind, out io.Writer) error {
	switch kind {
	case Groups:
		groups, err := client.ListGroups(ctx)
		if err != nil {
			return err
		}
		if len(groups) == 0 {
			_, err := fmt.Fprintln(out, "no groups found")
			return err
		}
		return ui.WriteGroupsTable(out, groups)
	default:
		users, err := client.ListUsers(ctx)
		if err != nil {
			return err
		}
		if len(users) == 0 {
			_, err := fmt.Fprintln(out, "no users found")
			return err
		}
		return ui.WriteUsersTable(out, users)
	}
}
