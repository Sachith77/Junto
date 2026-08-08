package domain

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// The architectural claim this project makes is "Clean Architecture separating domain logic
// from transport". That claim is only worth making if something enforces it. Code review
// does not scale, and the violation that matters — someone importing pgx or net/http into
// the domain because it was convenient — looks entirely reasonable in a diff.
//
// So it is a test. CI fails on it like any other failure.

// allowedNonStdImports is the complete set of third-party imports the domain layer may use.
//
// Adding to this list should feel uncomfortable. Each entry needs to be a leaf value-type
// library with no I/O, no framework behaviour, and no transitive dependencies — otherwise
// the domain is no longer independent of infrastructure, it just looks like it is.
var allowedNonStdImports = map[string]string{
	"github.com/google/uuid": "leaf value-type library: no I/O, no transitive deps (see ids.go)",
}

// allowedInternalPrefixes are module-internal packages the domain may import. pkg/ is
// dependency-free by construction; internal/repository, internal/service and
// internal/transport are all outside the domain and must never appear here.
var allowedInternalPrefixes = []string{
	"github.com/junto/junto/pkg/",
}

// bannedSubstrings catch the specific mistakes this architecture exists to prevent, even
// if a future allowlist entry would otherwise permit them.
var bannedSubstrings = map[string]string{
	"net/http":                                   "the domain must not know about HTTP",
	"database/sql":                               "the domain must not know about SQL",
	"github.com/jackc/pgx":                       "the domain must not know about Postgres",
	"github.com/go-chi":                          "the domain must not know about the HTTP router",
	"github.com/gorilla/websocket":               "the domain must not know about WebSockets",
	"github.com/coder/websocket":                 "the domain must not know about WebSockets",
	"github.com/redis":                           "the domain must not know about Redis",
	"github.com/golang-jwt":                      "JWT is a transport-layer credential format",
	"github.com/junto/junto/internal/repository": "inner layers must not depend on outer layers",
	"github.com/junto/junto/internal/service":    "inner layers must not depend on outer layers",
	"github.com/junto/junto/internal/transport":  "inner layers must not depend on outer layers",
}

func TestDomainImportsAreClean(t *testing.T) {
	fset := token.NewFileSet()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("reading domain package directory: %v", err)
	}

	checked := 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		checked++

		file, err := parser.ParseFile(fset, name, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parsing %s: %v", name, err)
		}

		for _, spec := range file.Imports {
			path, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				t.Fatalf("%s: unquoting import %s: %v", name, spec.Path.Value, err)
			}

			for banned, why := range bannedSubstrings {
				if strings.Contains(path, banned) {
					t.Errorf("%s imports %q — %s", name, path, why)
				}
			}

			if isStdlib(path) {
				continue
			}
			if _, ok := allowedNonStdImports[path]; ok {
				continue
			}
			if hasAnyPrefix(path, allowedInternalPrefixes) {
				continue
			}

			t.Errorf("%s imports %q, which is not in the domain allowlist.\n"+
				"The domain layer may import the standard library, %v, and junto/pkg/... only.\n"+
				"If this dependency is genuinely a leaf value type, add it to allowedNonStdImports "+
				"with a justification. If it is infrastructure, it belongs in repository/ or service/.",
				name, path, keys(allowedNonStdImports))
		}
	}

	if checked == 0 {
		t.Fatal("no domain source files were checked — the test is not actually running")
	}
	t.Logf("checked imports of %d domain source files", checked)
}

// TestDomainHasNoSubdirectories keeps the domain flat, so the import check above cannot be
// bypassed by tucking an infrastructure-dependent file into a nested package.
func TestDomainHasNoSubdirectories(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("reading domain package directory: %v", err)
	}
	for _, e := range entries {
		if e.IsDir() && !strings.HasPrefix(e.Name(), ".") && e.Name() != "testdata" {
			t.Errorf("unexpected subdirectory %q in internal/domain: the arch check only scans "+
				"the top level, so nested packages would escape it", e.Name())
		}
	}
}

// isStdlib reports whether an import path belongs to the standard library.
//
// The heuristic is the same one the go tool uses: standard library paths never have a dot
// in their first segment, because that segment would have to be a domain name.
func isStdlib(path string) bool {
	first, _, _ := strings.Cut(path, "/")
	return !strings.Contains(first, ".")
}

func hasAnyPrefix(s string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

func keys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestArchTestSeesRealFiles guards against the check silently passing because it is looking
// in the wrong place — the classic way an architecture test becomes decorative.
func TestArchTestSeesRealFiles(t *testing.T) {
	for _, required := range []string{"ports.go", "slot.go", "option.go", "permission.go"} {
		if _, err := os.Stat(filepath.Join(".", required)); err != nil {
			t.Errorf("expected domain file %s to exist: %v", required, err)
		}
	}
}
