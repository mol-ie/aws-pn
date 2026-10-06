package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"kiro-scim/internal/scim"
	"kiro-scim/internal/ui"
)

// stubClient implements scimClient for tests.
type stubClient struct {
	users  []scim.User
	groups []scim.Group

	deletedUsers  []string
	deletedGroups []string
	// failIDs are ids whose deletion returns an error.
	failIDs map[string]bool
}

func (s *stubClient) ListUsers(context.Context) ([]scim.User, error)   { return s.users, nil }
func (s *stubClient) ListGroups(context.Context) ([]scim.Group, error) { return s.groups, nil }

func (s *stubClient) DeleteUser(_ context.Context, id string) error {
	if s.failIDs[id] {
		return errors.New("boom")
	}
	s.deletedUsers = append(s.deletedUsers, id)
	return nil
}

func (s *stubClient) DeleteGroup(_ context.Context, id string) error {
	if s.failIDs[id] {
		return errors.New("boom")
	}
	s.deletedGroups = append(s.deletedGroups, id)
	return nil
}

// fakeSelector returns a preset selection or error.
type fakeSelector struct {
	indexes []int
	err     error
}

func (f fakeSelector) Select(string, []string) ([]int, error) { return f.indexes, f.err }

func sampleUsers() []scim.User {
	return []scim.User{
		{ID: "u1", UserName: "alice", Raw: json.RawMessage(`{"id":"u1","userName":"alice"}`)},
		{ID: "u2", UserName: "bob", Raw: json.RawMessage(`{"id":"u2","userName":"bob"}`)},
	}
}

func newDeps(client scimClient, sel ui.Selector, dir string) (deleteDeps, *bytes.Buffer) {
	buf := &bytes.Buffer{}
	return deleteDeps{
		client:      client,
		selector:    sel,
		out:         buf,
		snapshotDir: dir,
		now:         func() time.Time { return time.Date(2026, 1, 15, 14, 25, 30, 0, time.UTC) },
		confirm:     func(string) (bool, error) { return true, nil },
	}, buf
}

func TestRunDeleteNothingSelected(t *testing.T) {
	c := &stubClient{users: sampleUsers()}
	d, buf := newDeps(c, fakeSelector{indexes: nil}, t.TempDir())

	if err := runDelete(context.Background(), d, Users, true, true); err != nil {
		t.Fatalf("runDelete: %v", err)
	}
	if len(c.deletedUsers) != 0 {
		t.Errorf("expected no deletions, got %v", c.deletedUsers)
	}
	if !strings.Contains(buf.String(), "nothing selected") {
		t.Errorf("expected 'nothing selected' message, got: %s", buf.String())
	}
}

func TestRunDeleteAbort(t *testing.T) {
	c := &stubClient{users: sampleUsers()}
	d, buf := newDeps(c, fakeSelector{err: ui.ErrAborted}, t.TempDir())

	if err := runDelete(context.Background(), d, Users, true, true); err != nil {
		t.Fatalf("runDelete: %v", err)
	}
	if len(c.deletedUsers) != 0 {
		t.Errorf("expected no deletions on abort, got %v", c.deletedUsers)
	}
	if !strings.Contains(buf.String(), "aborted") {
		t.Errorf("expected abort message, got: %s", buf.String())
	}
}

func TestRunDeleteNoTTY(t *testing.T) {
	c := &stubClient{users: sampleUsers()}
	d, _ := newDeps(c, fakeSelector{err: ui.ErrNoTTY}, t.TempDir())

	err := runDelete(context.Background(), d, Users, true, true)
	if err == nil {
		t.Fatal("expected error when no TTY")
	}
	if !strings.Contains(err.Error(), "interactive terminal") {
		t.Errorf("error should mention interactive terminal: %v", err)
	}
}

func TestRunDeleteDryRun(t *testing.T) {
	c := &stubClient{users: sampleUsers()}
	dir := t.TempDir()
	d, buf := newDeps(c, fakeSelector{indexes: []int{0, 1}}, dir)

	// confirm=false => dry run, regardless of yes.
	if err := runDelete(context.Background(), d, Users, false, false); err != nil {
		t.Fatalf("runDelete: %v", err)
	}
	if len(c.deletedUsers) != 0 {
		t.Errorf("dry run must not delete; got %v", c.deletedUsers)
	}
	if !strings.Contains(buf.String(), "DRY RUN") {
		t.Errorf("expected DRY RUN notice, got: %s", buf.String())
	}
	// No snapshot file should be created on a dry run.
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Errorf("dry run should not write a snapshot; found %d files", len(entries))
	}
}

func TestRunDeleteConfirmedWritesSnapshotAndDeletes(t *testing.T) {
	c := &stubClient{users: sampleUsers()}
	dir := t.TempDir()
	d, buf := newDeps(c, fakeSelector{indexes: []int{0, 1}}, dir)

	if err := runDelete(context.Background(), d, Users, true, true); err != nil {
		t.Fatalf("runDelete: %v", err)
	}
	if len(c.deletedUsers) != 2 {
		t.Errorf("expected 2 deletions, got %v", c.deletedUsers)
	}

	// Snapshot file exists, is named for the kind, and excludes the token.
	want := filepath.Join(dir, "kiro-scim-deleted-users-20260115-142530.json")
	data, err := os.ReadFile(want)
	if err != nil {
		t.Fatalf("reading snapshot: %v", err)
	}
	var snap snapshotFile
	if err := json.Unmarshal(data, &snap); err != nil {
		t.Fatalf("snapshot not valid JSON: %v", err)
	}
	if snap.Resource != "users" || snap.Count != 2 || len(snap.Records) != 2 {
		t.Errorf("unexpected snapshot header: %+v", snap)
	}
	if strings.Contains(string(data), "secret") || strings.Contains(string(data), "Bearer") {
		t.Errorf("snapshot should not contain any token material: %s", data)
	}

	// Reminders present.
	out := buf.String()
	if !strings.Contains(out, "seats") || !strings.Contains(out, "Entra") {
		t.Errorf("expected post-delete reminders, got: %s", out)
	}
}

func TestRunDeletePartialFailure(t *testing.T) {
	c := &stubClient{users: sampleUsers(), failIDs: map[string]bool{"u2": true}}
	dir := t.TempDir()
	d, buf := newDeps(c, fakeSelector{indexes: []int{0, 1}}, dir)

	err := runDelete(context.Background(), d, Users, true, true)
	if err == nil {
		t.Fatal("expected error when a deletion fails")
	}
	if len(c.deletedUsers) != 1 || c.deletedUsers[0] != "u1" {
		t.Errorf("expected u1 deleted and u2 failed, got %v", c.deletedUsers)
	}
	if !strings.Contains(buf.String(), "FAILED") {
		t.Errorf("expected a FAILED line, got: %s", buf.String())
	}
}

func TestRunDeleteConfirmCancelled(t *testing.T) {
	c := &stubClient{users: sampleUsers()}
	d, buf := newDeps(c, fakeSelector{indexes: []int{0}}, t.TempDir())
	d.confirm = func(string) (bool, error) { return false, nil } // operator says no

	if err := runDelete(context.Background(), d, Users, true, false); err != nil {
		t.Fatalf("runDelete: %v", err)
	}
	if len(c.deletedUsers) != 0 {
		t.Errorf("cancelled confirm must not delete; got %v", c.deletedUsers)
	}
	if !strings.Contains(buf.String(), "cancelled") {
		t.Errorf("expected cancellation message, got: %s", buf.String())
	}
}

func TestRunDeleteGroups(t *testing.T) {
	c := &stubClient{groups: []scim.Group{
		{ID: "g1", DisplayName: "Eng", Raw: json.RawMessage(`{"id":"g1","displayName":"Eng"}`)},
	}}
	dir := t.TempDir()
	d, _ := newDeps(c, fakeSelector{indexes: []int{0}}, dir)

	if err := runDelete(context.Background(), d, Groups, true, true); err != nil {
		t.Fatalf("runDelete: %v", err)
	}
	if len(c.deletedGroups) != 1 || c.deletedGroups[0] != "g1" {
		t.Errorf("expected group g1 deleted, got %v", c.deletedGroups)
	}
	if _, err := os.Stat(filepath.Join(dir, "kiro-scim-deleted-groups-20260115-142530.json")); err != nil {
		t.Errorf("expected groups snapshot file: %v", err)
	}
}

func TestListEmptyAndPopulated(t *testing.T) {
	// Empty users.
	var buf bytes.Buffer
	if err := list(context.Background(), &stubClient{}, Users, &buf); err != nil {
		t.Fatalf("list: %v", err)
	}
	if !strings.Contains(buf.String(), "no users found") {
		t.Errorf("expected 'no users found', got: %s", buf.String())
	}

	// Populated groups.
	buf.Reset()
	c := &stubClient{groups: []scim.Group{{ID: "g1", DisplayName: "Eng"}}}
	if err := list(context.Background(), c, Groups, &buf); err != nil {
		t.Fatalf("list: %v", err)
	}
	if !strings.Contains(buf.String(), "Eng") || !strings.Contains(buf.String(), "g1") {
		t.Errorf("expected group row, got: %s", buf.String())
	}
}

func TestParseKind(t *testing.T) {
	for _, tc := range []struct {
		in     string
		want   Kind
		wantOK bool
	}{
		{"users", Users, true},
		{"groups", Groups, true},
		{"user", 0, false},
		{"", 0, false},
	} {
		got, ok := ParseKind(tc.in)
		if ok != tc.wantOK || (ok && got != tc.want) {
			t.Errorf("ParseKind(%q) = (%v,%v), want (%v,%v)", tc.in, got, ok, tc.want, tc.wantOK)
		}
	}
}
