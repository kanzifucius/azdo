package azdevops

import (
	"sort"
	"strings"
	"testing"

	"github.com/Elpulgo/azdo/internal/provider"
)

func providerPRIDs(prs []provider.PullRequest) []string {
	ids := make([]string, 0, len(prs))
	for _, pr := range prs {
		ids = append(ids, pr.Identity.ID)
	}
	sort.Strings(ids)
	return ids
}

func TestAdapter_ListPullRequests_HonoursRepoFilterUnlessAllRepos(t *testing.T) {
	alpha := twoRepoProject()
	mc := newRepoFilterMultiClient(t, map[string]*repoFilterProject{"alpha": alpha})
	mc.SetRepoFilters(map[string][]string{"alpha": {"frontend"}})
	adapter := NewAdapter(mc)

	filtered, err := adapter.ListPullRequests(25, provider.ListOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if got := providerPRIDs(filtered); strings.Join(got, ",") != "1" {
		t.Errorf("zero-value opts must honour the filter, got %v", got)
	}
	if filtered[0].Identity.Scope != "alpha" {
		t.Errorf("identity scope lost: %+v", filtered[0].Identity)
	}

	all, err := adapter.ListPullRequests(25, provider.ListOpts{AllRepos: true})
	if err != nil {
		t.Fatal(err)
	}
	if got := providerPRIDs(all); strings.Join(got, ",") != "1,2,3" {
		t.Errorf("AllRepos must bypass the filter, got %v", got)
	}
}

func TestAdapter_MyAndReviewerPullRequests_RouteAllRepos(t *testing.T) {
	alpha := twoRepoProject()
	mc := newRepoFilterMultiClient(t, map[string]*repoFilterProject{"alpha": alpha})
	mc.SetRepoFilters(map[string][]string{"alpha": {"frontend"}})
	adapter := NewAdapter(mc)

	if _, err := adapter.ListMyPullRequests(25, provider.ListOpts{}); err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.ListMyPullRequests(25, provider.ListOpts{AllRepos: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.ListPullRequestsAsReviewer(25, provider.ListOpts{}); err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.ListPullRequestsAsReviewer(25, provider.ListOpts{AllRepos: true}); err != nil {
		t.Fatal(err)
	}

	mine := alpha.requestsMatching("searchCriteria.creatorId=")
	review := alpha.requestsMatching("searchCriteria.reviewerId=")
	if len(mine) != 2 || len(review) != 2 {
		t.Fatalf("expected two creator and two reviewer requests, got %d/%d", len(mine), len(review))
	}
	countScoped := func(reqs []string) int {
		n := 0
		for _, r := range reqs {
			if strings.Contains(r, "searchCriteria.repositoryId=") {
				n++
			}
		}
		return n
	}
	if countScoped(mine) != 1 || countScoped(review) != 1 {
		t.Errorf("exactly one of each pair should be repository-scoped: mine=%v review=%v", mine, review)
	}
}

func TestAdapter_ListPipelineRuns_HonoursRepoFilterAndMapsRepositoryName(t *testing.T) {
	alpha := twoRepoProject()
	mc := newRepoFilterMultiClient(t, map[string]*repoFilterProject{"alpha": alpha})
	mc.SetRepoFilters(map[string][]string{"alpha": {"backend"}})
	adapter := NewAdapter(mc)

	runs, err := adapter.ListPipelineRuns(30, provider.ListOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].Identity.ID != "12" {
		t.Fatalf("expected only Backend's run, got %+v", runs)
	}
	if runs[0].RepositoryName != "Backend" {
		t.Errorf("RepositoryName = %q, want Backend", runs[0].RepositoryName)
	}

	all, err := adapter.ListPipelineRuns(30, provider.ListOpts{AllRepos: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Errorf("AllRepos must list every repository's runs, got %d", len(all))
	}
}

func TestMapPipelineRun_RepositoryName(t *testing.T) {
	mapped := MapPipelineRun(PipelineRun{ID: 1, Repository: BuildRepository{ID: "x", Name: "frontend", Type: "TfsGit"}}, "p", "P")
	if mapped.RepositoryName != "frontend" {
		t.Errorf("RepositoryName = %q", mapped.RepositoryName)
	}
	if MapPipelineRun(PipelineRun{ID: 2}, "p", "P").RepositoryName != "" {
		t.Error("a build without a repository must map to an empty RepositoryName")
	}
}
