package azdevops

import (
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"
	"sync"
)

// resolvedRepo is one repository an allow-list entry matched: the ID the
// list APIs narrow on and the name shown in warnings.
type resolvedRepo struct {
	ID   string
	Name string
}

// SetRepoFilters installs the per-project repository allow-list (project API
// name → configured names or globs). Projects absent from the map are
// unfiltered. Passing nil or an empty map removes every filter. Any cached
// resolution is discarded so the next ResolveRepoFilters (or the first
// filtered fetch) resolves against the new patterns.
//
// A setter rather than a NewMultiClient parameter keeps the constructor's
// signature — and every existing caller and test — untouched.
func (mc *MultiClient) SetRepoFilters(filters map[string][]string) {
	mc.repoFilterMu.Lock()
	defer mc.repoFilterMu.Unlock()
	mc.repoPatterns = nil
	mc.repoResolved = nil
	for project, patterns := range filters {
		if len(patterns) == 0 {
			continue
		}
		if mc.repoPatterns == nil {
			mc.repoPatterns = make(map[string][]string, len(filters))
		}
		mc.repoPatterns[project] = append([]string(nil), patterns...)
	}
}

// HasRepoFilter reports whether at least one project has a repository
// allow-list installed.
func (mc *MultiClient) HasRepoFilter() bool {
	mc.repoFilterMu.Lock()
	defer mc.repoFilterMu.Unlock()
	return len(mc.repoPatterns) > 0
}

// ResolveRepoFilters resolves every filtered project's configured names and
// globs to repository IDs, one ListRepositories call per filtered project,
// run concurrently. It is meant to be called once at start-up so unknown
// names surface as warnings before the first fetch; a project whose
// repository listing fails stays unresolved and is retried lazily by the
// first filtered fetch that needs it.
//
// warnings are user-facing sentences, one per problem (an entry matching
// nothing, a project matching nothing at all, a listing failure). err is
// non-nil when at least one listing failed and aggregates those failures;
// the matching warning is still present so a caller that only reads
// warnings misses nothing.
func (mc *MultiClient) ResolveRepoFilters() (warnings []string, err error) {
	mc.repoFilterMu.Lock()
	patterns := make(map[string][]string, len(mc.repoPatterns))
	for project, p := range mc.repoPatterns {
		if _, done := mc.repoResolved[project]; done {
			continue
		}
		patterns[project] = p
	}
	mc.repoFilterMu.Unlock()

	if len(patterns) == 0 {
		return nil, nil
	}

	type result struct {
		project  string
		warnings []string
		err      error
	}

	var wg sync.WaitGroup
	ch := make(chan result, len(patterns))
	for project := range patterns {
		wg.Add(1)
		go func(p string) {
			defer wg.Done()
			w, e := mc.resolveProjectRepoFilter(p)
			ch <- result{p, w, e}
		}(project)
	}
	wg.Wait()
	close(ch)

	results := make([]result, 0, len(patterns))
	for r := range ch {
		results = append(results, r)
	}
	// Deterministic order for the banner and for tests.
	sort.Slice(results, func(i, j int) bool { return results[i].project < results[j].project })

	var errs []error
	for _, r := range results {
		warnings = append(warnings, r.warnings...)
		if r.err != nil {
			errs = append(errs, r.err)
		}
	}
	return warnings, errors.Join(errs...)
}

// resolveProjectRepoFilter lists project's repositories, matches them against
// the configured patterns and caches the outcome. On a listing failure
// nothing is cached (so the next call retries) and the returned warning says
// so.
func (mc *MultiClient) resolveProjectRepoFilter(project string) ([]string, error) {
	mc.repoFilterMu.Lock()
	patterns := mc.repoPatterns[project]
	mc.repoFilterMu.Unlock()

	client, ok := mc.clients[project]
	if !ok || len(patterns) == 0 {
		return nil, nil
	}

	repos, err := client.ListRepositories()
	if err != nil {
		wrapped := fmt.Errorf("project %q: %w", project, err)
		return []string{fmt.Sprintf("could not list repositories for project %q: %v — the filter will be resolved on the first fetch",
			project, err)}, wrapped
	}

	resolved, warnings := matchRepoFilter(project, patterns, repos)

	mc.repoFilterMu.Lock()
	if mc.repoResolved == nil {
		mc.repoResolved = make(map[string][]resolvedRepo)
	}
	mc.repoResolved[project] = resolved
	mc.repoFilterMu.Unlock()

	return warnings, nil
}

// matchRepoFilter is the pure matching step: each pattern is compared
// case-insensitively, first as an exact name and then as a path.Match glob.
// Matched repositories are returned in configuration order without
// duplicates. An empty result is a deliberate fail-closed outcome — the
// caller shows nothing for the project — and the warnings say so.
func matchRepoFilter(project string, patterns []string, repos []Repository) ([]resolvedRepo, []string) {
	var resolved []resolvedRepo
	var warnings []string
	seen := make(map[string]struct{}, len(repos))

	for _, pattern := range patterns {
		lowerPattern := strings.ToLower(strings.TrimSpace(pattern))
		matchedAny := false
		for _, repo := range repos {
			lowerName := strings.ToLower(repo.Name)
			matched := lowerName == lowerPattern
			if !matched {
				// Malformed globs were dropped by config loading; a residual
				// ErrBadPattern here simply means "no match".
				matched, _ = path.Match(lowerPattern, lowerName)
			}
			if !matched {
				continue
			}
			matchedAny = true
			if _, dup := seen[repo.ID]; dup {
				continue
			}
			seen[repo.ID] = struct{}{}
			resolved = append(resolved, resolvedRepo{ID: repo.ID, Name: repo.Name})
		}
		if !matchedAny {
			warnings = append(warnings, fmt.Sprintf("project %q: repos entry %q matches no repository (known: %s)",
				project, pattern, knownRepoNames(repos)))
		}
	}

	if len(resolved) == 0 {
		warnings = append(warnings, fmt.Sprintf("project %q: no repos entry matches a repository, so the project shows no pull requests or pipeline runs until the config is corrected (press R to show every repository)",
			project))
		resolved = []resolvedRepo{}
	}
	return resolved, warnings
}

// knownRepoNames renders the repository names a warning lists, sorted and
// capped so a project with hundreds of repositories does not flood the banner.
func knownRepoNames(repos []Repository) string {
	const maxNames = 8
	if len(repos) == 0 {
		return "none"
	}
	names := make([]string, 0, len(repos))
	for _, r := range repos {
		names = append(names, r.Name)
	}
	sort.Strings(names)
	if len(names) > maxNames {
		return strings.Join(names[:maxNames], ", ") + fmt.Sprintf(", … %d more", len(names)-maxNames)
	}
	return strings.Join(names, ", ")
}

// reposFor returns the resolved allow-list for project. filtered is false
// for an unfiltered project (the caller makes its usual project-wide call).
// A filtered project whose start-up resolution failed is resolved here, on
// the first fetch that needs it; if that fails too the error is returned and
// becomes the project's failure in the PartialError accounting.
func (mc *MultiClient) reposFor(project string) (repos []resolvedRepo, filtered bool, err error) {
	mc.repoFilterMu.Lock()
	_, filtered = mc.repoPatterns[project]
	resolved, done := mc.repoResolved[project]
	mc.repoFilterMu.Unlock()

	if !filtered {
		return nil, false, nil
	}
	if done {
		return resolved, true, nil
	}

	if _, err := mc.resolveProjectRepoFilter(project); err != nil {
		return nil, true, fmt.Errorf("failed to resolve repository filter: %w", err)
	}

	mc.repoFilterMu.Lock()
	resolved = mc.repoResolved[project]
	mc.repoFilterMu.Unlock()
	return resolved, true, nil
}

// fetchProjectRows fetches one project's rows for a pane list method. With
// allRepos set, or for an unfiltered project, it is the single project-wide
// request every caller made before the repository filter existed. For a
// filtered project it is one request per allowed repository (concurrently,
// results concatenated in allow-list order) so each repository gets its own
// $top window; an empty allow-list returns no rows without a network call;
// any repository-level failure fails the project, keeping PartialError's
// "project" unit intact.
func fetchProjectRows[T any](mc *MultiClient, project string, client *Client, allRepos bool,
	fetch func(c *Client, repositoryID string) ([]T, error)) ([]T, error) {
	if allRepos {
		return fetch(client, "")
	}
	repos, filtered, err := mc.reposFor(project)
	if err != nil {
		return nil, err
	}
	if !filtered {
		return fetch(client, "")
	}
	if len(repos) == 0 {
		return nil, nil
	}

	type repoResult struct {
		rows []T
		err  error
	}
	results := make([]repoResult, len(repos))
	var wg sync.WaitGroup
	for i, repo := range repos {
		wg.Add(1)
		go func(i int, repositoryID string) {
			defer wg.Done()
			rows, err := fetch(client, repositoryID)
			results[i] = repoResult{rows, err}
		}(i, repo.ID)
	}
	wg.Wait()

	var all []T
	for i, r := range results {
		if r.err != nil {
			return nil, fmt.Errorf("repository %q: %w", repos[i].Name, r.err)
		}
		all = append(all, r.rows...)
	}
	return all, nil
}

// fanOut runs fetch for every project concurrently via fetchProjectRows,
// tags each row with its project via tag, merges the rows and applies the
// shared "all failed" / PartialError contract. Failed/Total are project
// counts, exactly as every MultiClient list method reports them.
func fanOut[T any](mc *MultiClient, allRepos bool,
	fetch func(c *Client, repositoryID string) ([]T, error),
	tag func(row *T, project, displayName string)) ([]T, error) {
	type result struct {
		project string
		rows    []T
		err     error
	}

	var wg sync.WaitGroup
	ch := make(chan result, len(mc.clients))

	for project, client := range mc.clients {
		wg.Add(1)
		go func(p string, c *Client) {
			defer wg.Done()
			rows, err := fetchProjectRows(mc, p, c, allRepos, fetch)
			ch <- result{p, rows, err}
		}(project, client)
	}

	go func() {
		wg.Wait()
		close(ch)
	}()

	var all []T
	var errs []error
	for r := range ch {
		if r.err != nil {
			errs = append(errs, r.err)
			continue
		}
		displayName := mc.DisplayNameFor(r.project)
		for i := range r.rows {
			tag(&r.rows[i], r.project, displayName)
		}
		all = append(all, r.rows...)
	}

	if len(errs) == len(mc.clients) {
		return nil, fmt.Errorf("all projects failed: %v", errs)
	}
	if len(errs) > 0 {
		return all, &PartialError{Failed: len(errs), Total: len(mc.clients), Errors: errs}
	}
	return all, nil
}

func tagPullRequest(pr *PullRequest, project, displayName string) {
	pr.ProjectName = project
	pr.ProjectDisplayName = displayName
}

func tagPipelineRun(run *PipelineRun, project, displayName string) {
	run.ProjectName = project
	run.ProjectDisplayName = displayName
}

func sortPullRequestsNewestFirst(prs []PullRequest) {
	sort.Slice(prs, func(i, j int) bool {
		return prs[i].CreationDate.After(prs[j].CreationDate)
	})
}

// fetchPipelineRunsForRepository is the per-request body shared by the
// project-wide and repository-scoped pipeline fetches: the recency-windowed
// list, with the active (queued/running) builds merged in separately so a
// build stuck queued for a long time can never be pushed out of the window
// covered by top (see Client.ListActivePipelineRuns). Best-effort: a failure
// of the active query does not invalidate the recent-runs fetch that already
// succeeded.
func fetchPipelineRunsForRepository(c *Client, repositoryID string, top int) ([]PipelineRun, error) {
	var (
		runs   []PipelineRun
		active []PipelineRun
		err    error
	)
	if repositoryID == "" {
		runs, err = c.ListPipelineRuns(top)
	} else {
		runs, err = c.ListPipelineRunsInRepository(repositoryID, top)
	}
	if err != nil {
		return nil, err
	}
	var activeErr error
	if repositoryID == "" {
		active, activeErr = c.ListActivePipelineRuns()
	} else {
		active, activeErr = c.ListActivePipelineRunsInRepository(repositoryID)
	}
	if activeErr == nil {
		runs = mergeActiveRuns(runs, active)
	}
	return runs, nil
}

// ListPullRequestsFiltered is ListPullRequests honouring the repository
// allow-list installed with SetRepoFilters. allRepos bypasses the filter for
// this call (the user's R toggle); with no filter installed both values
// behave identically.
func (mc *MultiClient) ListPullRequestsFiltered(top int, allRepos bool) ([]PullRequest, error) {
	prs, err := fanOut(mc, allRepos, func(c *Client, repositoryID string) ([]PullRequest, error) {
		prs, err := c.ListPullRequestsWithCriteria(top, PullRequestCriteria{RepositoryID: repositoryID})
		if err != nil {
			return nil, fmt.Errorf("failed to list pull requests: %w", err)
		}
		return prs, nil
	}, tagPullRequest)
	sortPullRequestsNewestFirst(prs)
	return prs, err
}

// ListMyPullRequestsFiltered is ListMyPullRequests honouring the repository
// allow-list (see ListPullRequestsFiltered for allRepos).
func (mc *MultiClient) ListMyPullRequestsFiltered(top int, allRepos bool) ([]PullRequest, error) {
	userID, err := mc.currentUserID()
	if err != nil {
		return nil, err
	}
	prs, err := fanOut(mc, allRepos, func(c *Client, repositoryID string) ([]PullRequest, error) {
		prs, err := c.ListPullRequestsWithCriteria(top, PullRequestCriteria{CreatorID: userID, RepositoryID: repositoryID})
		if err != nil {
			return nil, fmt.Errorf("failed to list my pull requests: %w", err)
		}
		return prs, nil
	}, tagPullRequest)
	sortPullRequestsNewestFirst(prs)
	return prs, err
}

// ListPullRequestsAsReviewerFiltered is ListPullRequestsAsReviewer honouring
// the repository allow-list (see ListPullRequestsFiltered for allRepos).
func (mc *MultiClient) ListPullRequestsAsReviewerFiltered(top int, allRepos bool) ([]PullRequest, error) {
	userID, err := mc.currentUserID()
	if err != nil {
		return nil, err
	}
	return mc.listPullRequestsAsReviewerFiltered(userID, top, allRepos)
}

func (mc *MultiClient) listPullRequestsAsReviewerFiltered(userID string, top int, allRepos bool) ([]PullRequest, error) {
	prs, err := fanOut(mc, allRepos, func(c *Client, repositoryID string) ([]PullRequest, error) {
		prs, err := c.ListPullRequestsWithCriteria(top, PullRequestCriteria{ReviewerID: userID, RepositoryID: repositoryID})
		if err != nil {
			return nil, fmt.Errorf("failed to list pull requests as reviewer: %w", err)
		}
		return prs, nil
	}, tagPullRequest)
	sortPullRequestsNewestFirst(prs)
	return prs, err
}

// ListPipelineRunsFiltered is ListPipelineRuns honouring the repository
// allow-list (see ListPullRequestsFiltered for allRepos). Builds sourced
// from repositories outside Azure Repos (GitHub and the like) carry no
// TfsGit ID and are therefore hidden while the filter applies.
func (mc *MultiClient) ListPipelineRunsFiltered(top int, allRepos bool) ([]PipelineRun, error) {
	runs, err := fanOut(mc, allRepos, func(c *Client, repositoryID string) ([]PipelineRun, error) {
		return fetchPipelineRunsForRepository(c, repositoryID, top)
	}, tagPipelineRun)
	sort.Slice(runs, func(i, j int) bool {
		return runs[i].QueueTime.After(runs[j].QueueTime)
	})
	return runs, err
}

// currentUserID resolves the authenticated user's ID from any project client
// (all share the same PAT and organisation).
func (mc *MultiClient) currentUserID() (string, error) {
	for _, client := range mc.clients {
		id, err := client.GetCurrentUserID()
		if err != nil {
			return "", fmt.Errorf("failed to get current user ID: %w", err)
		}
		return id, nil
	}
	return "", fmt.Errorf("no project clients configured")
}
