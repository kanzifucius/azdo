# 0003. Multi-organisation Azure scope routing

**Date:** 2026-10-14
**Status:** Accepted

## Context

azdo needs to support several Azure DevOps organisations at once. The existing `provider.CompositeProvider` already fans list calls out across N backends and routes detail/mutation/URL calls through a `scope → backend` map built from each backend's `Scopes()`. The natural design is one `azdevops.Adapter` (wrapping one org-scoped `MultiClient`) per configured organisation.

Azure scopes today are **bare project API names**. Two organisations that both contain a project named e.g. `Platform` would collide in the composite's routing map; first-registered-wins would silently send mutations to the wrong organisation.

## Decision

Keep bare project scopes. Reject cross-organisation duplicate project API names (case-insensitive) at config validation, with an error that names both organisations and the project.

Single-organisation and legacy `organization:`/`projects:` configs are unaffected. Multi-organisation configs use a new top-level `organizations:` list; the legacy keys remain supported and migrate into a one-entry `Organizations` slice on load.

## Alternatives considered

### Org-qualified scopes (`org/project`)

Correct for every collision, and mirrors GitHub's `owner/repo` scopes. Rejected for this iteration because `Identity.Scope` feeds state persistence (`state.DetailRef`), Azure notifications triage-store keys, notification include/exclude globs, and WIQL client lookup. Changing the spelling would invalidate persisted last-viewed state and resurface triaged notifications as unread. Revisit if the duplicate-project restriction bites real users.

## Consequences

- `Config.Validate()` enforces unique project names across organisations and unique organisation names.
- List fan-out and detail routing need no `CompositeProvider` changes.
- UI differentiation across organisations uses `ScopeDisplay` prefixed with the organisation name when more than one organisation is configured; `Scope` stays bare.
- Metrics remains bound to the first organisation until a separate fan-out is designed.
