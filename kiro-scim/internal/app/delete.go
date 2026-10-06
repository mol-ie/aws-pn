package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"kiro-scim/internal/config"
	"kiro-scim/internal/scim"
	"kiro-scim/internal/ui"
)

// item is the generic view of one user or group used by the delete flow: an id
// to delete by, a human label for the picker and summaries, and the full raw
// record for the snapshot.
type item struct {
	id    string
	label string
	raw   json.RawMessage
}

// deleteDeps are the collaborators the delete flow needs. Bundling them makes
// the flow easy to drive from tests with stubs.
type deleteDeps struct {
	client   scimClient
	selector ui.Selector
	out      io.Writer
	// snapshotDir is the directory the snapshot file is written to (the working
	// directory in production; a temp dir in tests).
	snapshotDir string
	// now supplies the snapshot timestamp (injectable for deterministic tests).
	now func() time.Time
	// confirm is called before destructive deletion when running with --confirm
	// but without --yes. It returns true to proceed.
	confirm func(prompt string) (bool, error)
}

// Delete runs the interactive delete flow for the given kind against the live
// Kiro SCIM endpoint. confirm enables real deletion (otherwise it is a dry
// run); yes skips the final confirmation guard.
func Delete(ctx context.Context, kind Kind, confirm, yes bool) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	wd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("determining working directory: %w", err)
	}
	deps := deleteDeps{
		client:      scim.NewClient(cfg.Endpoint, cfg.Token(), nil),
		selector:    ui.SurveySelector{},
		out:         os.Stdout,
		snapshotDir: wd,
		now:         time.Now,
		confirm:     promptYesNo,
	}
	return runDelete(ctx, deps, kind, confirm, yes)
}

// runDelete is the testable core of the delete flow.
func runDelete(ctx context.Context, d deleteDeps, kind Kind, confirm, yes bool) error {
	items, err := loadItems(ctx, d.client, kind)
	if err != nil {
		return err
	}
	if len(items) == 0 {
		fmt.Fprintf(d.out, "no %s found; nothing to delete\n", kind)
		return nil
	}

	labels := make([]string, len(items))
	for i, it := range items {
		labels[i] = it.label
	}

	chosen, err := d.selector.Select(
		fmt.Sprintf("Select %s to delete", kind), labels)
	if err != nil {
		switch {
		case errors.Is(err, ui.ErrNoTTY):
			return fmt.Errorf("delete requires an interactive terminal; run it from a terminal (no TTY detected)")
		case errors.Is(err, ui.ErrAborted):
			fmt.Fprintln(d.out, "aborted; nothing was deleted")
			return nil
		default:
			return err
		}
	}
	if len(chosen) == 0 {
		fmt.Fprintln(d.out, "nothing selected; nothing was deleted")
		return nil
	}

	selected := make([]item, len(chosen))
	for i, idx := range chosen {
		selected[i] = items[idx]
	}

	fmt.Fprintf(d.out, "\nSelected %d %s for deletion:\n", len(selected), kind)
	for _, it := range selected {
		fmt.Fprintf(d.out, "  - %s\n", it.label)
	}

	// Dry run: show what would happen and stop.
	if !confirm {
		fmt.Fprintf(d.out, "\nDRY RUN: would delete %d %s. Re-run with --confirm to delete.\n",
			len(selected), kind)
		return nil
	}

	// Confirmed mode: ask once more unless --yes was given.
	if !yes {
		ok, err := d.confirm(fmt.Sprintf("Permanently delete these %d %s?", len(selected), kind))
		if err != nil {
			return err
		}
		if !ok {
			fmt.Fprintln(d.out, "cancelled; nothing was deleted")
			return nil
		}
	}

	// Snapshot before any deletion. Abort entirely if it cannot be written.
	path, err := writeSnapshot(d, kind, selected)
	if err != nil {
		return fmt.Errorf("writing pre-delete snapshot (aborting before any deletion): %w", err)
	}
	fmt.Fprintf(d.out, "\nWrote pre-delete snapshot: %s\n", path)

	// Delete each selected resource, continuing past individual failures.
	var failures int
	for _, it := range selected {
		derr := deleteItem(ctx, d.client, kind, it.id)
		if derr != nil {
			failures++
			fmt.Fprintf(d.out, "  FAILED  %s: %v\n", it.label, derr)
			continue
		}
		fmt.Fprintf(d.out, "  deleted %s\n", it.label)
	}

	succeeded := len(selected) - failures
	fmt.Fprintf(d.out, "\nDone: %d deleted, %d failed.\n", succeeded, failures)
	printReminders(d.out, kind)

	if failures > 0 {
		return fmt.Errorf("%d of %d deletions failed", failures, len(selected))
	}
	return nil
}

// loadItems lists resources of the given kind and reduces them to generic items.
func loadItems(ctx context.Context, client scimClient, kind Kind) ([]item, error) {
	switch kind {
	case Groups:
		groups, err := client.ListGroups(ctx)
		if err != nil {
			return nil, err
		}
		items := make([]item, len(groups))
		for i, g := range groups {
			items[i] = item{
				id:    g.ID,
				label: groupLabel(g),
				raw:   g.Raw,
			}
		}
		return items, nil
	default:
		users, err := client.ListUsers(ctx)
		if err != nil {
			return nil, err
		}
		items := make([]item, len(users))
		for i, u := range users {
			items[i] = item{
				id:    u.ID,
				label: userLabel(u),
				raw:   u.Raw,
			}
		}
		return items, nil
	}
}

func deleteItem(ctx context.Context, client scimClient, kind Kind, id string) error {
	if kind == Groups {
		return client.DeleteGroup(ctx, id)
	}
	return client.DeleteUser(ctx, id)
}

func userLabel(u scim.User) string {
	if email := u.PrimaryEmail(); email != "" {
		return fmt.Sprintf("%s <%s>", u.UserName, email)
	}
	return u.UserName
}

func groupLabel(g scim.Group) string {
	name := g.DisplayName
	if name == "" {
		name = "(no name)"
	}
	return fmt.Sprintf("%s (%s)", name, g.ID)
}

// snapshotFile is the on-disk structure of a pre-delete snapshot. It never
// contains the token.
type snapshotFile struct {
	Timestamp string            `json:"timestamp"`
	Resource  string            `json:"resource"`
	Count     int               `json:"count"`
	Records   []json.RawMessage `json:"records"`
}

func writeSnapshot(d deleteDeps, kind Kind, items []item) (string, error) {
	ts := d.now().UTC()
	name := fmt.Sprintf("kiro-scim-deleted-%s-%s.json", kind, ts.Format("20060102-150405"))
	path := filepath.Join(d.snapshotDir, name)

	records := make([]json.RawMessage, len(items))
	for i, it := range items {
		records[i] = it.raw
	}
	snap := snapshotFile{
		Timestamp: ts.Format(time.RFC3339),
		Resource:  kind.String(),
		Count:     len(items),
		Records:   records,
	}
	data, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return "", err
	}
	return path, nil
}

func printReminders(out io.Writer, kind Kind) {
	fmt.Fprintln(out, "\nReminders:")
	fmt.Fprintf(out, "  - SCIM deletion does NOT release Kiro subscription seats for the affected %s;\n", kind)
	fmt.Fprintln(out, "    remove seats manually in the Kiro admin console.")
	fmt.Fprintf(out, "  - Entra ID is the source of truth and will reprovision these %s on its next sync.\n", kind)
}
