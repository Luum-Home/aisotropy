package aisotropy

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"

	badger "github.com/dgraph-io/badger/v4"

	"github.com/Luum-Home/aisotropy/internal/memory"
)

// TestFailedBootReleasesTheDataDirLock is the regression for the defect this
// file was added for: startRuntime's cost-meter error branch used to stop the
// background workers and return WITHOUT closing the stores it had already
// opened. With AISOTROPY_DATA_DIR set, working memory is an on-disk BadgerDB,
// and BadgerDB holds a directory lock for as long as it is open — so a boot
// that failed on the cost meter left the data directory locked for the rest of
// the process's life. The operator's next move after a failed start is to start
// again; that second start could not open its own database.
//
// The probe is deliberately the thing the operator does: after the failed boot,
// open the same directory again. A refusal here is the lock still being held.
func TestFailedBootReleasesTheDataDirLock(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	t.Setenv("AISOTROPY_DATA_DIR", dir)

	// Seed unreadable cost totals: the one condition that makes
	// costmeter.NewPersistent refuse, AFTER every store is already open and the
	// background workers are already running.
	wm, err := memory.Open(dir, 10, memory.NoopSummarizer{})
	if err != nil {
		t.Fatalf("open working memory: %v", err)
	}
	if err := wm.NewKV(costTotalsKeyPrefix).Store(ctx, map[string][]byte{"acme": []byte("{not json")}); err != nil {
		t.Fatalf("seed corrupt totals: %v", err)
	}
	if err := wm.Close(); err != nil {
		t.Fatalf("close seed store: %v", err)
	}

	app := New(WithLLM(fastStub()), WithRateLimit(1e9, 1e9))
	rt, err := app.ensureRuntime()
	if err == nil {
		rt.close(ctx)
		t.Fatal("the runtime booted over unreadable cost totals")
	}
	if !strings.Contains(err.Error(), "cost meter") {
		t.Fatalf("boot error = %v, want it to name the cost meter", err)
	}

	// The operator restarts. Same directory, same process lifetime.
	again, err := memory.Open(dir, 10, memory.NoopSummarizer{})
	if err != nil {
		t.Fatalf("the data directory is still locked after the failed boot: %v", err)
	}
	if err := again.Close(); err != nil {
		t.Errorf("close: %v", err)
	}
}

// TestWorkerPanicDuringTeardownDoesNotKillTheProcess covers the far side of the
// bounded join. waitWorkers gives up on a stuck worker and closes the stores
// anyway — deliberately — and a worker caught inside a BadgerDB read at that
// moment does not get an error back: badger's Txn.NewIterator panics with
// ErrDBClosed. Unrecovered in a goroutine that ends the process, so the shutdown
// that chose "close anyway" died from its own choice. Here the panic must be
// logged and the process must survive.
func TestWorkerPanicDuringTeardownDoesNotKillTheProcess(t *testing.T) {
	workerCtx, stopWorker := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	launch := launchWorkerOn(workerCtx, &wg)

	// The worker only panics once teardown has begun, which is exactly when the
	// real store close lands underneath a real worker.
	started := make(chan struct{})
	launch(func() {
		close(started)
		<-workerCtx.Done()
		panic(badger.ErrDBClosed)
	})
	<-started
	stopWorker()

	// The join returning at all is the assertion: an unrecovered panic here would
	// have taken the test binary down instead.
	waitWorkers(context.Background(), &wg)
}

// TestWorkerPanicBeforeTeardownStaysFatal is the other half, and the reason the
// recover above is gated rather than blanket: while the workers are still meant
// to be running, a panic is a bug and must stay fatal. Swallowing it would turn
// this guard into the thing it must never become — a silence that hides defects
// in a live system. Verified in a child process, because a correct result here
// is a dead process.
func TestWorkerPanicBeforeTeardownStaysFatal(t *testing.T) {
	if os.Getenv("AISOTROPY_TEST_FATAL_WORKER_CHILD") == "1" {
		workerCtx, stopWorker := context.WithCancel(context.Background())
		defer stopWorker()
		var wg sync.WaitGroup
		launchWorkerOn(workerCtx, &wg)(func() { panic("worker bug while the runtime is live") })
		wg.Wait()
		return
	}

	cmd := exec.Command(os.Args[0], "-test.run=TestWorkerPanicBeforeTeardownStaysFatal", "-test.v")
	cmd.Env = append(os.Environ(), "AISOTROPY_TEST_FATAL_WORKER_CHILD=1")
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("the child survived a worker panic outside teardown — the recover is swallowing real bugs:\n%s", out)
	}
	if !strings.Contains(string(out), "worker bug while the runtime is live") {
		t.Errorf("child died without reporting the original panic value:\n%s", out)
	}
}

// TestWorkerPanicGateTracksTeardown pins the decision itself: teardown is
// "stopWorker() has been called", nothing looser.
func TestWorkerPanicGateTracksTeardown(t *testing.T) {
	workerCtx, stopWorker := context.WithCancel(context.Background())
	if workerPanicIsTeardownRace(workerCtx) {
		t.Fatal("a live runtime is being treated as teardown; real worker bugs would be logged away")
	}
	stopWorker()
	if !workerPanicIsTeardownRace(workerCtx) {
		t.Fatal("teardown is not recognised; the bounded-join race would still crash the process")
	}
}
