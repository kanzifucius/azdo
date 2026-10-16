package demo

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Elpulgo/azdo/internal/azdevops"
)

func getJSON(t *testing.T, url string, into any) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for %s, got %d", url, resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(into); err != nil {
		t.Fatalf("decode failed: %v", err)
	}
}

func TestServerRepositories_PerProject(t *testing.T) {
	srv := httptest.NewServer(newMockHandler())
	defer srv.Close()

	var result azdevops.RepositoriesResponse
	getJSON(t, srv.URL+"/"+projectNexus+"/git/repositories?api-version=7.1", &result)
	if len(result.Value) != 2 || result.Value[0].Name != repoNameNexus || result.Value[1].Name != repoNameNexusInfra {
		t.Errorf("nexus repositories = %+v", result.Value)
	}

	getJSON(t, srv.URL+"/"+projectHorizon+"/git/repositories?api-version=7.1", &result)
	if len(result.Value) != 2 || result.Value[0].Name != repoNameHorizon {
		t.Errorf("horizon repositories = %+v", result.Value)
	}
}

func TestServerPullRequests_NarrowByRepositoryID(t *testing.T) {
	srv := httptest.NewServer(newMockHandler())
	defer srv.Close()

	var result azdevops.PullRequestsResponse
	getJSON(t, srv.URL+"/"+projectNexus+"/git/pullrequests?api-version=7.1&searchCriteria.repositoryId="+repoIDNexusInfra, &result)
	if len(result.Value) != 0 {
		t.Errorf("no demo PR lives in nexus-infra, got %d", len(result.Value))
	}
	getJSON(t, srv.URL+"/"+projectNexus+"/git/pullrequests?api-version=7.1&searchCriteria.repositoryId="+repoIDNexus, &result)
	if len(result.Value) == 0 {
		t.Fatal("expected nexus-platform PRs")
	}
	for _, pr := range result.Value {
		if pr.Repository.ID != repoIDNexus {
			t.Errorf("PR %d leaked from repository %s", pr.ID, pr.Repository.ID)
		}
	}
}

func TestServerBuilds_NarrowByRepositoryID(t *testing.T) {
	srv := httptest.NewServer(newMockHandler())
	defer srv.Close()

	var all, infra azdevops.PipelineRunsResponse
	getJSON(t, srv.URL+"/"+projectNexus+"/build/builds?api-version=7.1", &all)
	getJSON(t, srv.URL+"/"+projectNexus+"/build/builds?api-version=7.1&repositoryId="+repoIDNexusInfra+"&repositoryType=TfsGit", &infra)

	if len(infra.Value) == 0 || len(infra.Value) >= len(all.Value) {
		t.Fatalf("expected a strict subset for nexus-infra, got %d of %d", len(infra.Value), len(all.Value))
	}
	for _, run := range infra.Value {
		if run.Repository.ID != repoIDNexusInfra || run.Repository.Type != "TfsGit" {
			t.Errorf("build %d has repository %+v", run.ID, run.Repository)
		}
	}
	for _, run := range all.Value {
		if run.Repository.ID == "" {
			t.Errorf("build %d must carry a repository so the filter can narrow on it", run.ID)
		}
	}
}

// The demo filter (Nexus → nexus-platform only) must resolve cleanly against
// the mock server so the demo starts without a warning banner.
func TestDemoRepoFilter_ResolvesWithoutWarnings(t *testing.T) {
	srv := httptest.NewServer(newMockHandler())
	defer srv.Close()

	client, err := azdevops.NewMultiClient(demoOrg, []string{projectNexus, projectHorizon}, "demo-pat", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, project := range []string{projectNexus, projectHorizon} {
		client.ClientFor(project).SetBaseURL(srv.URL + "/" + project)
	}
	client.SetRepoFilters(map[string][]string{projectNexus: {repoNameNexus}})

	warnings, err := client.ResolveRepoFilters()
	if err != nil || len(warnings) != 0 {
		t.Fatalf("resolve: warnings=%v err=%v", warnings, err)
	}

	filtered, err := client.ListPipelineRunsFiltered(30, false)
	if err != nil {
		t.Fatal(err)
	}
	all, err := client.ListPipelineRunsFiltered(30, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(filtered) >= len(all) {
		t.Errorf("the filter should hide the nexus-infra builds: filtered=%d all=%d", len(filtered), len(all))
	}
	for _, run := range filtered {
		if run.ProjectName == projectNexus && run.Repository.ID != repoIDNexus {
			t.Errorf("nexus build %d from %s leaked through the filter", run.ID, run.Repository.Name)
		}
	}
}
