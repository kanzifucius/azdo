package azdevops

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

// captureServer records every request's path and query and replies with body.
func captureServer(t *testing.T, body string) (*httptest.Server, *[]*url.URL) {
	t.Helper()
	var seen []*url.URL
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u := *r.URL
		seen = append(seen, &u)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	return server, &seen
}

func TestClient_ListRepositories(t *testing.T) {
	server, seen := captureServer(t, `{"count":2,"value":[
		{"id":"id-a","name":"frontend"},
		{"id":"id-b","name":"Backend"}
	]}`)

	repos, err := newTestClient(server.URL).ListRepositories()
	if err != nil {
		t.Fatalf("ListRepositories: %v", err)
	}
	if len(repos) != 2 || repos[0].ID != "id-a" || repos[1].Name != "Backend" {
		t.Fatalf("unexpected repositories: %+v", repos)
	}
	if len(*seen) != 1 {
		t.Fatalf("expected one request, got %d", len(*seen))
	}
	req := (*seen)[0]
	if req.Path != "/test-org/test-project/_apis/git/repositories" {
		t.Errorf("unexpected path %q", req.Path)
	}
	if req.Query().Get("api-version") != "7.1" {
		t.Errorf("expected api-version=7.1, got %q", req.Query().Get("api-version"))
	}
}

func TestClient_ListRepositories_Error(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer server.Close()

	if _, err := newTestClient(server.URL).ListRepositories(); err == nil {
		t.Fatal("expected an error for a 403 response")
	}
}

func TestPullRequestCriteria_QueryString(t *testing.T) {
	tests := []struct {
		name     string
		criteria PullRequestCriteria
		want     map[string]string
		absent   []string
	}{
		{
			name:     "zero value lists every active PR",
			criteria: PullRequestCriteria{},
			want:     map[string]string{"$top": "25", "searchCriteria.status": "active"},
			absent:   []string{"searchCriteria.creatorId", "searchCriteria.reviewerId", "searchCriteria.repositoryId"},
		},
		{
			name:     "repository only",
			criteria: PullRequestCriteria{RepositoryID: "repo-1"},
			want:     map[string]string{"searchCriteria.repositoryId": "repo-1"},
			absent:   []string{"searchCriteria.creatorId", "searchCriteria.reviewerId"},
		},
		{
			name:     "creator scoped to a repository combines",
			criteria: PullRequestCriteria{CreatorID: "me", RepositoryID: "repo-1"},
			want:     map[string]string{"searchCriteria.creatorId": "me", "searchCriteria.repositoryId": "repo-1"},
			absent:   []string{"searchCriteria.reviewerId"},
		},
		{
			name:     "reviewer scoped to a repository combines",
			criteria: PullRequestCriteria{ReviewerID: "me", RepositoryID: "repo-1"},
			want:     map[string]string{"searchCriteria.reviewerId": "me", "searchCriteria.repositoryId": "repo-1"},
			absent:   []string{"searchCriteria.creatorId"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			u, err := url.Parse(tt.criteria.queryString(25))
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if u.Path != "/git/pullrequests" {
				t.Errorf("unexpected path %q", u.Path)
			}
			q := u.Query()
			for k, v := range tt.want {
				if got := q.Get(k); got != v {
					t.Errorf("%s = %q, want %q", k, got, v)
				}
			}
			for _, k := range tt.absent {
				if q.Has(k) {
					t.Errorf("%s should be absent, got %q", k, q.Get(k))
				}
			}
		})
	}
}

func TestClient_ListPullRequestsWithCriteria_SendsRepositoryID(t *testing.T) {
	server, seen := captureServer(t, `{"count":1,"value":[{"pullRequestId":7,"title":"x","repository":{"id":"repo-1","name":"frontend"}}]}`)

	prs, err := newTestClient(server.URL).ListPullRequestsWithCriteria(25, PullRequestCriteria{RepositoryID: "repo-1"})
	if err != nil {
		t.Fatalf("ListPullRequestsWithCriteria: %v", err)
	}
	if len(prs) != 1 || prs[0].ID != 7 {
		t.Fatalf("unexpected PRs: %+v", prs)
	}
	q := (*seen)[0].Query()
	if q.Get("searchCriteria.repositoryId") != "repo-1" {
		t.Errorf("expected searchCriteria.repositoryId=repo-1, got %q", q.Get("searchCriteria.repositoryId"))
	}
}

// The three named list methods must keep their exact request shape: they
// back the notification sources and every existing caller.
func TestClient_NamedPullRequestListMethods_UnchangedShape(t *testing.T) {
	server, seen := captureServer(t, `{"count":0,"value":[]}`)
	c := newTestClient(server.URL)

	if _, err := c.ListPullRequests(25); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ListMyPullRequests("user-1", 25); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ListPullRequestsAsReviewer("user-1", 25); err != nil {
		t.Fatal(err)
	}

	if len(*seen) != 3 {
		t.Fatalf("expected 3 requests, got %d", len(*seen))
	}
	for i, req := range *seen {
		q := req.Query()
		if q.Has("searchCriteria.repositoryId") {
			t.Errorf("request %d must not carry searchCriteria.repositoryId", i)
		}
		if q.Get("searchCriteria.status") != "active" || q.Get("$top") != "25" {
			t.Errorf("request %d lost its base query: %s", i, req.RawQuery)
		}
	}
	if (*seen)[1].Query().Get("searchCriteria.creatorId") != "user-1" {
		t.Error("ListMyPullRequests must send searchCriteria.creatorId")
	}
	if (*seen)[2].Query().Get("searchCriteria.reviewerId") != "user-1" {
		t.Error("ListPullRequestsAsReviewer must send searchCriteria.reviewerId")
	}
}

func TestClient_ListPipelineRunsInRepository_SendsRepositoryIDAndType(t *testing.T) {
	server, seen := captureServer(t, `{"count":1,"value":[{"id":1,"buildNumber":"1","repository":{"id":"repo-1","name":"frontend","type":"TfsGit"}}]}`)

	runs, err := newTestClient(server.URL).ListPipelineRunsInRepository("repo-1", 30)
	if err != nil {
		t.Fatalf("ListPipelineRunsInRepository: %v", err)
	}
	if len(runs) != 1 || runs[0].Repository.Name != "frontend" || runs[0].Repository.Type != "TfsGit" {
		t.Fatalf("repository field not decoded: %+v", runs)
	}
	q := (*seen)[0].Query()
	if q.Get("repositoryId") != "repo-1" {
		t.Errorf("expected repositoryId=repo-1, got %q", q.Get("repositoryId"))
	}
	if q.Get("repositoryType") != "TfsGit" {
		t.Errorf("expected repositoryType=TfsGit, got %q", q.Get("repositoryType"))
	}
	if q.Get("$top") != "30" || q.Get("queryOrder") != "queueTimeDescending" {
		t.Errorf("base query lost: %s", (*seen)[0].RawQuery)
	}
}

func TestClient_ListActivePipelineRunsInRepository_BothRequestsNarrowed(t *testing.T) {
	server, seen := captureServer(t, `{"count":0,"value":[]}`)

	if _, err := newTestClient(server.URL).ListActivePipelineRunsInRepository("repo-1"); err != nil {
		t.Fatalf("ListActivePipelineRunsInRepository: %v", err)
	}
	if len(*seen) != 2 {
		t.Fatalf("expected 2 requests (notStarted, inProgress), got %d", len(*seen))
	}
	statuses := map[string]bool{}
	for _, req := range *seen {
		q := req.Query()
		statuses[q.Get("statusFilter")] = true
		if q.Get("repositoryId") != "repo-1" || q.Get("repositoryType") != "TfsGit" {
			t.Errorf("request %s lacks repository narrowing", req.RawQuery)
		}
	}
	if !statuses["notStarted"] || !statuses["inProgress"] {
		t.Errorf("expected both statusFilter values, got %v", statuses)
	}
}

func TestClient_ProjectWidePipelineListMethods_UnchangedShape(t *testing.T) {
	server, seen := captureServer(t, `{"count":0,"value":[]}`)
	c := newTestClient(server.URL)

	if _, err := c.ListPipelineRuns(30); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ListActivePipelineRuns(); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ListMyFailedPipelineRuns("user-1", 7, 50); err != nil {
		t.Fatal(err)
	}
	for _, req := range *seen {
		q := req.Query()
		if q.Has("repositoryId") || q.Has("repositoryType") {
			t.Errorf("project-wide request must not narrow by repository: %s", req.RawQuery)
		}
	}
}
