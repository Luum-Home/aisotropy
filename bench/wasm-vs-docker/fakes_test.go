package main

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/Luum-Home/aisotropy/internal/sandbox"
)

// ---------------------------------------------------------------------------
// fakeRunner: a cmdRunner test double. Every method records the spec it was
// called with and delegates to an optional hook; an unstubbed call returns an
// error naming the spec, so a test that forgets to stub a path fails loudly
// instead of silently invoking a real process.
// ---------------------------------------------------------------------------

type fakeRunner struct {
	lookPathFn       func(file string) (string, error)
	outputFn         func(spec cmdSpec) ([]byte, error)
	combinedOutputFn func(spec cmdSpec) ([]byte, error)
	runFn            func(spec cmdSpec) error
	calls            []cmdSpec
}

func (f *fakeRunner) LookPath(file string) (string, error) {
	if f.lookPathFn != nil {
		return f.lookPathFn(file)
	}
	return "", fmt.Errorf("fakeRunner: LookPath(%q) not stubbed", file)
}

func (f *fakeRunner) Output(spec cmdSpec) ([]byte, error) {
	f.calls = append(f.calls, spec)
	if f.outputFn != nil {
		return f.outputFn(spec)
	}
	return nil, fmt.Errorf("fakeRunner: Output(%+v) not stubbed", spec)
}

func (f *fakeRunner) CombinedOutput(spec cmdSpec) ([]byte, error) {
	f.calls = append(f.calls, spec)
	if f.combinedOutputFn != nil {
		return f.combinedOutputFn(spec)
	}
	return nil, fmt.Errorf("fakeRunner: CombinedOutput(%+v) not stubbed", spec)
}

func (f *fakeRunner) Run(spec cmdSpec) error {
	f.calls = append(f.calls, spec)
	if f.runFn != nil {
		return f.runFn(spec)
	}
	return fmt.Errorf("fakeRunner: Run(%+v) not stubbed", spec)
}

// ---------------------------------------------------------------------------
// fakeComputeProvider / fakeComputeInstance: sandbox.ComputeProvider and
// sandbox.ComputeInstance test doubles, so WASM-candidate logic (benchWASM,
// rssOnlyMain) is testable without a real wazero-compiled module.
// ---------------------------------------------------------------------------

type fakeComputeInstance struct {
	name       string
	dispatchFn func(ctx context.Context, args json.RawMessage) (string, error)
	closeErr   error
	closeCalls int
}

func (i *fakeComputeInstance) Dispatch(ctx context.Context, argsJSON json.RawMessage) (string, error) {
	if i.dispatchFn != nil {
		return i.dispatchFn(ctx, argsJSON)
	}
	return `{"ok":true}`, nil
}

func (i *fakeComputeInstance) Name() string { return i.name }

func (i *fakeComputeInstance) Close(ctx context.Context) error {
	i.closeCalls++
	return i.closeErr
}

type fakeComputeProvider struct {
	loadFn    func(ctx context.Context, spec sandbox.ComputeSpec) (sandbox.ComputeInstance, error)
	loadCalls int
}

func (p *fakeComputeProvider) Load(ctx context.Context, spec sandbox.ComputeSpec) (sandbox.ComputeInstance, error) {
	p.loadCalls++
	if p.loadFn != nil {
		return p.loadFn(ctx, spec)
	}
	return &fakeComputeInstance{name: spec.Name}, nil
}

func (p *fakeComputeProvider) Name() string { return "fake-wasm" }

var _ sandbox.ComputeProvider = (*fakeComputeProvider)(nil)

// ---------------------------------------------------------------------------
// fakeSandbox / fakeDockerProvider: sandbox.Sandbox and the local
// dockerProvider (sandbox.Provider + CleanupAll) test doubles.
// ---------------------------------------------------------------------------

type fakeSandbox struct {
	execFn   func(ctx context.Context, argv []string, opts sandbox.ExecOptions) (sandbox.ExecResult, error)
	closeErr error
	workdir  string
	execN    int
}

func (s *fakeSandbox) Exec(ctx context.Context, argv []string, opts sandbox.ExecOptions) (sandbox.ExecResult, error) {
	s.execN++
	if s.execFn != nil {
		return s.execFn(ctx, argv, opts)
	}
	return sandbox.ExecResult{ExitCode: 0}, nil
}

func (s *fakeSandbox) CopyIn(ctx context.Context, hostPath, sandboxPath string) error  { return nil }
func (s *fakeSandbox) CopyOut(ctx context.Context, sandboxPath, hostPath string) error { return nil }
func (s *fakeSandbox) Workdir() string                                                 { return s.workdir }
func (s *fakeSandbox) Close(ctx context.Context) error                                 { return s.closeErr }

var _ sandbox.Sandbox = (*fakeSandbox)(nil)

type fakeDockerProvider struct {
	createFn     func(ctx context.Context, spec sandbox.Spec) (sandbox.Sandbox, error)
	cleanupCalls int
}

func (p *fakeDockerProvider) Create(ctx context.Context, spec sandbox.Spec) (sandbox.Sandbox, error) {
	if p.createFn != nil {
		return p.createFn(ctx, spec)
	}
	return &fakeSandbox{}, nil
}

func (p *fakeDockerProvider) Name() string                   { return "fake-docker" }
func (p *fakeDockerProvider) CleanupAll(ctx context.Context) { p.cleanupCalls++ }

var _ dockerProvider = (*fakeDockerProvider)(nil)
