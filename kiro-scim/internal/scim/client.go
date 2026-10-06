package scim

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Resource paths under the SCIM base URL.
const (
	resourceUsers  = "Users"
	resourceGroups = "Groups"
)

// defaultTimeout bounds a single SCIM HTTP request.
const defaultTimeout = 30 * time.Second

// maxPages caps cursor pagination so a server that keeps returning a cursor
// cannot loop forever.
const maxPages = 10000

// userAgent identifies the tool in requests. It deliberately carries no
// sensitive information.
const userAgent = "kiro-scim/1.0"

// Client is a minimal SCIM 2.0 client for Kiro's IAM Identity Center endpoint.
type Client struct {
	baseURL    string
	token      string
	httpClient *http.Client
}

// NewClient returns a Client for the given SCIM base URL and bearer token. If
// hc is nil a default client with a sensible timeout is used.
func NewClient(baseURL, token string, hc *http.Client) *Client {
	if hc == nil {
		hc = &http.Client{Timeout: defaultTimeout}
	}
	return &Client{
		baseURL:    strings.TrimRight(baseURL, "/"),
		token:      token,
		httpClient: hc,
	}
}

// ListUsers retrieves every user, following cursor-based pagination.
func (c *Client) ListUsers(ctx context.Context) ([]User, error) {
	raws, err := c.listRaw(ctx, resourceUsers)
	if err != nil {
		return nil, err
	}
	users := make([]User, 0, len(raws))
	for _, raw := range raws {
		var u User
		if err := json.Unmarshal(raw, &u); err != nil {
			return nil, fmt.Errorf("decoding user resource: %w", err)
		}
		u.Raw = raw
		users = append(users, u)
	}
	return users, nil
}

// ListGroups retrieves every group, following cursor-based pagination.
func (c *Client) ListGroups(ctx context.Context) ([]Group, error) {
	raws, err := c.listRaw(ctx, resourceGroups)
	if err != nil {
		return nil, err
	}
	groups := make([]Group, 0, len(raws))
	for _, raw := range raws {
		var g Group
		if err := json.Unmarshal(raw, &g); err != nil {
			return nil, fmt.Errorf("decoding group resource: %w", err)
		}
		g.Raw = raw
		groups = append(groups, g)
	}
	return groups, nil
}

// DeleteUser deletes a single user by SCIM id.
func (c *Client) DeleteUser(ctx context.Context, id string) error {
	return c.deleteByID(ctx, resourceUsers, id)
}

// DeleteGroup deletes a single group by SCIM id.
func (c *Client) DeleteGroup(ctx context.Context, id string) error {
	return c.deleteByID(ctx, resourceGroups, id)
}

// listRaw walks cursor-based pagination for the given resource ("Users" or
// "Groups") and returns the raw JSON of every resource across all pages.
//
// IAM Identity Center uses cursor pagination, not startIndex: the first request
// carries an empty "cursor" query parameter, and each response that includes a
// non-empty "nextCursor" indicates another page, fetched by passing that value
// as "cursor". startIndex is never sent.
func (c *Client) listRaw(ctx context.Context, resource string) ([]json.RawMessage, error) {
	var all []json.RawMessage
	cursor := ""

	for page := 0; page < maxPages; page++ {
		q := url.Values{}
		// The first request must include an empty cursor parameter; subsequent
		// requests carry the previous page's nextCursor value.
		q.Set("cursor", cursor)

		endpoint := fmt.Sprintf("%s/%s?%s", c.baseURL, resource, q.Encode())
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return nil, fmt.Errorf("building %s list request: %w", resource, err)
		}
		c.setHeaders(req)

		resp, err := c.httpClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("listing %s: %w", resource, err)
		}

		body, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if readErr != nil {
			return nil, fmt.Errorf("reading %s list response: %w", resource, readErr)
		}

		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return nil, httpError(resp.StatusCode, body)
		}

		var lr listResponse
		if err := json.Unmarshal(body, &lr); err != nil {
			return nil, fmt.Errorf("decoding %s list response: %w", resource, err)
		}
		all = append(all, lr.Resources...)

		if lr.NextCursor == "" {
			return all, nil
		}
		cursor = lr.NextCursor
	}
	return nil, fmt.Errorf("listing %s: exceeded maximum of %d pages (possible pagination loop)", resource, maxPages)
}

// deleteByID issues DELETE {base}/{resource}/{id}. 200 and 204 are treated as
// success.
func (c *Client) deleteByID(ctx context.Context, resource, id string) error {
	endpoint := fmt.Sprintf("%s/%s/%s", c.baseURL, resource, url.PathEscape(id))
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, endpoint, nil)
	if err != nil {
		return fmt.Errorf("building delete request: %w", err)
	}
	c.setHeaders(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("deleting %s %s: %w", resource, id, err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusNoContent {
		return nil
	}
	return httpError(resp.StatusCode, body)
}

// setHeaders applies the auth and content negotiation headers common to every
// request.
func (c *Client) setHeaders(req *http.Request) {
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/scim+json")
	req.Header.Set("User-Agent", userAgent)
}

// scimError is the shape of an IAM Identity Center SCIM error body. Both the
// SCIM "detail" field and AWS-style "message" field are checked.
type scimError struct {
	Detail  string `json:"detail"`
	Message string `json:"message"`
}

// httpError maps a non-2xx response into an actionable error. It includes the
// HTTP status and any server-provided detail, but never the request's
// Authorization header or token.
func httpError(status int, body []byte) error {
	var hint string
	switch status {
	case http.StatusBadRequest:
		hint = "the request was rejected as invalid (ValidationException)"
	case http.StatusUnauthorized:
		hint = "the SCIM token is missing, invalid, or expired, or the endpoint tenant is wrong (UnauthorizedException)"
	case http.StatusForbidden:
		hint = "the token is not permitted to perform this operation (AccessDeniedException)"
	case http.StatusNotFound:
		hint = "the resource was not found (ResourceNotFoundException)"
	case http.StatusTooManyRequests:
		hint = "the request was throttled; retry later (ThrottlingException)"
	case http.StatusInternalServerError:
		hint = "the SCIM service failed to process the request (InternalServerException)"
	default:
		hint = "unexpected response from the SCIM service"
	}

	if detail := extractDetail(body); detail != "" {
		return fmt.Errorf("SCIM request failed (HTTP %d): %s: %s", status, hint, detail)
	}
	return fmt.Errorf("SCIM request failed (HTTP %d): %s", status, hint)
}

// extractDetail returns a human-readable message from a SCIM/AWS error body, or
// "" if none can be parsed.
func extractDetail(body []byte) string {
	var se scimError
	if err := json.Unmarshal(body, &se); err == nil {
		if se.Detail != "" {
			return se.Detail
		}
		if se.Message != "" {
			return se.Message
		}
	}
	return ""
}
