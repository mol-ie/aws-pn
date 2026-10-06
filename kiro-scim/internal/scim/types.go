// Package scim is a minimal SCIM 2.0 client for Kiro's AWS IAM Identity Center
// SCIM endpoint, implementing only the operations kiro-scim needs: listing
// users and groups (with cursor-based pagination) and deleting a user or group
// by id.
package scim

import "encoding/json"

// User is the subset of a SCIM User resource that kiro-scim displays, plus the
// full original record kept for the deletion snapshot.
//
// The IAM Identity Center ListUsers response contains many more fields; only
// the ones rendered in the users table are given named fields here. Raw holds
// the complete, unmodified JSON for the resource so the snapshot is a faithful
// record of what was deleted.
type User struct {
	ID          string  `json:"id"`
	UserName    string  `json:"userName"`
	DisplayName string  `json:"displayName,omitempty"`
	Active      bool    `json:"active"`
	Emails      []Email `json:"emails,omitempty"`

	// Raw is the full original JSON of this resource. It is populated by the
	// client from the per-resource bytes and is not itself (un)marshaled as a
	// field of User.
	Raw json.RawMessage `json:"-"`
}

// PrimaryEmail returns the value of the user's primary email, falling back to
// the first email if none is explicitly marked primary, or "" if the user has
// no emails.
func (u User) PrimaryEmail() string {
	if len(u.Emails) == 0 {
		return ""
	}
	for _, e := range u.Emails {
		if e.Primary {
			return e.Value
		}
	}
	return u.Emails[0].Value
}

// Group is the subset of a SCIM Group resource that kiro-scim displays, plus
// the full original record kept for the deletion snapshot.
//
// Note: the IAM Identity Center ListGroups endpoint always returns an empty
// members array, so group membership cannot be shown from a list call and is
// intentionally not modeled here.
type Group struct {
	ID          string `json:"id"`
	DisplayName string `json:"displayName,omitempty"`
	ExternalID  string `json:"externalId,omitempty"`

	// Raw is the full original JSON of this resource (see User.Raw).
	Raw json.RawMessage `json:"-"`
}

// Email is one entry of a SCIM User's emails array.
type Email struct {
	Value   string `json:"value"`
	Type    string `json:"type,omitempty"`
	Primary bool   `json:"primary,omitempty"`
}

// listResponse models the parts of a SCIM ListResponse that the client needs:
// the raw per-resource records and the cursor-pagination continuation token.
//
// Each element of Resources is kept as json.RawMessage so the client can both
// unmarshal it into a typed User/Group for display and preserve the original
// bytes for the snapshot. NextCursor is empty on the final (or only) page.
type listResponse struct {
	Resources  []json.RawMessage `json:"Resources"`
	NextCursor string            `json:"nextCursor,omitempty"`
}
