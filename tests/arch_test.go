// Package tests holds checks that span the whole module rather than one package.
package tests

import (
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// The layering rules for "Clean Architecture separating domain logic from transport",
// enforced across every package rather than only inside internal/domain.
//
// internal/domain/arch_test.go checks the domain's own imports in detail (stdlib plus a
// justified allowlist). This checks the DIRECTION of dependencies between layers, and it is
// written to cover packages that do not exist yet: when internal/service and
// internal/transport arrive, their rules are already here and already failing if violated.
//
// The dependency rule: inner layers never import outer ones.
//
//	transport -> service -> domain <- repository
//	                          ^
//	                          |  (all arrows point inward)

const modulePath = "github.com/junto/junto"

// layerRules maps a package prefix to the module-internal prefixes it may NOT import.
var layerRules = []struct {
	layer     string
	forbidden []string
	why       string
}{
	{
		layer: "internal/domain",
		forbidden: []string{
			"internal/repository", "internal/service", "internal/transport",
			"internal/middleware", "internal/security", "configs", "migrations", "cmd",
		},
		why: "the domain is the innermost layer and must not know any outer layer exists",
	},
	{
		layer: "internal/service",
		forbidden: []string{
			"internal/transport", "internal/middleware", "cmd",
			// The critical one: services depend on domain PORTS, never on a concrete
			// Postgres implementation or a generated sqlc type. If this ever passes by
			// accident, the architecture claim is no longer true.
			"internal/repository",
			// A service importing the sync engine would recreate the construction cycle the
			// two-object split exists to avoid — and, worse, would invite op-log writing to
			// move out of the one place it is allowed to live. Services reach the engine
			// through domain.OpPublisher and nothing else.
			"internal/syncengine",
		},
		why: "services depend on domain interfaces, never on transport, a concrete repository, or the sync engine",
	},
	{
		layer: "internal/syncengine",
		forbidden: []string{
			// The engine calls SERVICES, never repositories directly. A repository call here
			// would bypass the trip sequencer and the operation log, so the change would
			// commit and then be invisible to every resyncing client — with every test still
			// green. This is the same rule, and the same reasoning, as transport -> repository.
			"internal/repository",
			"internal/transport", "internal/middleware", "cmd",
		},
		why: "the sync engine orchestrates services and owns no persistence and no transport",
	},
	{
		layer: "internal/repository",
		forbidden: []string{
			"internal/transport", "internal/middleware", "internal/service", "cmd",
		},
		why: "the repository layer implements domain ports; it must not reach upward or sideways",
	},
	{
		layer: "internal/transport",
		forbidden: []string{
			// The rule that matters most at this layer. A handler reaching a repository
			// directly would bypass the service layer, and in Stage 2 that means bypassing
			// the operation log — silently breaking resync while every test still passes.
			"internal/repository",
			"cmd",
		},
		why: "handlers decode, delegate to a service, and render; they never touch persistence",
	},
	{
		layer: "internal/middleware",
		forbidden: []string{
			"internal/repository", "internal/service", "internal/transport", "cmd",
		},
		why: "middleware depends on domain types and small local interfaces only; importing transport would also create an import cycle",
	},
	{
		layer: "internal/email",
		forbidden: []string{
			"internal/transport", "internal/middleware", "internal/service",
			"internal/repository", "cmd",
		},
		why: "email adapters implement a domain port and nothing more",
	},
	{
		layer: "internal/storage",
		forbidden: []string{
			"internal/transport", "internal/middleware", "internal/service",
			"internal/repository", "cmd",
		},
		why: "storage adapters implement domain.FileStorage; nothing above them may learn that storage is S3-shaped",
	},
	{
		layer: "internal/security",
		forbidden: []string{
			"internal/transport", "internal/middleware", "internal/service",
			"internal/repository", "cmd",
		},
		why: "security adapters implement domain ports and nothing more",
	},
	{
		layer: "pkg",
		forbidden: []string{
			"internal", "configs", "cmd",
		},
		why: "pkg is the lowest layer: self-contained utilities with no knowledge of the application",
	},
}

func TestLayerDependenciesPointInward(t *testing.T) {
	root := moduleRoot(t)
	fset := token.NewFileSet()
	checked := 0

	for _, rule := range layerRules {
		layerDir := filepath.Join(root, filepath.FromSlash(rule.layer))
		if _, err := os.Stat(layerDir); os.IsNotExist(err) {
			// Not built yet. Logged rather than skipped silently, so an empty run is
			// visible instead of looking like a pass.
			t.Logf("layer %s does not exist yet; its rules are staged and will apply on creation", rule.layer)
			continue
		}

		err := filepath.WalkDir(layerDir, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") {
				return nil
			}
			// Generated code is excluded: it is a build artifact of this layer, not a
			// design decision made in it.
			if strings.Contains(filepath.ToSlash(path), "/sqlcgen/") {
				return nil
			}
			// Test files are excluded. The dependency rule constrains what SHIPS: an
			// integration test that wires a real repository behind a real handler to
			// exercise the whole stack is doing its job, not violating the architecture.
			// Production code is what must not reach across layers.
			if strings.HasSuffix(path, "_test.go") {
				return nil
			}
			checked++

			file, parseErr := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
			if parseErr != nil {
				return parseErr
			}
			rel, _ := filepath.Rel(root, path)

			for _, spec := range file.Imports {
				imported, uErr := strconv.Unquote(spec.Path.Value)
				if uErr != nil {
					return uErr
				}
				if !strings.HasPrefix(imported, modulePath+"/") {
					continue // third-party and stdlib are the domain arch test's business
				}
				internalPath := strings.TrimPrefix(imported, modulePath+"/")

				for _, banned := range rule.forbidden {
					if internalPath == banned || strings.HasPrefix(internalPath, banned+"/") {
						t.Errorf("%s imports %s\n  rule: %s must not import %s\n  why:  %s",
							rel, internalPath, rule.layer, banned, rule.why)
					}
				}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walking %s: %v", rule.layer, err)
		}
	}

	if checked == 0 {
		t.Fatal("no source files were checked; the layering test is not actually running")
	}
	t.Logf("checked %d source files against %d layer rules", checked, len(layerRules))
}

// TestGeneratedCodeIsIsolated makes sure sqlc output cannot leak past the repository layer.
//
// This is the specific failure mode the mapping boilerplate exists to prevent: a sqlcgen
// struct returned from a repository method would put a generated persistence type into
// service signatures, and the "domain logic independent of the database" claim would quietly
// stop being true while everything still compiled.
func TestGeneratedCodeIsIsolated(t *testing.T) {
	root := moduleRoot(t)
	const generated = modulePath + "/internal/repository/sqlcgen"

	fset := token.NewFileSet()
	var offenders []string

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if name := d.Name(); name == ".git" || name == "node_modules" || name == "bin" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}

		rel, _ := filepath.Rel(root, path)
		relSlash := filepath.ToSlash(rel)
		// Only internal/repository itself may import the generated package.
		if strings.HasPrefix(relSlash, "internal/repository/") {
			return nil
		}

		file, parseErr := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if parseErr != nil {
			return parseErr
		}
		for _, spec := range file.Imports {
			imported, uErr := strconv.Unquote(spec.Path.Value)
			if uErr != nil {
				return uErr
			}
			if imported == generated {
				offenders = append(offenders, relSlash)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking module: %v", err)
	}

	for _, o := range offenders {
		t.Errorf("%s imports the generated sqlc package.\n"+
			"  Only internal/repository may do so. Map the generated row to a domain type\n"+
			"  in internal/repository/mapping.go and return that instead.", o)
	}
}

// transportBannedImports are the packages that would make the sync engine transport-aware.
//
// Matched as substrings so a version bump or a fork ("nhooyr.io/websocket",
// "github.com/coder/websocket/wsjson") cannot slip past an exact-match list.
var transportBannedImports = map[string]string{
	"websocket":    "the sync engine must not know how its operations reach a client",
	"net/http":     "the sync engine must not know about HTTP",
	"redis":        "fan-out transport belongs behind a port, not inside the engine",
	"grpc":         "the sync engine must not know about gRPC",
	"encoding/gob": "wire encoding is a transport concern",
}

// TestSyncEngineIsTransportAgnostic is the test that earns the resume clause
// "transport-agnostic WebSocket-based sync engine".
//
// The claim is only worth making if something enforces it, and the violation that would break
// it looks entirely reasonable in a diff: someone reaches for *websocket.Conn inside the
// engine because it was convenient at the time. After that the engine still works, every other
// test still passes, and the claim is quietly false.
//
// Verified to FAIL against planted `net/http` and `github.com/coder/websocket` imports in a
// syncengine file, and against a planted `internal/syncengine` import in internal/service.
// An architecture test that has never failed is decoration.
func TestSyncEngineIsTransportAgnostic(t *testing.T) {
	root := moduleRoot(t)
	engineDir := filepath.Join(root, filepath.FromSlash("internal/syncengine"))

	if _, err := os.Stat(engineDir); os.IsNotExist(err) {
		t.Fatal("internal/syncengine does not exist; this test is not actually checking anything")
	}

	fset := token.NewFileSet()
	checked := 0

	err := filepath.WalkDir(engineDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		// Unlike the layering rules, test files are NOT excluded. A test double that pulls a
		// real socket into this package is a signal the boundary is not actually usable
		// without one — which is the thing being claimed.
		checked++

		file, parseErr := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if parseErr != nil {
			return parseErr
		}
		rel, _ := filepath.Rel(root, path)

		for _, spec := range file.Imports {
			imported, uErr := strconv.Unquote(spec.Path.Value)
			if uErr != nil {
				return uErr
			}
			for banned, why := range transportBannedImports {
				if strings.Contains(imported, banned) {
					t.Errorf("%s imports %q\n  why: %s\n"+
						"  The engine speaks in domain types and the syncengine.Sink interface.\n"+
						"  A WebSocket connection implements Sink; so would SSE, a long poll, or a\n"+
						"  test double. Put the transport detail in internal/transport/ws instead.",
						filepath.ToSlash(rel), imported, why)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking internal/syncengine: %v", err)
	}

	if checked == 0 {
		t.Fatal("no syncengine source files were checked; the test is not actually running")
	}
	t.Logf("checked %d sync engine source files against %d banned transport imports",
		checked, len(transportBannedImports))
}

// moduleRoot walks up from the test's directory to the directory holding go.mod.
func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not locate go.mod above the test directory")
		}
		dir = parent
	}
}
