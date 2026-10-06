// Package app implements the kiro-scim command logic (list and delete),
// wiring together configuration, the SCIM client, and the UI.
package app

import (
	"context"

	"kiro-scim/internal/scim"
)

// Kind identifies which SCIM resource a command operates on.
type Kind int

const (
	Users Kind = iota
	Groups
)

// String returns the lowercase plural resource word ("users"/"groups").
func (k Kind) String() string {
	if k == Groups {
		return "groups"
	}
	return "users"
}

// ParseKind converts a resource word into a Kind. ok is false for anything
// other than "users" or "groups".
func ParseKind(s string) (k Kind, ok bool) {
	switch s {
	case "users":
		return Users, true
	case "groups":
		return Groups, true
	default:
		return 0, false
	}
}

// scimClient is the subset of *scim.Client the app package uses. Declaring it as
// an interface lets tests substitute a stub without real HTTP.
type scimClient interface {
	ListUsers(ctx context.Context) ([]scim.User, error)
	ListGroups(ctx context.Context) ([]scim.Group, error)
	DeleteUser(ctx context.Context, id string) error
	DeleteGroup(ctx context.Context, id string) error
}
