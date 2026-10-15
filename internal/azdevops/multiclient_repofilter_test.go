package azdevops

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// repoFilterProject is a fake project server that knows its repositories
// and serves pull requests and builds per repository, honouring the
// repositoryId narrowing the real API offers. It records every request so
// tests can pin request shapes.
type repoFilterProject struct {
	mu        sync.Mutex
	repos     []Repository
	prs       map[string][]PullRequest // repository ID → PRs ("" = every repo when unfiltered)
	builds    map[string][]PipelineRun
	requests  []string // path?query per request
	reposFail bool
	prsFail   map[string]bool // repository ID → fail the PR request
}

func (p *repoFilterProject) record(r *http.Request) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.requests = append(p.requests, r.URL.Path+"?"+r.URL.RawQuery)
}

func (p *repoFilterProject) requestsMatching(substr string) []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []string
	for _, r := range p.requests {
		if strings.Contains(r, substr) {
			out = append(out, r)
		}
	}
	return out
}

func (p *repoFilterProject) allRows(m map[string][]PullRequest) []PullRequest {
	var all []PullRequest
	for _, rows := range m {
		all = append(all, rows...)
	}
	return all
}

func (p *repoFilterProject) allBuilds() []PipelineRun {
	var all []PipelineRun
	for _, rows := range p.builds {
		all = append(all, rows...)
	}
	return all
}

func (p *repoFilterProject) server(t *testing.T) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.record(r)
		w.Header().Set("Content-Type", "application/json")
		q := r.URL.Query()
		switch {
		case strings.HasSuffix(r.URL.Path, "/git/repositories"):
			if p.reposFail {
				w.WriteHeader(http.StatusInternalServerError)
				w.Write([]byte(`{"message":"boom"}`))
				return
			}
			json.NewEncoder(w).Encode(RepositoriesResponse{Count: len(p.repos), Value: p.repos})
		case strings.HasSuffix(r.URL.Path, "/git/pullrequests"):
			repoID := q.Get("searchCriteria.repositoryId")
			if p.prsFail[repoID] {
				w.WriteHeader(http.StatusInternalServerError)
				w.Write([]byte(`{"message":"boom"}`))
				return
			}
			var rows []PullRequest
			if repoID == "" {
				rows = p.allRows(p.prs)
			} else {
				rows = p.prs[repoID]
			}
			json.NewEncoder(w).Encode(PullRequestsResponse{Count: len(rows), Value: rows})
		case strings.HasSuffix(r.URL.Path, "/build/builds"):
			repoID := q.Get("repositoryId")
			var rows []PipelineRun
			if repoID == "" {
				rows = p.allBuilds()
			} else {
				rows = p.builds[repoID]
			}
			// The active queries return nothing: these fixtures only
			// exercise the repository narrowing, not the active merge.
			if q.Has("statusFilter") {
				rows = nil
			}
			json.NewEncoder(w).Encode(PipelineRunsResponse{Count: len(rows), Value: rows})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(s.Close)
	return s
}

func newRepoFilterMultiClient(t *testing.T, projects map[string]*repoFilterProject) *MultiClient {
	t.Helper()
	servers := make(map[string]*httptest.Server, len(projects))
	for name, p := range projects {
		servers[name] = p.server(t)
	}
	mc := newMultiClientWithServers(t, servers)
	for _, c := range mc.clients {
		c.userID = "user-1"
	}
	return mc
}

func prIDs(prs []PullRequest) []int {
	ids := make([]int, 0, len(prs))
	for _, pr := range prs {
		ids = append(ids, pr.ID)
	}
	sort.Ints(ids)
	return ids
}

func runIDs(runs []PipelineRun) []int {
	ids := make([]int, 0, len(runs))
	for _, r := range runs {
		ids = append(ids, r.ID)
	}
	sort.Ints(ids)
	return ids
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func twoRepoProject() *repoFilterProject {
	now := time.Now()
	return &repoFilterProject{
		repos: []Repository{{ID: "id-front", Name: "frontend"}, {ID: "id-back", Name: "Backend"}, {ID: "id-infra", Name: "infra-tools"}},
		prs: map[string][]PullRequest{
			"id-front": {{ID: 1, CreationDate: now.Add(-1 * time.Minute), Repository: Repository{ID: "id-front", Name: "frontend"}}},
			"id-back":  {{ID: 2, CreationDate: now.Add(-2 * time.Minute), Repository: Repository{ID: "id-back", Name: "Backend"}}},
			"id-infra": {{ID: 3, CreationDate: now.Add(-3 * time.Minute), Repository: Repository{ID: "id-infra", Name: "infra-tools"}}},
		},
		builds: map[string][]PipelineRun{
			"id-front": {{ID: 11, QueueTime: now.Add(-1 * time.Minute), Repository: BuildRepository{ID: "id-front", Name: "frontend", Type: "TfsGit"}}},
			"id-back":  {{ID: 12, QueueTime: now.Add(-2 * time.Minute), Repository: BuildRepository{ID: "id-back", Name: "Backend", Type: "TfsGit"}}},
			"id-infra": {{ID: 13, QueueTime: now.Add(-3 * time.Minute), Repository: BuildRepository{ID: "id-infra", Name: "infra-tools", Type: "TfsGit"}}},
		},
	}
}

func TestMatchRepoFilter_ExactCaseInsensitiveAndGlob(t *testing.T) {
	repos := []Repository{{ID: "a", Name: "frontend"}, {ID: "b", Name: "Backend"}, {ID: "c", Name: "infra-tools"}, {ID: "d", Name: "infra-docs"}}

	resolved, warnings := matchRepoFilter("proj", []string{"FRONTEND", "backend", "infra-*", "infra-tools"}, repos)
	if len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", warnings)
	}
	got := make([]string, 0, len(resolved))
	for _, r := range resolved {
		got = append(got, r.ID)
	}
	want := []string{"a", "b", "c", "d"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("resolved IDs = %v, want %v (configuration order, de-duplicated)", got, want)
	}
}

func TestMatchRepoFilter_UnknownEntryWarnsAndFailsClosed(t *testing.T) {
	repos := []Repository{{ID: "a", Name: "frontend"}, {ID: "b", Name: "backend"}}

	resolved, warnings := matchRepoFilter("proj", []string{"frontnd"}, repos)
	if len(resolved) != 0 {
		t.Fatalf("expected no repositories, got %+v", resolved)
	}
	if resolved == nil {
		t.Fatal("a fully-missed project must resolve to an empty, non-nil set (fail closed)")
	}
	if len(warnings) != 2 {
		t.Fatalf("expected 2 warnings (entry + project), got %d: %v", len(warnings), warnings)
	}
	if !strings.Contains(warnings[0], `repos entry "frontnd" matches no repository`) || !strings.Contains(warnings[0], "known: backend, frontend") {
		t.Errorf("entry warning = %q", warnings[0])
	}
	if !strings.Contains(warnings[1], "no repos entry matches a repository") || !strings.Contains(warnings[1], "press R") {
		t.Errorf("project warning = %q", warnings[1])
	}
}

func TestMatchRepoFilter_PartialMissKeepsMatches(t *testing.T) {
	repos := []Repository{{ID: "a", Name: "frontend"}, {ID: "b", Name: "backend"}}

	resolved, warnings := matchRepoFilter("proj", []string{"frontend", "typo"}, repos)
	if len(resolved) != 1 || resolved[0].ID != "a" {
		t.Fatalf("expected only frontend, got %+v", resolved)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], `"typo"`) {
		t.Errorf("expected one entry warning, got %v", warnings)
	}
}

func TestKnownRepoNames_Capped(t *testing.T) {
	var repos []Repository
	for _, n := range []string{"j", "i", "h", "g", "f", "e", "d", "c", "b", "a"} {
		repos = append(repos, Repository{Name: n})
	}
	got := knownRepoNames(repos)
	if got != "a, b, c, d, e, f, g, h, … 2 more" {
		t.Errorf("knownRepoNames = %q", got)
	}
	if knownRepoNames(nil) != "none" {
		t.Errorf("empty list should read %q", "none")
	}
}

func TestMultiClient_SetRepoFilters_HasRepoFilter(t *testing.T) {
	mc := &MultiClient{}
	if mc.HasRepoFilter() {
		t.Fatal("zero MultiClient must be unfiltered")
	}
	mc.SetRepoFilters(map[string][]string{"alpha": {"frontend"}, "beta": {}})
	if !mc.HasRepoFilter() {
		t.Fatal("expected a filter after SetRepoFilters")
	}
	if _, ok := mc.repoPatterns["beta"]; ok {
		t.Error("an empty pattern list must not install a filter")
	}
	mc.SetRepoFilters(nil)
	if mc.HasRepoFilter() {
		t.Fatal("nil must clear every filter")
	}
}

func TestMultiClient_ResolveRepoFilters_WarnsAndCaches(t *testing.T) {
	alpha := twoRepoProject()
	beta := twoRepoProject()
	mc := newRepoFilterMultiClient(t, map[string]*repoFilterProject{"alpha": alpha, "beta": beta})
	mc.SetRepoFilters(map[string][]string{"alpha": {"frontend", "nope"}})

	warnings, err := mc.ResolveRepoFilters()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], `project "alpha": repos entry "nope"`) {
		t.Errorf("warnings = %v", warnings)
	}
	if got := len(alpha.requestsMatching("/git/repositories")); got != 1 {
		t.Errorf("alpha should list repositories once, got %d", got)
	}
	if got := len(beta.requestsMatching("/git/repositories")); got != 0 {
		t.Errorf("unfiltered beta must not list repositories, got %d", got)
	}

	// A second call is a no-op: the resolution is cached for the session.
	warnings, err = mc.ResolveRepoFilters()
	if err != nil || len(warnings) != 0 {
		t.Errorf("second resolve: warnings=%v err=%v", warnings, err)
	}
	if got := len(alpha.requestsMatching("/git/repositories")); got != 1 {
		t.Errorf("cached resolution must not re-list repositories, got %d requests", got)
	}
}

func TestMultiClient_ResolveRepoFilters_ListingFailureWarnsThenRetriesLazily(t *testing.T) {
	alpha := twoRepoProject()
	alpha.reposFail = true
	mc := newRepoFilterMultiClient(t, map[string]*repoFilterProject{"alpha": alpha})
	mc.SetRepoFilters(map[string][]string{"alpha": {"frontend"}})

	warnings, err := mc.ResolveRepoFilters()
	if err == nil {
		t.Fatal("expected an error when the repository listing fails")
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], `could not list repositories for project "alpha"`) ||
		!strings.Contains(warnings[0], "resolved on the first fetch") {
		t.Errorf("warnings = %v", warnings)
	}

	// Still failing: the project fails, which with a single project is a
	// total failure.
	if _, err := mc.ListPullRequestsFiltered(25, false); err == nil {
		t.Fatal("expected the fetch to fail while the listing keeps failing")
	}

	// Recovered: the first successful fetch resolves and narrows.
	alpha.reposFail = false
	prs, err := mc.ListPullRequestsFiltered(25, false)
	if err != nil {
		t.Fatalf("fetch after recovery: %v", err)
	}
	if !equalInts(prIDs(prs), []int{1}) {
		t.Errorf("expected only frontend's PR, got %v", prIDs(prs))
	}
}

func TestMultiClient_ListPullRequestsFiltered_OneRequestPerAllowedRepo(t *testing.T) {
	alpha := twoRepoProject()
	beta := twoRepoProject()
	mc := newRepoFilterMultiClient(t, map[string]*repoFilterProject{"alpha": alpha, "beta": beta})
	mc.SetRepoFilters(map[string][]string{"alpha": {"frontend", "backend"}})
	if _, err := mc.ResolveRepoFilters(); err != nil {
		t.Fatal(err)
	}

	prs, err := mc.ListPullRequestsFiltered(25, false)
	if err != nil {
		t.Fatalf("ListPullRequestsFiltered: %v", err)
	}
	// alpha narrowed to two repos (PRs 1, 2); beta unfiltered (1, 2, 3).
	if !equalInts(prIDs(prs), []int{1, 1, 2, 2, 3}) {
		t.Errorf("unexpected PR IDs %v", prIDs(prs))
	}
	for _, pr := range prs {
		if pr.ProjectName == "alpha" && pr.ID == 3 {
			t.Error("alpha's infra-tools PR must be filtered out")
		}
		if pr.ProjectName == "" {
			t.Error("rows must be tagged with their project")
		}
	}

	alphaPRRequests := alpha.requestsMatching("/git/pullrequests")
	if len(alphaPRRequests) != 2 {
		t.Fatalf("alpha should make one PR request per allowed repository, got %d: %v", len(alphaPRRequests), alphaPRRequests)
	}
	ids := map[string]bool{}
	for _, r := range alphaPRRequests {
		if !strings.Contains(r, "searchCriteria.repositoryId=") {
			t.Errorf("filtered request lacks repositoryId: %s", r)
		}
		for _, id := range []string{"id-front", "id-back"} {
			if strings.Contains(r, "repositoryId="+id) {
				ids[id] = true
			}
		}
	}
	if !ids["id-front"] || !ids["id-back"] {
		t.Errorf("expected requests for both allowed repositories, got %v", alphaPRRequests)
	}

	betaPRRequests := beta.requestsMatching("/git/pullrequests")
	if len(betaPRRequests) != 1 || strings.Contains(betaPRRequests[0], "repositoryId") {
		t.Errorf("unfiltered beta must make one project-wide request, got %v", betaPRRequests)
	}

	// Newest first across projects.
	for i := 1; i < len(prs); i++ {
		if prs[i].CreationDate.After(prs[i-1].CreationDate) {
			t.Fatal("PRs not sorted newest first")
		}
	}
}

func TestMultiClient_ListPullRequestsFiltered_AllReposBypassesFilter(t *testing.T) {
	alpha := twoRepoProject()
	mc := newRepoFilterMultiClient(t, map[string]*repoFilterProject{"alpha": alpha})
	mc.SetRepoFilters(map[string][]string{"alpha": {"frontend"}})

	prs, err := mc.ListPullRequestsFiltered(25, true)
	if err != nil {
		t.Fatal(err)
	}
	if !equalInts(prIDs(prs), []int{1, 2, 3}) {
		t.Errorf("allRepos must list every repository, got %v", prIDs(prs))
	}
	reqs := alpha.requestsMatching("/git/pullrequests")
	if len(reqs) != 1 || strings.Contains(reqs[0], "repositoryId") {
		t.Errorf("allRepos must be the single project-wide request, got %v", reqs)
	}
	if got := len(alpha.requestsMatching("/git/repositories")); got != 0 {
		t.Errorf("bypassing the filter must not resolve it, got %d listing requests", got)
	}
}

func TestMultiClient_FilteredVariants_ScopeCreatorAndReviewerToRepo(t *testing.T) {
	alpha := twoRepoProject()
	mc := newRepoFilterMultiClient(t, map[string]*repoFilterProject{"alpha": alpha})
	mc.SetRepoFilters(map[string][]string{"alpha": {"frontend"}})

	if _, err := mc.ListMyPullRequestsFiltered(25, false); err != nil {
		t.Fatal(err)
	}
	if _, err := mc.ListPullRequestsAsReviewerFiltered(25, false); err != nil {
		t.Fatal(err)
	}
	mine := alpha.requestsMatching("searchCriteria.creatorId=user-1")
	if len(mine) != 1 || !strings.Contains(mine[0], "searchCriteria.repositoryId=id-front") {
		t.Errorf("creator request must be scoped to the allowed repository: %v", mine)
	}
	review := alpha.requestsMatching("searchCriteria.reviewerId=user-1")
	if len(review) != 1 || !strings.Contains(review[0], "searchCriteria.repositoryId=id-front") {
		t.Errorf("reviewer request must be scoped to the allowed repository: %v", review)
	}
}

func TestMultiClient_ListPipelineRunsFiltered_NarrowsRecentAndActiveRequests(t *testing.T) {
	alpha := twoRepoProject()
	mc := newRepoFilterMultiClient(t, map[string]*repoFilterProject{"alpha": alpha})
	mc.SetRepoFilters(map[string][]string{"alpha": {"backend"}})

	runs, err := mc.ListPipelineRunsFiltered(30, false)
	if err != nil {
		t.Fatalf("ListPipelineRunsFiltered: %v", err)
	}
	if !equalInts(runIDs(runs), []int{12}) {
		t.Errorf("expected only Backend's build, got %v", runIDs(runs))
	}
	if runs[0].ProjectName != "alpha" || runs[0].Repository.Name != "Backend" {
		t.Errorf("row tagging/repository lost: %+v", runs[0])
	}

	buildReqs := alpha.requestsMatching("/build/builds")
	if len(buildReqs) != 3 {
		t.Fatalf("expected recent + 2 active requests for one repository, got %d: %v", len(buildReqs), buildReqs)
	}
	for _, r := range buildReqs {
		if !strings.Contains(r, "repositoryId=id-back") || !strings.Contains(r, "repositoryType=TfsGit") {
			t.Errorf("build request lacks repository narrowing: %s", r)
		}
	}
}

func TestMultiClient_Filtered_EmptyResolvedSetReturnsNoRowsWithoutRequests(t *testing.T) {
	alpha := twoRepoProject()
	mc := newRepoFilterMultiClient(t, map[string]*repoFilterProject{"alpha": alpha})
	mc.SetRepoFilters(map[string][]string{"alpha": {"does-not-exist"}})
	if _, err := mc.ResolveRepoFilters(); err != nil {
		t.Fatal(err)
	}

	prs, err := mc.ListPullRequestsFiltered(25, false)
	if err != nil {
		t.Fatalf("a fail-closed project is not an error: %v", err)
	}
	if len(prs) != 0 {
		t.Errorf("expected no rows, got %d", len(prs))
	}
	runs, err := mc.ListPipelineRunsFiltered(30, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 0 {
		t.Errorf("expected no runs, got %d", len(runs))
	}
	if n := len(alpha.requestsMatching("/git/pullrequests")) + len(alpha.requestsMatching("/build/builds")); n != 0 {
		t.Errorf("fail-closed project must make no list requests, got %d", n)
	}
}

func TestMultiClient_Filtered_RepoFailureFailsTheProjectOnly(t *testing.T) {
	alpha := twoRepoProject()
	alpha.prsFail = map[string]bool{"id-back": true}
	beta := twoRepoProject()
	mc := newRepoFilterMultiClient(t, map[string]*repoFilterProject{"alpha": alpha, "beta": beta})
	mc.SetRepoFilters(map[string][]string{"alpha": {"frontend", "backend"}})

	prs, err := mc.ListPullRequestsFiltered(25, false)
	partial, ok := err.(*PartialError)
	if !ok {
		t.Fatalf("expected a PartialError, got %v", err)
	}
	if partial.Failed != 1 || partial.Total != 2 {
		t.Errorf("PartialError = %d/%d, want 1/2 (project units)", partial.Failed, partial.Total)
	}
	if len(partial.Errors) != 1 || !strings.Contains(partial.Errors[0].Error(), `repository "Backend"`) {
		t.Errorf("error should name the failing repository: %v", partial.Errors)
	}
	for _, pr := range prs {
		if pr.ProjectName == "alpha" {
			t.Error("a failed project contributes no rows")
		}
	}
	if !equalInts(prIDs(prs), []int{1, 2, 3}) {
		t.Errorf("beta's rows must survive: %v", prIDs(prs))
	}
}

// The unfiltered methods the notification sources and metrics use must
// ignore an installed filter entirely.
func TestMultiClient_UnfilteredMethods_IgnoreInstalledFilter(t *testing.T) {
	alpha := twoRepoProject()
	mc := newRepoFilterMultiClient(t, map[string]*repoFilterProject{"alpha": alpha})
	mc.SetRepoFilters(map[string][]string{"alpha": {"frontend"}})

	prs, err := mc.ListPullRequests(25)
	if err != nil {
		t.Fatal(err)
	}
	if !equalInts(prIDs(prs), []int{1, 2, 3}) {
		t.Errorf("ListPullRequests must stay project-wide, got %v", prIDs(prs))
	}
	if _, err := mc.ListMyPullRequests(25); err != nil {
		t.Fatal(err)
	}
	if _, err := mc.ListPullRequestsAsReviewer(25); err != nil {
		t.Fatal(err)
	}
	if _, err := mc.ListPullRequestsAsReviewerForUser("user-1", 25); err != nil {
		t.Fatal(err)
	}
	runs, err := mc.ListPipelineRuns(30)
	if err != nil {
		t.Fatal(err)
	}
	if !equalInts(runIDs(runs), []int{11, 12, 13}) {
		t.Errorf("ListPipelineRuns must stay project-wide, got %v", runIDs(runs))
	}

	alpha.mu.Lock()
	defer alpha.mu.Unlock()
	for _, r := range alpha.requests {
		if strings.Contains(r, "repositoryId") {
			t.Errorf("unfiltered method narrowed by repository: %s", r)
		}
		if strings.Contains(r, "/git/repositories") {
			t.Errorf("unfiltered method resolved the filter: %s", r)
		}
	}
}
