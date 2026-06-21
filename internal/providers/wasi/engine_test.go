package wasi

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// exit42.wasm is a 96-byte hand-encoded WASI module whose _start calls
// proc_exit(42). It needs no toolchain, so the core proofs always run.
func loadExit42(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "exit42.wasm"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return b
}

func TestRunReportsGuestExitCode(t *testing.T) {
	res, err := Run(context.Background(), RunSpec{Module: loadExit42(t)})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.ExitCode != 42 {
		t.Fatalf("ExitCode = %d, want 42", res.ExitCode)
	}
	if res.Calls == 0 {
		t.Fatal("Calls = 0, want a non-zero deterministic invocation count")
	}
}

// The gate property: identical module + inputs => identical output and an
// identical invocation count on every run.
func TestRunIsDeterministic(t *testing.T) {
	mod := loadExit42(t)
	first, err := Run(context.Background(), RunSpec{Module: mod})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		got, err := Run(context.Background(), RunSpec{Module: mod})
		if err != nil {
			t.Fatal(err)
		}
		if got.Calls != first.Calls || got.ExitCode != first.ExitCode || string(got.Stdout) != string(first.Stdout) {
			t.Fatalf("run %d diverged: calls %d!=%d exit %d!=%d", i, got.Calls, first.Calls, got.ExitCode, first.ExitCode)
		}
	}
}

func TestRunRejectsEmptyModule(t *testing.T) {
	if _, err := Run(context.Background(), RunSpec{}); err == nil {
		t.Fatal("Run accepted an empty module")
	}
}

// --- richer end-to-end proofs against a real Go module compiled to wasip1 ---

var (
	wasipOnce sync.Once
	wasipMod  []byte
	wasipErr  error
)

// buildTestprog compiles testdata/testprog to wasip1/wasm once. Tests that need
// it skip cleanly when the toolchain or target is unavailable.
func buildTestprog(t *testing.T) []byte {
	t.Helper()
	wasipOnce.Do(func() {
		if _, err := exec.LookPath("go"); err != nil {
			wasipErr = err
			return
		}
		out := filepath.Join(t.TempDir(), "testprog.wasm")
		cmd := exec.Command("go", "build", "-o", out, "./testdata/testprog")
		cmd.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm")
		if combined, err := cmd.CombinedOutput(); err != nil {
			wasipErr = err
			t.Logf("wasip1 build output: %s", combined)
			return
		}
		wasipMod, wasipErr = os.ReadFile(out)
	})
	if wasipErr != nil {
		t.Skipf("skipping: cannot build wasip1 module: %v", wasipErr)
	}
	return wasipMod
}

func TestRunCapturesStdoutArgsAndEnv(t *testing.T) {
	mod := buildTestprog(t)
	res, err := Run(context.Background(), RunSpec{
		Module: mod,
		Args:   []string{"prog", "alpha", "beta"},
		Env:    map[string]string{"GREETEE": "crabbox"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("ExitCode = %d (stderr: %s)", res.ExitCode, res.Stderr)
	}
	out := string(res.Stdout)
	if !strings.Contains(out, "args:alpha beta") {
		t.Fatalf("stdout missing args: %q", out)
	}
	if !strings.Contains(out, "hello:crabbox") {
		t.Fatalf("stdout missing env: %q", out)
	}
}

func TestRunPropagatesNonZeroExit(t *testing.T) {
	mod := buildTestprog(t)
	res, err := Run(context.Background(), RunSpec{
		Module: mod,
		Args:   []string{"prog"},
		Env:    map[string]string{"EXIT": "9"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 9 {
		t.Fatalf("ExitCode = %d, want 9", res.ExitCode)
	}
}

// Deny-by-default: with no MountDir, the guest cannot read host files.
func TestRunDeniesFilesystemByDefault(t *testing.T) {
	mod := buildTestprog(t)
	res, err := Run(context.Background(), RunSpec{
		Module: mod,
		Args:   []string{"prog"},
		Env:    map[string]string{"READ": "/work/secret.txt"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(res.Stdout), "fileerr:") {
		t.Fatalf("expected a file error with no mount, got: %q", res.Stdout)
	}
}

// With an explicit read-only mount, the guest reads exactly what was granted.
func TestRunReadsReadOnlyMount(t *testing.T) {
	mod := buildTestprog(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "secret.txt"), []byte("granted-value\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := Run(context.Background(), RunSpec{
		Module:   mod,
		Args:     []string{"prog"},
		MountDir: dir,
		Env:      map[string]string{"READ": "/work/secret.txt"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(res.Stdout), "file:granted-value") {
		t.Fatalf("expected mounted file contents, got: %q", res.Stdout)
	}
}

// The gate value holds for a non-trivial real workload too.
func TestRealModuleDeterministicCalls(t *testing.T) {
	mod := buildTestprog(t)
	spec := RunSpec{Module: mod, Args: []string{"prog", "x"}, Env: map[string]string{"GREETEE": "z"}}
	first, err := Run(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Run(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	if first.Calls != second.Calls {
		t.Fatalf("non-deterministic call count: %d != %d", first.Calls, second.Calls)
	}
	if string(first.Stdout) != string(second.Stdout) {
		t.Fatal("non-deterministic stdout")
	}
}
