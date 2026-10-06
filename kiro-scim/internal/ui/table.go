package ui

import (
	"fmt"
	"io"
	"text/tabwriter"

	"kiro-scim/internal/scim"
)

// dash is shown in place of a missing optional field so columns stay aligned
// and empty cells are visually obvious.
const dash = "-"

// WriteUsersTable renders users as an aligned text table to w, with columns
// #, USERNAME, DISPLAY NAME, EMAIL, and ACTIVE. The # column is 1-based.
func WriteUsersTable(w io.Writer, users []scim.User) error {
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintln(tw, "#\tUSERNAME\tDISPLAY NAME\tEMAIL\tACTIVE"); err != nil {
		return err
	}
	for i, u := range users {
		active := "no"
		if u.Active {
			active = "yes"
		}
		if _, err := fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\n",
			i+1,
			orDash(u.UserName),
			orDash(u.DisplayName),
			orDash(u.PrimaryEmail()),
			active,
		); err != nil {
			return err
		}
	}
	return tw.Flush()
}

// WriteGroupsTable renders groups as an aligned text table to w, with columns
// #, DISPLAY NAME, and ID. The # column is 1-based.
func WriteGroupsTable(w io.Writer, groups []scim.Group) error {
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintln(tw, "#\tDISPLAY NAME\tID"); err != nil {
		return err
	}
	for i, g := range groups {
		if _, err := fmt.Fprintf(tw, "%d\t%s\t%s\n",
			i+1,
			orDash(g.DisplayName),
			orDash(g.ID),
		); err != nil {
			return err
		}
	}
	return tw.Flush()
}

// orDash returns s, or a dash placeholder when s is empty.
func orDash(s string) string {
	if s == "" {
		return dash
	}
	return s
}
