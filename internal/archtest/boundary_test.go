// Package archtest enforces package dependency rules from docs/ARCHITECTURE.md.
package archtest

import (
	"bufio"
	"bytes"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const module = "github.com/MichaelYagi/cockpit"

// touchSources opens every source directory and stats every Go file and
// go.mod. The test inspects them through "go list", which the test cache
// cannot see; touching them here makes cached results invalidate when any
// import changes.
func touchSources(t *testing.T, root string) {
	t.Helper()
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			name := d.Name()
			if path != root && (strings.HasPrefix(name, ".") || name == "testdata" || name == "bin") {
				return filepath.SkipDir
			}
			_, err := os.ReadDir(path)
			return err
		}
		if strings.HasSuffix(path, ".go") || d.Name() == "go.mod" {
			_, err := os.Stat(path)
			return err
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// deps returns, for each package matching pattern, its transitive
// module-internal dependencies (test-only imports excluded).
func deps(t *testing.T, pattern string) map[string][]string {
	t.Helper()
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go command not available")
	}
	touchSources(t, "../..")
	cmd := exec.Command(goBin, "list", "-deps=false", "-f", `{{.ImportPath}} {{join .Deps " "}}`, module+"/"+pattern)
	cmd.Dir = "../.."
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list %s: %v\n%s", pattern, err, stderr.String())
	}
	result := map[string][]string{}
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) == 0 {
			continue
		}
		var internal []string
		for _, d := range fields[1:] {
			if strings.HasPrefix(d, module+"/") {
				internal = append(internal, strings.TrimPrefix(d, module+"/"))
			}
		}
		result[strings.TrimPrefix(fields[0], module+"/")] = internal
	}
	if len(result) == 0 {
		t.Fatalf("pattern %s matched no packages", pattern)
	}
	return result
}

// rule fails if any package matching pattern depends on a module package
// that is not allowed.
func rule(t *testing.T, pattern string, allowed func(dep string) bool) {
	t.Helper()
	for pkg, ds := range deps(t, pattern) {
		for _, d := range ds {
			if !allowed(d) {
				t.Errorf("%s must not depend on %s", pkg, d)
			}
		}
	}
}

func hasPrefix(prefixes ...string) func(string) bool {
	return func(d string) bool {
		for _, p := range prefixes {
			if d == p || strings.HasPrefix(d, p+"/") {
				return true
			}
		}
		return false
	}
}

// The TUI is an API client (D2): only pkg/client and pkg/apiv1.
func TestTUIUsesOnlyTheAPI(t *testing.T) {
	rule(t, "internal/tui/...", hasPrefix("internal/tui", "pkg/client", "pkg/apiv1"))
}

// Public packages never expose internals.
func TestPkgDoesNotImportInternal(t *testing.T) {
	rule(t, "pkg/...", hasPrefix("pkg"))
}

// The domain model depends on nothing else in the module.
func TestCoreIsALeaf(t *testing.T) {
	rule(t, "internal/core/...", func(string) bool { return false })
}

// Collectors see only the contract and the domain.
func TestPlatformDependsOnContractsOnly(t *testing.T) {
	rule(t, "internal/platform/...", hasPrefix("internal/platform", "internal/collector", "internal/core"))
}

// The API never reaches collectors, platforms or storage directly.
func TestAPIDoesNotReachCollectors(t *testing.T) {
	forbidden := hasPrefix("internal/platform", "internal/collector", "internal/storage", "internal/engine", "internal/app", "internal/cli")
	rule(t, "internal/api/...", func(d string) bool { return !forbidden(d) })
}

// The engine does not know about HTTP or the API.
func TestEngineHasNoHTTP(t *testing.T) {
	rule(t, "internal/engine/...", hasPrefix("internal/core", "internal/collector"))
}

// Auth has no HTTP or storage implementation dependencies.
func TestAuthIsIndependent(t *testing.T) {
	rule(t, "internal/auth/...", hasPrefix("internal/core"))
}

// Health evaluates snapshots through an interface; it never reaches the
// engine, collectors or the API.
func TestHealthDependsOnDomainOnly(t *testing.T) {
	rule(t, "internal/health/...", hasPrefix("internal/core"))
}

// Events are a leaf service.
func TestEventsDependsOnDomainOnly(t *testing.T) {
	rule(t, "internal/events/...", hasPrefix("internal/core", "internal/ulid"))
}

// Storage implements interfaces owned by domain packages.
func TestStorageDependsOnDomainOnly(t *testing.T) {
	rule(t, "internal/storage/...", hasPrefix("internal/core", "internal/auth", "internal/history", "internal/events", "internal/ulid", "internal/settings", "internal/config", "internal/netaddr"))
}

// History records snapshots through an interface; it never reaches the
// engine, collectors, storage implementation or the API.
func TestHistoryDependsOnDomainOnly(t *testing.T) {
	rule(t, "internal/history/...", hasPrefix("internal/core"))
}

// Settings validates through config and notifies through a callback; it
// never reaches the engine, health, storage implementation or the API.
func TestSettingsDependencies(t *testing.T) {
	rule(t, "internal/settings/...", hasPrefix("internal/core", "internal/config", "internal/netaddr"))
}

// Reports are post-processed from wire types alone (D21), so the same
// code serves the API and any client.
func TestReportUsesWireTypesOnly(t *testing.T) {
	rule(t, "internal/report/...", hasPrefix("pkg/apiv1"))
}

// Extension collectors reach the agent only through the public SDK and
// the adapter (D23).
func TestExtensionDependencies(t *testing.T) {
	rule(t, "internal/extension/...", hasPrefix("internal/collector", "internal/core", "pkg/collectorsdk"))
	rule(t, "pkg/collectorsdk/...", func(string) bool { return false })
}
