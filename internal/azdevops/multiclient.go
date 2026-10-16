package azdevops

import (
	"fmt"
	"sort"
	"sync"
	"time"
)

// MultiClient wraps multiple project-scoped clients for concurrent fetching.
type MultiClient struct {
	org          string
	pat          string
	clients      map[string]*Client // project name → client
	displayNames map[string]string  // API name → display name

	// Per-project repository allow-list (see multiclient_repofilter.go).
	// repoPatterns holds the configured names/globs per filtered project;
	// repoResolved caches the matched repositories once resolved. Both are
	// nil when no filter is installed, so a zero MultiClient is unfiltered.
	repoFilterMu sync.Mutex
	repoPatterns map[string][]string
	repoResolved map[string][]resolvedRepo
}

// NewMultiClient creates clients for each project.
// displayNames is an optional map of API name → display name for UI rendering.
func NewMultiClient(org string, projects []string, pat string, displayNames map[string]string) (*MultiClient, error) {
	if len(projects) == 0 {
		return nil, fmt.Errorf("at least one project is required")
	}

	clients := make(map[string]*Client, len(projects))
	for _, project := range projects {
		c, err := NewClient(org, project, pat)
		if err != nil {
			return nil, fmt.Errorf("failed to create client for project %q: %w", project, err)
		}
		clients[project] = c
	}
	return &MultiClient{org: org, pat: pat, clients: clients, displayNames: displayNames}, nil
}

// DisplayNameFor returns the display name for a project API name.
// If no display name is configured, returns the API name itself.
func (mc *MultiClient) DisplayNameFor(project string) string {
	if mc.displayNames != nil {
		if dn, ok := mc.displayNames[project]; ok {
			return dn
		}
	}
	return project
}

// ClientFor returns the project-specific client (for detail views).
func (mc *MultiClient) ClientFor(project string) *Client {
	return mc.clients[project]
}

// GetOrg returns the organization name.
func (mc *MultiClient) GetOrg() string { return mc.org }

// IsMultiProject returns true if more than one project is configured.
func (mc *MultiClient) IsMultiProject() bool { return len(mc.clients) > 1 }

// Projects returns the list of project names.
func (mc *MultiClient) Projects() []string {
	projects := make([]string, 0, len(mc.clients))
	for p := range mc.clients {
		projects = append(projects, p)
	}
	return projects
}

// ListPipelineRuns fetches pipeline runs from all projects concurrently,
// merges and sorts by QueueTime descending. It never applies the repository
// allow-list — the pipelines pane goes through ListPipelineRunsFiltered for
// that — so every other caller keeps the project-wide behaviour.
func (mc *MultiClient) ListPipelineRuns(top int) ([]PipelineRun, error) {
	return mc.ListPipelineRunsFiltered(top, true)
}

// mergeActiveRuns appends active runs not already present in recent (by ID)
// so a build returned by both the recency-windowed and the active queries
// isn't duplicated.
func mergeActiveRuns(recent, active []PipelineRun) []PipelineRun {
	seen := make(map[int]struct{}, len(recent))
	for _, r := range recent {
		seen[r.ID] = struct{}{}
	}
	for _, a := range active {
		if _, ok := seen[a.ID]; ok {
			continue
		}
		seen[a.ID] = struct{}{}
		recent = append(recent, a)
	}
	return recent
}

// ListMyFailedPipelineRuns fetches completed, failed pipeline runs requested
// for userID within the last lookbackDays days from all projects
// concurrently, tags each with ProjectName/ProjectDisplayName, merges and
// sorts by FinishTime descending. Mirrors ListRecentlyAssignedWorkItems's
// fan-out over Client.ListRecentlyAssignedWorkItems: same PartialError
// contract, and Failed/Total below are **project counts** (how many of
// mc.clients failed), not a count of individual runs.
//
// Backs the "my failed pipeline runs" notification source (task 7 of the
// phase-2 notifications spec, see SourceCIFailed in
// notifications_source_cifailed.go and Client.ListMyFailedPipelineRuns for
// the server-side query itself).
func (mc *MultiClient) ListMyFailedPipelineRuns(userID string, lookbackDays, top int) ([]PipelineRun, error) {
	type result struct {
		project string
		runs    []PipelineRun
		err     error
	}

	var wg sync.WaitGroup
	ch := make(chan result, len(mc.clients))

	for project, client := range mc.clients {
		wg.Add(1)
		go func(p string, c *Client) {
			defer wg.Done()
			runs, err := c.ListMyFailedPipelineRuns(userID, lookbackDays, top)
			ch <- result{p, runs, err}
		}(project, client)
	}

	go func() {
		wg.Wait()
		close(ch)
	}()

	var allRuns []PipelineRun
	var errs []error
	for r := range ch {
		if r.err != nil {
			errs = append(errs, r.err)
			continue
		}
		for i := range r.runs {
			r.runs[i].ProjectName = r.project
			r.runs[i].ProjectDisplayName = mc.DisplayNameFor(r.project)
		}
		allRuns = append(allRuns, r.runs...)
	}

	if len(errs) == len(mc.clients) {
		return nil, fmt.Errorf("all projects failed: %v", errs)
	}

	sort.Slice(allRuns, func(i, j int) bool {
		return finishTimeOrZero(allRuns[i]).After(finishTimeOrZero(allRuns[j]))
	})

	if len(errs) > 0 {
		return allRuns, &PartialError{Failed: len(errs), Total: len(mc.clients), Errors: errs}
	}

	return allRuns, nil
}

// finishTimeOrZero reads run.FinishTime for the merge sort in
// ListMyFailedPipelineRuns without dereferencing a nil pointer. A run this
// source keeps should always carry a non-nil FinishTime (it is
// isMyFailedRun-filtered to Status "completed" before this sort matters in
// practice), but the fixture used to pin the belt-and-braces re-check
// (finding 1 of task 7's review) deliberately returns a mixed bag including
// in-progress runs with a nil FinishTime, and this must not panic on those.
func finishTimeOrZero(run PipelineRun) time.Time {
	if run.FinishTime == nil {
		return time.Time{}
	}
	return *run.FinishTime
}

// ListPullRequests fetches PRs from all projects concurrently,
// tags each with ProjectName, merges and sorts by CreationDate descending.
// It never applies the repository allow-list (see ListPullRequestsFiltered).
func (mc *MultiClient) ListPullRequests(top int) ([]PullRequest, error) {
	return mc.ListPullRequestsFiltered(top, true)
}

// ListMyPullRequests fetches PRs created by the authenticated user from all
// projects concurrently, tags each with ProjectName, merges and sorts by
// CreationDate descending. It never applies the repository allow-list (see
// ListMyPullRequestsFiltered).
func (mc *MultiClient) ListMyPullRequests(top int) ([]PullRequest, error) {
	return mc.ListMyPullRequestsFiltered(top, true)
}

// ListPullRequestsAsReviewer fetches PRs where the authenticated user is a
// reviewer from all projects concurrently, tags each with ProjectName, merges
// and sorts by CreationDate descending. It resolves the authenticated user id
// itself via Client.GetCurrentUserID; see ListPullRequestsAsReviewerForUser
// for a variant that accepts an already-resolved id instead. It never applies
// the repository allow-list (see ListPullRequestsAsReviewerFiltered).
func (mc *MultiClient) ListPullRequestsAsReviewer(top int) ([]PullRequest, error) {
	return mc.ListPullRequestsAsReviewerFiltered(top, true)
}

// ListPullRequestsAsReviewerForUser is ListPullRequestsAsReviewer's variant
// for a caller that has already resolved the authenticated user id itself.
// SourceReviewRequested (notifications_source_review.go) uses this so that
// runSourcesConcurrently's single upfront resolveAuthenticatedUserID call is
// what every concurrently running source shares, rather than each source
// (this one included) independently racing Client.userID's unsynchronized
// cache via its own GetCurrentUserID call — see runSourcesConcurrently's doc
// comment for the race this closes.
//
// The notification source must see every repository, so the allow-list is
// never applied here.
func (mc *MultiClient) ListPullRequestsAsReviewerForUser(userID string, top int) ([]PullRequest, error) {
	return mc.listPullRequestsAsReviewerFiltered(userID, top, true)
}

// ListWorkItems fetches work items from all projects concurrently,
// tags each with ProjectName, merges and sorts by ChangedDate descending.
func (mc *MultiClient) ListWorkItems(top int) ([]WorkItem, error) {
	type result struct {
		project string
		items   []WorkItem
		err     error
	}

	var wg sync.WaitGroup
	ch := make(chan result, len(mc.clients))

	for project, client := range mc.clients {
		wg.Add(1)
		go func(p string, c *Client) {
			defer wg.Done()
			items, err := c.ListWorkItems(top)
			ch <- result{p, items, err}
		}(project, client)
	}

	go func() {
		wg.Wait()
		close(ch)
	}()

	var allItems []WorkItem
	var errs []error
	for r := range ch {
		if r.err != nil {
			errs = append(errs, r.err)
			continue
		}
		for i := range r.items {
			r.items[i].ProjectName = r.project
			r.items[i].ProjectDisplayName = mc.DisplayNameFor(r.project)
		}
		allItems = append(allItems, r.items...)
	}

	if len(errs) == len(mc.clients) {
		return nil, fmt.Errorf("all projects failed: %v", errs)
	}

	sort.Slice(allItems, func(i, j int) bool {
		return allItems[i].Fields.ChangedDate.After(allItems[j].Fields.ChangedDate)
	})

	if len(errs) > 0 {
		return allItems, &PartialError{Failed: len(errs), Total: len(mc.clients), Errors: errs}
	}

	return allItems, nil
}

// ListRecentlyAssignedWorkItems fetches work items assigned to the
// authenticated user (@Me) that changed within the last lookbackDays days
// from all projects concurrently, tags each with ProjectName, merges and
// sorts by ChangedDate descending. Backs the "recently assigned work items"
// notification source (task 6 of the phase-2 notifications spec, see
// SourceAssigned in notifications_source_assigned.go and
// Client.ListRecentlyAssignedWorkItems for the query itself).
func (mc *MultiClient) ListRecentlyAssignedWorkItems(lookbackDays, top int) ([]WorkItem, error) {
	type result struct {
		project string
		items   []WorkItem
		err     error
	}

	var wg sync.WaitGroup
	ch := make(chan result, len(mc.clients))

	for project, client := range mc.clients {
		wg.Add(1)
		go func(p string, c *Client) {
			defer wg.Done()
			items, err := c.ListRecentlyAssignedWorkItems(lookbackDays, top)
			ch <- result{p, items, err}
		}(project, client)
	}

	go func() {
		wg.Wait()
		close(ch)
	}()

	var allItems []WorkItem
	var errs []error
	for r := range ch {
		if r.err != nil {
			errs = append(errs, r.err)
			continue
		}
		for i := range r.items {
			r.items[i].ProjectName = r.project
			r.items[i].ProjectDisplayName = mc.DisplayNameFor(r.project)
		}
		allItems = append(allItems, r.items...)
	}

	if len(errs) == len(mc.clients) {
		return nil, fmt.Errorf("all projects failed: %v", errs)
	}

	sort.Slice(allItems, func(i, j int) bool {
		return allItems[i].Fields.ChangedDate.After(allItems[j].Fields.ChangedDate)
	})

	if len(errs) > 0 {
		return allItems, &PartialError{Failed: len(errs), Total: len(mc.clients), Errors: errs}
	}

	return allItems, nil
}

// MetricsWorkItems fetches the org-wide metrics dataset (configured workflow
// states plus items closed on or after `since`) from all projects
// concurrently, tags each with ProjectName, merges and sorts by ChangedDate
// descending.
func (mc *MultiClient) MetricsWorkItems(since time.Time, states MetricsStateNames) ([]WorkItem, error) {
	type result struct {
		project string
		items   []WorkItem
		err     error
	}

	var wg sync.WaitGroup
	ch := make(chan result, len(mc.clients))

	for project, client := range mc.clients {
		wg.Add(1)
		go func(p string, c *Client) {
			defer wg.Done()
			items, err := c.MetricsWorkItems(since, states)
			ch <- result{p, items, err}
		}(project, client)
	}

	go func() {
		wg.Wait()
		close(ch)
	}()

	var allItems []WorkItem
	var errs []error
	for r := range ch {
		if r.err != nil {
			errs = append(errs, r.err)
			continue
		}
		for i := range r.items {
			r.items[i].ProjectName = r.project
			r.items[i].ProjectDisplayName = mc.DisplayNameFor(r.project)
		}
		allItems = append(allItems, r.items...)
	}

	if len(errs) == len(mc.clients) {
		return nil, fmt.Errorf("all projects failed: %v", errs)
	}

	sort.Slice(allItems, func(i, j int) bool {
		return allItems[i].Fields.ChangedDate.After(allItems[j].Fields.ChangedDate)
	})

	if len(errs) > 0 {
		return allItems, &PartialError{Failed: len(errs), Total: len(mc.clients), Errors: errs}
	}

	return allItems, nil
}

// ListMyWorkItems fetches work items assigned to the authenticated user (@Me)
// from all projects concurrently, tags each with ProjectName, merges and sorts
// by ChangedDate descending.
func (mc *MultiClient) ListMyWorkItems(top int) ([]WorkItem, error) {
	type result struct {
		project string
		items   []WorkItem
		err     error
	}

	var wg sync.WaitGroup
	ch := make(chan result, len(mc.clients))

	for project, client := range mc.clients {
		wg.Add(1)
		go func(p string, c *Client) {
			defer wg.Done()
			items, err := c.ListMyWorkItems(top)
			ch <- result{p, items, err}
		}(project, client)
	}

	go func() {
		wg.Wait()
		close(ch)
	}()

	var allItems []WorkItem
	var errs []error
	for r := range ch {
		if r.err != nil {
			errs = append(errs, r.err)
			continue
		}
		for i := range r.items {
			r.items[i].ProjectName = r.project
			r.items[i].ProjectDisplayName = mc.DisplayNameFor(r.project)
		}
		allItems = append(allItems, r.items...)
	}

	if len(errs) == len(mc.clients) {
		return nil, fmt.Errorf("all projects failed: %v", errs)
	}

	sort.Slice(allItems, func(i, j int) bool {
		return allItems[i].Fields.ChangedDate.After(allItems[j].Fields.ChangedDate)
	})

	if len(errs) > 0 {
		return allItems, &PartialError{Failed: len(errs), Total: len(mc.clients), Errors: errs}
	}

	return allItems, nil
}
