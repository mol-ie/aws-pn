package scim

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

const testToken = "secret-token-should-never-leak"

// recordedRequest captures the parts of a request the tests assert on.
type recordedRequest struct {
	method string
	path   string
	query  url.Values
}

func TestListPagination(t *testing.T) {
	for _, resource := range []string{resourceUsers, resourceGroups} {
		t.Run(resource, func(t *testing.T) {
			var reqs []recordedRequest

			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if err := r.ParseForm(); err != nil {
					t.Fatalf("parsing form: %v", err)
				}
				reqs = append(reqs, recordedRequest{
					method: r.Method,
					path:   r.URL.Path,
					query:  r.URL.Query(),
				})

				w.Header().Set("Content-Type", "application/scim+json")
				// Two pages: the first returns a nextCursor, the second does not.
				switch r.URL.Query().Get("cursor") {
				case "":
					fmt.Fprint(w, `{"Resources":[{"id":"1","userName":"a","displayName":"A"}],"nextCursor":"CUR2"}`)
				case "CUR2":
					fmt.Fprint(w, `{"Resources":[{"id":"2","userName":"b"}]}`)
				default:
					t.Fatalf("unexpected cursor %q", r.URL.Query().Get("cursor"))
				}
			}))
			defer srv.Close()

			c := NewClient(srv.URL, testToken, srv.Client())
			ctx := context.Background()

			var count int
			if resource == resourceUsers {
				users, err := c.ListUsers(ctx)
				if err != nil {
					t.Fatalf("ListUsers: %v", err)
				}
				count = len(users)
				if users[0].Raw == nil {
					t.Error("expected Raw to be populated")
				}
			} else {
				groups, err := c.ListGroups(ctx)
				if err != nil {
					t.Fatalf("ListGroups: %v", err)
				}
				count = len(groups)
				if groups[0].Raw == nil {
					t.Error("expected Raw to be populated")
				}
			}

			if count != 2 {
				t.Errorf("got %d resources across pages, want 2", count)
			}
			if len(reqs) != 2 {
				t.Fatalf("got %d requests, want 2 (one per page)", len(reqs))
			}

			// First request: cursor present and empty.
			if _, ok := reqs[0].query["cursor"]; !ok {
				t.Error("first request must include a cursor parameter")
			}
			if got := reqs[0].query.Get("cursor"); got != "" {
				t.Errorf("first request cursor: got %q, want empty", got)
			}
			// Second request: cursor is the nextCursor from page one.
			if got := reqs[1].query.Get("cursor"); got != "CUR2" {
				t.Errorf("second request cursor: got %q, want CUR2", got)
			}
			// Correct resource path, and startIndex never sent.
			for i, rq := range reqs {
				if !strings.HasSuffix(rq.path, "/"+resource) {
					t.Errorf("request %d path %q does not target /%s", i, rq.path, resource)
				}
				if _, ok := rq.query["startIndex"]; ok {
					t.Errorf("request %d sent startIndex, which is unsupported", i)
				}
			}
		})
	}
}

func TestListSinglePage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"Resources":[{"id":"1","userName":"only"}]}`)
	}))
	defer srv.Close()

	users, err := NewClient(srv.URL, testToken, srv.Client()).ListUsers(context.Background())
	if err != nil {
		t.Fatalf("ListUsers: %v", err)
	}
	if len(users) != 1 || users[0].UserName != "only" {
		t.Errorf("unexpected users: %+v", users)
	}
}

func TestAuthHeaderSent(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		fmt.Fprint(w, `{"Resources":[]}`)
	}))
	defer srv.Close()

	if _, err := NewClient(srv.URL, testToken, srv.Client()).ListUsers(context.Background()); err != nil {
		t.Fatalf("ListUsers: %v", err)
	}
	if gotAuth != "Bearer "+testToken {
		t.Errorf("Authorization header: got %q, want bearer token", gotAuth)
	}
}

func TestErrorStatusMapping(t *testing.T) {
	cases := []struct {
		status    int
		wantInErr string
	}{
		{http.StatusBadRequest, "HTTP 400"},
		{http.StatusUnauthorized, "HTTP 401"},
		{http.StatusForbidden, "HTTP 403"},
		{http.StatusNotFound, "HTTP 404"},
		{http.StatusTooManyRequests, "HTTP 429"},
		{http.StatusInternalServerError, "HTTP 500"},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprintf("status-%d", tc.status), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				fmt.Fprint(w, `{"detail":"boom"}`)
			}))
			defer srv.Close()

			_, err := NewClient(srv.URL, testToken, srv.Client()).ListUsers(context.Background())
			if err == nil {
				t.Fatalf("expected error for status %d", tc.status)
			}
			if !strings.Contains(err.Error(), tc.wantInErr) {
				t.Errorf("error %q should contain %q", err.Error(), tc.wantInErr)
			}
			if !strings.Contains(err.Error(), "boom") {
				t.Errorf("error %q should include server detail", err.Error())
			}
		})
	}
}

func TestDeleteSuccess(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusNoContent} {
		t.Run(fmt.Sprintf("status-%d", status), func(t *testing.T) {
			var gotMethod, gotPath string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotMethod, gotPath = r.Method, r.URL.Path
				w.WriteHeader(status)
			}))
			defer srv.Close()

			c := NewClient(srv.URL, testToken, srv.Client())
			if err := c.DeleteUser(context.Background(), "user-123"); err != nil {
				t.Fatalf("DeleteUser: %v", err)
			}
			if gotMethod != http.MethodDelete {
				t.Errorf("method: got %s, want DELETE", gotMethod)
			}
			if !strings.HasSuffix(gotPath, "/Users/user-123") {
				t.Errorf("path: got %q, want .../Users/user-123", gotPath)
			}
		})
	}
}

func TestDeleteGroupPath(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	if err := NewClient(srv.URL, testToken, srv.Client()).DeleteGroup(context.Background(), "grp-9"); err != nil {
		t.Fatalf("DeleteGroup: %v", err)
	}
	if !strings.HasSuffix(gotPath, "/Groups/grp-9") {
		t.Errorf("path: got %q, want .../Groups/grp-9", gotPath)
	}
}

func TestDeleteFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `{"detail":"not found"}`)
	}))
	defer srv.Close()

	err := NewClient(srv.URL, testToken, srv.Client()).DeleteUser(context.Background(), "nope")
	if err == nil {
		t.Fatal("expected error for 404 delete")
	}
	if !strings.Contains(err.Error(), "HTTP 404") {
		t.Errorf("error %q should contain HTTP 404", err.Error())
	}
}

// TestTokenNeverInErrors asserts that the client never surfaces the bearer
// token in its own error construction. The server returns a normal error body
// (it does not reflect the token); the test fails only if the client itself
// weaves the token into the error (for example via a logged URL or header).
func TestTokenNeverInErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"detail":"invalid bearer token"}`)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, testToken, srv.Client())

	_, listErr := c.ListUsers(context.Background())
	delErr := c.DeleteUser(context.Background(), "x")

	for _, err := range []error{listErr, delErr} {
		if err == nil {
			t.Fatal("expected an error")
		}
		if strings.Contains(err.Error(), testToken) {
			t.Errorf("error leaked the token: %q", err.Error())
		}
	}
}
