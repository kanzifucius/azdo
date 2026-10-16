package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

func TestParseProjectEntries_ReposOnObjectEntry(t *testing.T) {
	raw := []interface{}{
		"plain-project",
		map[string]interface{}{
			"name":         "filtered",
			"display_name": "Filtered",
			"repos":        []interface{}{" platform-api ", "Platform-Web", "platform-web", "infra-*", 42},
		},
		map[interface{}]interface{}{
			"name":  "legacy-map",
			"repos": []interface{}{"billing"},
		},
	}
	projects, displayNames, repoFilters := parseProjectEntries(raw)

	if len(projects) != 3 {
		t.Fatalf("projects = %v, want 3 entries", projects)
	}
	if displayNames["filtered"] != "Filtered" {
		t.Errorf("displayNames = %v", displayNames)
	}
	got := repoFilters["filtered"]
	want := []string{"platform-api", "Platform-Web", "infra-*"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("repoFilters[filtered] = %v, want %v (trimmed, case-insensitively deduplicated, non-strings skipped)", got, want)
	}
	if strings.Join(repoFilters["legacy-map"], ",") != "billing" {
		t.Errorf("repoFilters[legacy-map] = %v", repoFilters["legacy-map"])
	}
	if _, ok := repoFilters["plain-project"]; ok {
		t.Errorf("plain string entry must not appear in repoFilters")
	}
}

func TestParseProjectEntries_NoReposReturnsNilMap(t *testing.T) {
	raw := []interface{}{
		"a",
		map[string]interface{}{"name": "b", "display_name": "B"},
		map[string]interface{}{"name": "c", "repos": []interface{}{}},
	}
	_, _, repoFilters := parseProjectEntries(raw)
	if repoFilters != nil {
		t.Errorf("repoFilters = %v, want nil when no project carries a non-empty repos list", repoFilters)
	}
}

func TestLoad_RepoFilter_OrganizationsShape(t *testing.T) {
	path := writeConfig(t, `organizations:
  - name: org-a
    projects:
      - project-one
      - name: ugly-api-name
        display_name: My Project
        repos:
          - platform-api
          - "infra-*"
  - name: org-b
    projects:
      - name: project-two
        repos: [billing]
polling_interval: 60
theme: dark
`)
	cfg, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}

	if !cfg.HasRepoFilter() {
		t.Fatal("HasRepoFilter() = false, want true")
	}
	if got := cfg.RepoFilterFor("ugly-api-name"); strings.Join(got, ",") != "platform-api,infra-*" {
		t.Errorf("RepoFilterFor(ugly-api-name) = %v", got)
	}
	if got := cfg.RepoFilterFor("project-two"); strings.Join(got, ",") != "billing" {
		t.Errorf("RepoFilterFor(project-two) = %v", got)
	}
	if got := cfg.RepoFilterFor("project-one"); got != nil {
		t.Errorf("RepoFilterFor(project-one) = %v, want nil (unfiltered)", got)
	}
	if got := cfg.RepoFilterFor("unknown"); got != nil {
		t.Errorf("RepoFilterFor(unknown) = %v, want nil", got)
	}
	// Deprecated first-org mirror stays consistent.
	if strings.Join(cfg.RepoFilters["ugly-api-name"], ",") != "platform-api,infra-*" {
		t.Errorf("legacy RepoFilters mirror = %v", cfg.RepoFilters)
	}
	if len(cfg.Warnings) != 0 {
		t.Errorf("unexpected warnings: %v", cfg.Warnings)
	}
}

func TestLoad_RepoFilter_LegacyShape(t *testing.T) {
	path := writeConfig(t, `organization: test-org
projects:
  - name: proj
    repos:
      - repo-a
  - other
polling_interval: 60
theme: dark
`)
	cfg, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}
	if got := cfg.RepoFilterFor("proj"); strings.Join(got, ",") != "repo-a" {
		t.Errorf("RepoFilterFor(proj) = %v", got)
	}
	if len(cfg.Organizations) != 1 || strings.Join(cfg.Organizations[0].RepoFilters["proj"], ",") != "repo-a" {
		t.Errorf("Organizations[0].RepoFilters = %v", cfg.Organizations)
	}
}

func TestLoad_RepoFilter_UnsetMeansAllRepos(t *testing.T) {
	path := writeConfig(t, `organization: test-org
projects:
  - proj
polling_interval: 60
theme: dark
`)
	cfg, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}
	if cfg.HasRepoFilter() {
		t.Error("HasRepoFilter() = true for a config without repos keys")
	}
	if cfg.RepoFilterFor("proj") != nil {
		t.Error("RepoFilterFor(proj) should be nil when unset")
	}
}

func TestLoad_RepoFilter_EmptyEntryIsError(t *testing.T) {
	path := writeConfig(t, `organizations:
  - name: org-a
    projects:
      - name: proj
        repos:
          - repo-a
          - "   "
polling_interval: 60
theme: dark
`)
	_, err := LoadFrom(path)
	if err == nil {
		t.Fatal("expected an error for an empty repos entry")
	}
	if !strings.Contains(err.Error(), "empty entry in its repos list") {
		t.Errorf("error = %q, want it to name the empty repos entry", err)
	}
}

func TestLoad_RepoFilter_MalformedGlobWarnsAndDrops(t *testing.T) {
	path := writeConfig(t, `organizations:
  - name: org-a
    projects:
      - name: proj
        repos:
          - "repo-["
          - good-repo
      - name: all-bad
        repos:
          - "[x"
polling_interval: 60
theme: dark
`)
	cfg, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}
	if got := cfg.RepoFilterFor("proj"); strings.Join(got, ",") != "good-repo" {
		t.Errorf("RepoFilterFor(proj) = %v, want the malformed glob dropped", got)
	}
	if got := cfg.RepoFilterFor("all-bad"); got != nil {
		t.Errorf("RepoFilterFor(all-bad) = %v, want nil once every entry was dropped", got)
	}
	if len(cfg.Warnings) != 2 {
		t.Fatalf("warnings = %v, want exactly two", cfg.Warnings)
	}
	if !strings.Contains(cfg.Warnings[0], "organizations[0].projects[0].repos") || !strings.Contains(cfg.Warnings[0], "the remaining entries still apply") {
		t.Errorf("warning[0] = %q", cfg.Warnings[0])
	}
	if !strings.Contains(cfg.Warnings[1], "organizations[0].projects[1].repos") || !strings.Contains(cfg.Warnings[1], "every repository in the project is shown") {
		t.Errorf("warning[1] = %q", cfg.Warnings[1])
	}
}

func TestConfigSave_RepoFilter_RoundTripsBothShapes(t *testing.T) {
	for _, tc := range []struct {
		name    string
		content string
		topKey  string
	}{
		{
			name: "organizations",
			content: `organizations:
  - name: org-a
    projects:
      - plain
      - name: proj
        display_name: Proj
        repos: [repo-a, repo-b]
polling_interval: 60
theme: dark
`,
			topKey: "organizations",
		},
		{
			name: "legacy",
			content: `organization: org-a
projects:
  - plain
  - name: proj
    repos: [repo-a, repo-b]
polling_interval: 60
theme: dark
`,
			topKey: "organization",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := writeConfig(t, tc.content)
			cfg, err := LoadFrom(path)
			if err != nil {
				t.Fatalf("LoadFrom: %v", err)
			}
			if err := cfg.Save(); err != nil {
				t.Fatalf("Save: %v", err)
			}

			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var saved map[string]interface{}
			if err := yaml.Unmarshal(data, &saved); err != nil {
				t.Fatalf("saved file is not YAML: %v", err)
			}
			if _, ok := saved[tc.topKey]; !ok {
				t.Fatalf("saved file lost its %q key: %s", tc.topKey, data)
			}
			if !strings.Contains(string(data), "repo-a") || !strings.Contains(string(data), "repos:") {
				t.Errorf("saved file does not carry the repos list:\n%s", data)
			}

			reloaded, err := LoadFrom(path)
			if err != nil {
				t.Fatalf("reload: %v", err)
			}
			if got := reloaded.RepoFilterFor("proj"); strings.Join(got, ",") != "repo-a,repo-b" {
				t.Errorf("after round trip RepoFilterFor(proj) = %v", got)
			}
			if reloaded.RepoFilterFor("plain") != nil {
				t.Errorf("plain project gained a repo filter after round trip")
			}
		})
	}
}

func TestConfigSave_NoReposKeyWhenUnset(t *testing.T) {
	path := writeConfig(t, `organization: org-a
projects:
  - plain
polling_interval: 60
theme: dark
`)
	cfg, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}
	if err := cfg.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	data, _ := os.ReadFile(path)
	if strings.Contains(string(data), "repos") {
		t.Errorf("Save wrote a repos key for an unfiltered config:\n%s", data)
	}
}

func TestConfig_HasRepoFilter_LegacyFieldsOnly(t *testing.T) {
	cfg := Config{
		Organization: "org",
		Projects:     []string{"proj"},
		RepoFilters:  map[string][]string{"proj": {"a"}},
	}
	if !cfg.HasRepoFilter() {
		t.Error("HasRepoFilter() = false for legacy-only fields")
	}
	if got := cfg.RepoFilterFor("proj"); strings.Join(got, ",") != "a" {
		t.Errorf("RepoFilterFor(proj) = %v", got)
	}
	cfg.ensureOrganizationsFromLegacy()
	if strings.Join(cfg.Organizations[0].RepoFilters["proj"], ",") != "a" {
		t.Errorf("ensureOrganizationsFromLegacy dropped RepoFilters: %v", cfg.Organizations)
	}
}
