package analysis

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/example/mutation-judge/internal/config"
	"github.com/example/mutation-judge/internal/model"
	"github.com/example/mutation-judge/internal/runner"
)

// This is the re-audit's executable false-kill fixture. The imported module
// writes beside its source via runtime.Caller. Previously the baseline left
// a marker in the *host* replacement directory; the later boundary mutant
// was then falsely KILLED. The same fixture catches sandbox reuse between
// mutants, reuse between coverage-profiling runs, and worker reuse.
func TestR01ExternalLocalReplacementIsolatedAcrossExecutions(t *testing.T) {
	for _, target := range []string{"absolute", "relative"} {
		for _, workers := range []int{1, 2} {
			for _, profiling := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/workers=%d/profiling=%t", target, workers, profiling), func(t *testing.T) {
					parent := t.TempDir()
					mainRoot := filepath.Join(parent, "main")
					depRoot := filepath.Join(parent, "dep")
					write := func(path, contents string) {
						t.Helper()
						if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
							t.Fatal(err)
						}
						if err := os.WriteFile(path, []byte(contents), 0644); err != nil {
							t.Fatal(err)
						}
					}
					write(filepath.Join(depRoot, "go.mod"), "module example.test/localdep\n\ngo 1.22\n")
					depSource := `package localdep
import ("os"; "path/filepath"; "runtime")
func AlreadyRan() bool {
 _, filename, _, _ := runtime.Caller(0)
 marker := filepath.Join(filepath.Dir(filename), "marker.test")
 if _, err := os.Stat(marker); err == nil { return true }
 if err := os.WriteFile(marker, []byte("written"), 0600); err != nil { panic(err) }
 return false
}
`
					write(filepath.Join(depRoot, "dep.go"), depSource)
					path := filepath.ToSlash(depRoot)
					if target == "relative" {
						path = "../dep"
					}
					write(filepath.Join(mainRoot, "go.mod"), "module example.test/main\n\ngo 1.22\n\nrequire example.test/localdep v0.0.0\nreplace example.test/localdep => "+path+"\n")
					write(filepath.Join(mainRoot, "p.go"), `package p
func A(x int) bool { return x > 0 }
func B(x int) bool { return x > 1 }
func C(x int) bool { return x > 2 }
`)
					write(filepath.Join(mainRoot, "p_test.go"), `package p
import ("testing"; "example.test/localdep")
func TestWeak(t *testing.T) {
 if localdep.AlreadyRan() { t.Fatal("external fixture contaminated") }
 if !A(4) || !B(4) || !C(4) { t.Fatal("positive") }
}
`)
					cfg := config.Default()
					cfg.Cache = false
					cfg.Operators = []string{"boundary"}
					cfg.Workers = workers
					cfg.CoverageTestSelection = profiling
					cfg.CacheDir = filepath.Join(mainRoot, ".mutation-judge", "cache")
					report, err := (Engine{Version: "r01-test", Backend: runner.GoTest{}}).Analyze(context.Background(), Request{CWD: mainRoot, Patterns: []string{"."}, Config: cfg})
					if err != nil {
						t.Fatal(err)
					}
					if !report.Complete || len(report.Results) != 3 {
						t.Fatalf("expected three complete mutants: %+v", report)
					}
					for _, r := range report.Results {
						if r.Verdict != model.VerdictSurvived {
							t.Errorf("%s: %s (false kill from external replacement?): %s", r.Mutation.ID, r.Verdict, r.Output)
						}
					}
					if _, err := os.Stat(filepath.Join(depRoot, "marker.test")); !os.IsNotExist(err) {
						t.Errorf("host replacement was modified: %v", err)
					}
					if bytes, err := os.ReadFile(filepath.Join(depRoot, "dep.go")); err != nil || string(bytes) != depSource {
						t.Errorf("host dependency source was modified: %v", err)
					}
					mod, err := os.ReadFile(filepath.Join(mainRoot, "go.mod"))
					if err != nil || !strings.Contains(string(mod), path) {
						t.Errorf("host go.mod was rewritten: %s: %v", mod, err)
					}
				})
			}
		}
	}
}

func TestR01AbsoluteInModuleReplacementCannotEscapeSandbox(t *testing.T) {
	parent := t.TempDir()
	write := func(name, content string) {
		t.Helper()
		dest := filepath.Join(parent, name)
		if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(dest, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	dep := filepath.Join(parent, "dep")
	write("dep/go.mod", "module example.test/internaldep\n\ngo 1.22\n")
	write("dep/dep.go", `package internaldep
import ("os"; "path/filepath"; "runtime")
func WasRun() bool {
 _, filename, _, _ := runtime.Caller(0)
 marker := filepath.Join(filepath.Dir(filename), "marker.test")
 if _, err := os.Stat(marker); err == nil { return true }
 if err := os.WriteFile(marker, []byte("yes"), 0600); err != nil { panic(err) }
 return false
}`)
	write("go.mod", "module example.test/main\n\ngo 1.22\nrequire example.test/internaldep v0.0.0\nreplace example.test/internaldep => "+filepath.ToSlash(dep)+"\n")
	write("p.go", "package p\nfunc F(x int) bool { return x > 0 }\n")
	write("p_test.go", `package p
import ("testing"; "example.test/internaldep")
func TestWeak(t *testing.T) { if internaldep.WasRun() { t.Fatal("dirty") }; if !F(1) { t.Fatal("positive") } }
`)
	cfg := config.Default()
	cfg.Cache = false
	cfg.Operators = []string{"boundary"}
	report, err := (Engine{Version: "r01-test", Backend: runner.GoTest{}}).Analyze(context.Background(), Request{CWD: parent, Patterns: []string{"."}, Config: cfg})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Results) != 1 || report.Results[0].Verdict != model.VerdictSurvived {
		t.Fatalf("unexpected verdicts: %+v", report.Summary)
	}
	if _, err := os.Stat(filepath.Join(dep, "marker.test")); !os.IsNotExist(err) {
		t.Errorf("wrote through absolute in-module replacement: %v", err)
	}
}

// A replacement can be version-qualified and imported indirectly through
// another local replacement. Both targets must be copied and rewritten in the
// MAIN go.mod, which is the only go.mod whose replace directives Go honors.
func TestR01VersionedAndTransitiveExternalReplacements(t *testing.T) {
	parent := t.TempDir()
	write := func(path, content string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	a := filepath.Join(parent, "a")
	b := filepath.Join(parent, "b")
	main := filepath.Join(parent, "main")
	write(filepath.Join(b, "go.mod"), "module example.test/b\n\ngo 1.22\n")
	write(filepath.Join(b, "b.go"), `package b
import ("os"; "path/filepath"; "runtime")
func Check() bool {
 _, path, _, _ := runtime.Caller(0)
 marker := filepath.Join(filepath.Dir(path), "marker")
 if _, err := os.Stat(marker); err == nil { return true }
 if err := os.WriteFile(marker, []byte("marked"), 0600); err != nil { panic(err) }
 return false
}`)
	write(filepath.Join(a, "go.mod"), "module example.test/a\n\ngo 1.22\nrequire example.test/b v0.0.0\n")
	write(filepath.Join(a, "a.go"), "package a\nimport \"example.test/b\"\nfunc Check() bool { return b.Check() }\n")
	write(filepath.Join(main, "go.mod"), "module example.test/main\n\ngo 1.22\nrequire (\nexample.test/a v0.0.0\nexample.test/b v0.0.0\n)\nreplace example.test/a v0.0.0 => ../a\nreplace example.test/b => ../b\n")
	write(filepath.Join(main, "p.go"), "package p\nfunc F(x int) bool { return x > 0 }\n")
	write(filepath.Join(main, "p_test.go"), `package p
import ("testing"; "example.test/a")
func TestWeak(t *testing.T) { if a.Check() { t.Fatal("shared dependency") }; if !F(1) { t.Fatal("positive") } }
`)
	cfg := config.Default()
	cfg.Cache = false
	cfg.Operators = []string{"boundary"}
	report, err := (Engine{Version: "r01-test", Backend: runner.GoTest{}}).Analyze(context.Background(), Request{CWD: main, Patterns: []string{"."}, Config: cfg})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Results) != 1 || report.Results[0].Verdict != model.VerdictSurvived {
		t.Fatalf("unexpected verdict: %+v", report.Summary)
	}
	if _, err := os.Stat(filepath.Join(b, "marker")); !os.IsNotExist(err) {
		t.Fatalf("transitive dependency leaked to host: %v", err)
	}
}
