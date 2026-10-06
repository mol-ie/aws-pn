package ui

import (
	"bytes"
	"strings"
	"testing"

	"kiro-scim/internal/scim"
)

func TestWriteUsersTable(t *testing.T) {
	users := []scim.User{
		{
			ID:          "1",
			UserName:    "jdoe",
			DisplayName: "John Doe",
			Active:      true,
			Emails:      []scim.Email{{Value: "jdoe@example.com", Primary: true}},
		},
		{
			ID:       "2",
			UserName: "nobody",
			// no display name, no email, inactive
		},
	}

	var buf bytes.Buffer
	if err := WriteUsersTable(&buf, users); err != nil {
		t.Fatalf("WriteUsersTable: %v", err)
	}
	out := buf.String()

	for _, want := range []string{
		"USERNAME", "DISPLAY NAME", "EMAIL", "ACTIVE",
		"jdoe", "John Doe", "jdoe@example.com", "yes",
		"nobody", "no",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q\n%s", want, out)
		}
	}

	lines := nonEmptyLines(out)
	if len(lines) != 3 { // header + 2 users
		t.Fatalf("got %d lines, want 3:\n%s", len(lines), out)
	}
	// The row for the user with no display name/email must show the dash
	// placeholder and be 1-based index 2.
	if !strings.HasPrefix(strings.TrimSpace(lines[2]), "2") {
		t.Errorf("second data row should start with index 2: %q", lines[2])
	}
	if !strings.Contains(lines[2], dash) {
		t.Errorf("row with missing fields should contain a dash: %q", lines[2])
	}
}

func TestWriteGroupsTable(t *testing.T) {
	groups := []scim.Group{
		{ID: "grp-1", DisplayName: "Engineers"},
		{ID: "grp-2"}, // no display name
	}

	var buf bytes.Buffer
	if err := WriteGroupsTable(&buf, groups); err != nil {
		t.Fatalf("WriteGroupsTable: %v", err)
	}
	out := buf.String()

	for _, want := range []string{"DISPLAY NAME", "ID", "Engineers", "grp-1", "grp-2"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q\n%s", want, out)
		}
	}

	lines := nonEmptyLines(out)
	if len(lines) != 3 { // header + 2 groups
		t.Fatalf("got %d lines, want 3:\n%s", len(lines), out)
	}
	if !strings.Contains(lines[2], dash) {
		t.Errorf("group row with missing display name should contain a dash: %q", lines[2])
	}
}

func nonEmptyLines(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if strings.TrimSpace(l) != "" {
			out = append(out, l)
		}
	}
	return out
}
