package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/Elpulgo/azdo/internal/azdevops"
)

// TestRunTUI_WiresRepoFilters pins, via the AST, that runTUI installs each
// organisation's repos allow-list on its MultiClient (SetRepoFilters), resolves
// the filters through resolveRepoFilters, and hands the warnings to the model
// (SetRepoFilterWarnings). Without all three a configured repos key would be
// parsed and validated, then silently ignored.
func TestRunTUI_WiresRepoFilters(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("failed to resolve test file path via runtime.Caller")
	}
	mainFile := filepath.Join(filepath.Dir(thisFile), "main.go")

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, mainFile, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", mainFile, err)
	}

	var sawSetRepoFilters, sawResolveRepoFilters, sawSetRepoFilterWarnings bool
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if ident, ok := call.Fun.(*ast.Ident); ok && ident.Name == "resolveRepoFilters" {
			sawResolveRepoFilters = true
		}
		if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
			switch sel.Sel.Name {
			case "SetRepoFilters":
				sawSetRepoFilters = true
			case "SetRepoFilterWarnings":
				sawSetRepoFilterWarnings = true
			}
		}
		return true
	})

	if !sawSetRepoFilters {
		t.Error("runTUI must call SetRepoFilters on each organisation's MultiClient")
	}
	if !sawResolveRepoFilters {
		t.Error("runTUI must resolve the filters eagerly via resolveRepoFilters")
	}
	if !sawSetRepoFilterWarnings {
		t.Error("runTUI must hand resolution warnings to the model via SetRepoFilterWarnings")
	}
}

// newRepoListingServer serves /git/repositories with the given names and
// records how many times it was asked.
func newRepoListingServer(t *testing.T, names []string, hits *int) *httptest.Server {
	t.Helper()
	var items []string
	for _, n := range names {
		items = append(items, fmt.Sprintf(`{"id":"id-%s","name":"%s"}`, n, n))
	}
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/git/repositories") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		*hits++
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"count":%d,"value":[%s]}`, len(items), strings.Join(items, ","))
	}))
	t.Cleanup(s.Close)
	return s
}

// newTestMultiClient builds a MultiClient whose single project client points
// at serverURL, the same way internal/demo redirects its clients.
func newTestMultiClient(t *testing.T, org, project, serverURL string) *azdevops.MultiClient {
	t.Helper()
	mc, err := azdevops.NewMultiClient(org, []string{project}, "pat", nil)
	if err != nil {
		t.Fatal(err)
	}
	mc.ClientFor(project).SetBaseURL(serverURL)
	return mc
}

func TestResolveRepoFilters_MergesWarningsAcrossOrganisations(t *testing.T) {
	if got := resolveRepoFilters(nil); got != nil {
		t.Fatalf("no clients → nil warnings, got %v", got)
	}

	var hitsA, hitsB int
	serverA := newRepoListingServer(t, []string{"frontend", "backend"}, &hitsA)
	serverB := newRepoListingServer(t, []string{"billing"}, &hitsB)

	orgA := newTestMultiClient(t, "org-a", "alpha", serverA.URL)
	orgA.SetRepoFilters(map[string][]string{"alpha": {"frontend", "typo"}})
	orgB := newTestMultiClient(t, "org-b", "beta", serverB.URL)
	orgB.SetRepoFilters(map[string][]string{"beta": {"billing"}})

	warnings := resolveRepoFilters([]*azdevops.MultiClient{orgA, orgB})
	if len(warnings) != 1 || !strings.Contains(warnings[0], `project "alpha": repos entry "typo"`) {
		t.Errorf("warnings = %v", warnings)
	}
	if hitsA != 1 || hitsB != 1 {
		t.Errorf("each filtered project should be listed exactly once, got a=%d b=%d", hitsA, hitsB)
	}
}
