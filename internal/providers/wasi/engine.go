// Package wasi runs WebAssembly System Interface (WASI preview1) modules in an
// embedded, pure-Go wazero runtime with deny-by-default capabilities and
// deterministic execution.
//
// It is the minimal v1 slice behind the Crabbox WASI proposal (openclaw/crabbox
// #533) scoped at the one workload where a restricted runtime is a feature
// rather than a limitation: reproducible, hermetic execution gates
// (openclaw/crabbox #280 — "Explore deterministic profiling gates for Crabbox
// runs"). Given the same module and inputs, Run produces identical output and an
// identical, host-independent invocation count — something a container or microVM
// cannot guarantee because of ambient time, scheduling, and filesystem state.
package wasi

import (
	"bytes"
	"context"
	"errors"
	"sort"
	"time"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/experimental"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
	"github.com/tetratelabs/wazero/sys"
)

// maxCaptureBytes bounds each captured stream so an adversarial module cannot
// exhaust host memory through stdout/stderr.
const maxCaptureBytes = 1 << 20

// RunSpec describes one hermetic, capability-scoped execution.
//
// Everything is deny-by-default: the guest sees no host environment, no
// filesystem, no network, and no real wall-clock unless granted here.
type RunSpec struct {
	Module   []byte            // the .wasm module bytes
	Args     []string          // argv (argv[0] is the program name)
	Env      map[string]string // explicit env; nothing is inherited from the host
	MountDir string            // host dir exposed read-only at /work; "" disables all FS
	Stdin    []byte            // optional stdin
}

// RunResult is the deterministic outcome of one execution.
type RunResult struct {
	ExitCode int
	Stdout   []byte
	Stderr   []byte
	// Calls is the number of function invocations observed during execution.
	// For a given module and inputs it is identical on every run and every
	// host, which is what makes it usable as a reproducible gate metric.
	Calls uint64
	// Duration is wall-clock time. It is informational only and deliberately
	// never part of the deterministic gate.
	Duration time.Duration
}

// Run compiles and executes a WASI module to completion (or until ctx is done)
// and returns its deterministic result. A guest proc_exit(n) is reported as
// ExitCode=n with a nil error; only host/runtime failures return an error.
func Run(ctx context.Context, spec RunSpec) (RunResult, error) {
	if len(spec.Module) == 0 {
		return RunResult{}, errors.New("wasi: empty module")
	}

	counter := &callCounter{}
	ctx = experimental.WithFunctionListenerFactory(ctx, experimental.FunctionListenerFactoryFunc(
		func(api.FunctionDefinition) experimental.FunctionListener { return counter },
	))

	r := wazero.NewRuntimeWithConfig(ctx, wazero.NewRuntimeConfig().WithCloseOnContextDone(true))
	defer r.Close(ctx)
	if _, err := wasi_snapshot_preview1.Instantiate(ctx, r); err != nil {
		return RunResult{}, err
	}

	stdout := &boundedBuffer{limit: maxCaptureBytes}
	stderr := &boundedBuffer{limit: maxCaptureBytes}
	cfg := wazero.NewModuleConfig().
		WithStdout(stdout).
		WithStderr(stderr).
		// Deterministic randomness: random_get is a pure function of the run,
		// so the module's output depends only on its declared inputs.
		WithRandSource(&deterministicRand{}).
		// Deliberately NOT WithSysWalltime/WithSysNanotime: the default fake
		// clock keeps execution reproducible across runs and machines.
		WithName("")
	if len(spec.Args) > 0 {
		cfg = cfg.WithArgs(spec.Args...)
	}
	// Apply env in a stable order so the guest's environ vector is identical
	// across runs regardless of Go map iteration order.
	for _, k := range sortedKeys(spec.Env) {
		cfg = cfg.WithEnv(k, spec.Env[k])
	}
	if spec.MountDir != "" {
		cfg = cfg.WithFSConfig(wazero.NewFSConfig().WithReadOnlyDirMount(spec.MountDir, "/work"))
	}
	if len(spec.Stdin) > 0 {
		cfg = cfg.WithStdin(bytes.NewReader(spec.Stdin))
	}

	start := time.Now()
	_, err := r.InstantiateWithConfig(ctx, spec.Module, cfg)
	res := RunResult{
		Stdout:   stdout.Bytes(),
		Stderr:   stderr.Bytes(),
		Calls:    counter.n,
		Duration: time.Since(start),
	}

	var exitErr *sys.ExitError
	if errors.As(err, &exitErr) {
		res.ExitCode = int(exitErr.ExitCode())
		return res, nil
	}
	if err != nil {
		return res, err
	}
	return res, nil
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// callCounter counts every function invocation. Execution is single-goroutine,
// so a plain counter is sufficient and keeps the count deterministic.
type callCounter struct{ n uint64 }

func (c *callCounter) Before(context.Context, api.Module, api.FunctionDefinition, []uint64, experimental.StackIterator) {
	c.n++
}
func (c *callCounter) After(context.Context, api.Module, api.FunctionDefinition, []uint64) {}
func (c *callCounter) Abort(context.Context, api.Module, api.FunctionDefinition, error)    {}

// deterministicRand is a fixed, reproducible byte source for random_get.
type deterministicRand struct{ n byte }

func (d *deterministicRand) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = d.n
		d.n++
	}
	return len(p), nil
}

// boundedBuffer is an io.Writer that captures at most limit bytes and silently
// drops the overflow, so a runaway guest cannot exhaust host memory.
type boundedBuffer struct {
	buf   bytes.Buffer
	limit int
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	if remaining := b.limit - b.buf.Len(); remaining > 0 {
		if len(p) > remaining {
			b.buf.Write(p[:remaining])
		} else {
			b.buf.Write(p)
		}
	}
	// Report the full length so the guest's write is considered complete.
	return len(p), nil
}

func (b *boundedBuffer) Bytes() []byte { return b.buf.Bytes() }
