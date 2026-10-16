# 0004. Per-project Azure repository filter, resolved server-side

**Date:** 2026-10-16
**Status:** Accepted

## Context

An Azure DevOps project often holds many more Git repositories than a single user cares about. The Pull Requests and Pipelines panes list project-wide — `GET {project}/_apis/git/pullrequests` and `GET {project}/_apis/build/builds` — with a fixed recency window (`$top` of 25 pull requests and 30 builds per project). On a busy project the repositories a user actually works in can be crowded out of that window by noise from the rest of the project.

Users asked for a per-project allow-list of repositories, configured next to the project in `config.yaml`, with a quick way to see everything again. Work items are not repository-scoped and the Notifications feed already has its own include/exclude globs, so neither was in scope. GitHub's `github.repos` list already is the allow-list for that backend.

Two properties of the Azure REST API shaped the design:

- both list endpoints narrow by **a single repository ID** per request (`searchCriteria.repositoryId`; `repositoryId` + `repositoryType=TfsGit`), never by a list and never by name;
- `GET {project}/_apis/git/repositories` returns every repository's name and ID.

## Decision

1. **Allow-list, per project, under the project entry.** A `repos:` list on an object-form project entry, under both the `organizations:` and legacy `projects:` shapes. Entries are names matched case-insensitively, with `path.Match` globs accepted (the same sanitiser the notification globs use). Absence means "every repository", so existing configs are unchanged.

2. **Server-side, one request per allowed repository.** Configured names are resolved to IDs once per session through the repositories endpoint (`MultiClient.ResolveRepoFilters`, run eagerly and concurrently at start-up, retried lazily on the first filtered fetch if the listing failed). The filtered list variants then issue one narrowed request per resolved repository and concatenate, so **each allowed repository gets its own `$top` window**. The error unit stays the project: a repository-level failure fails that project, so `PartialError{Failed, Total}` and the existing partial-load warning are unchanged.

3. **Fail closed on a complete miss, warn on any miss.** An entry that matches no repository produces a warning naming the entry and the known repository names. A project whose entries all miss resolves to an empty set and shows nothing for that project — the typo is visible rather than silently widening to everything — and the warning says so. Warnings render as a banner above the two panes and persist while the filter is switched off, because the config problem does.

4. **Runtime toggle, persisted.** `R` on either pane flips `ListOpts.AllRepos` (zero value honours the filter), pushes it to both panes and the pipelines poller, refetches, and records `repo_filter_off: true` in `state.yaml`. The field is additive (no schema version bump) and honoured unconditionally on restore; a stale `true` from an earlier session therefore starts a newly added filter switched off, which the missing `Repos` pill and the `R repos` footer hint make visible. Documented rather than engineered around.

5. **Notifications untouched by construction.** The notification sources keep calling the unfiltered `MultiClient` methods, which never consult the allow-list. The feed stays governed by `notifications.include_repos` / `exclude_repos` alone.

## Alternatives considered

### Client-side filtering with over-fetch

Fetch project-wide with a larger `$top` and drop rows whose repository is not allowed. Rejected: no `$top` guarantees the allowed repositories make it into the window on a busy project — the exact failure the feature exists to fix — and the resolution step (name → ID) would still be needed to detect typos. The server-side approach costs `len(repos)` requests per filtered project per refresh, which is bounded by what the user opted into.

### Deny-list (`exclude_repos`) instead of, or alongside, the allow-list

A deny-list cannot be expressed to either API (both narrow to one repository, never "all but"), so it could only be a client-side post-filter with the over-fetch problem above. The Notifications feed offers both directions because it filters an already-fetched merged feed; the list panes do not. An allow-list also matches the GitHub backend's existing `github.repos` shape.

### Fail open on a complete miss

Showing every repository when no entry matches would hide a typo behind the behaviour the user had before adding the filter. Fail closed plus a banner plus `R` keeps the mistake visible and the data one keypress away.

### Resolving on every refresh

Would pick up repositories created mid-session but adds a listing request per filtered project per poll. Once per session, with restart picking up new repositories, was judged the right trade; the behaviour is documented.

## Consequences

- `Client` gains `ListRepositories`, `ListPullRequestsWithCriteria` (a `PullRequestCriteria` combining creator/reviewer/repository), and `ListPipelineRunsInRepository` / `ListActivePipelineRunsInRepository`; the existing named methods keep their exact request shapes.
- `MultiClient` gains `SetRepoFilters`, `ResolveRepoFilters`, `HasRepoFilter` and the four `*Filtered(top, allRepos)` pane methods; the unfiltered methods delegate with `allRepos=true`.
- `provider.ListOpts.AllRepos` and `provider.PipelineRun.RepositoryName` (mapped from the new `PipelineRun.Repository` wire field) are the only provider-level additions.
- Pipelines sourced from GitHub or other non-Azure-Repos repositories carry no TfsGit ID and are hidden while the filter applies. A `type/name` spelling for such entries is a possible follow-up.
- Request fan-out grows with the allow-list length; eager resolution adds one request per filtered project before the TUI starts.
