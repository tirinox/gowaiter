package main

import (
	"context"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestLoadCronConfig(t *testing.T) {
	path := writeCronConfig(t, `[
		{"period":60,"task":"http://nginx/erudite/api/cron.php"},
		{"period":300,"task":"http://worker:8080/task"}
	]`)

	entries, err := LoadCronConfig(path)
	if err != nil {
		t.Fatalf("LoadCronConfig() error = %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("entry count = %d, want 2", len(entries))
	}
	if entries[0].Period != 60 || entries[0].Task != "http://nginx/erudite/api/cron.php" {
		t.Fatalf("first entry = %#v", entries[0])
	}
}

func TestLoadCronConfigAllowsEmptyArray(t *testing.T) {
	entries, err := LoadCronConfig(writeCronConfig(t, `[]`))
	if err != nil {
		t.Fatalf("LoadCronConfig() error = %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("entry count = %d, want 0", len(entries))
	}
}

func TestLoadCronConfigRejectsInvalidConfiguration(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "malformed", body: `[`},
		{name: "not array", body: `{}`},
		{name: "null", body: `null`},
		{name: "trailing value", body: `[] []`},
		{name: "unknown field", body: `[{"period":60,"task":"http://nginx/task","typo":true}]`},
		{name: "zero period", body: `[{"period":0,"task":"http://nginx/task"}]`},
		{name: "negative period", body: `[{"period":-1,"task":"http://nginx/task"}]`},
		{name: "excessive period", body: `[{"period":9223372037,"task":"http://nginx/task"}]`},
		{name: "missing task", body: `[{"period":60}]`},
		{name: "relative task", body: `[{"period":60,"task":"/cron.php"}]`},
		{name: "credentials", body: `[{"period":60,"task":"http://user:pass@nginx/task"}]`},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := LoadCronConfig(writeCronConfig(t, test.body)); err == nil {
				t.Fatal("LoadCronConfig() succeeded, want error")
			}
		})
	}
}

func TestLoadCronConfigRejectsMissingAndOversizedFiles(t *testing.T) {
	if _, err := LoadCronConfig(filepath.Join(t.TempDir(), "missing.json")); err == nil {
		t.Fatal("missing config was accepted")
	}

	oversized := strings.Repeat(" ", maxCronConfigBytes+1)
	if _, err := LoadCronConfig(writeCronConfig(t, oversized)); err == nil {
		t.Fatal("oversized config was accepted")
	}
}

func TestCronRunnerWaitsForTickAndDoesNotOverlapRuns(t *testing.T) {
	ticker := newManualCronTicker()
	callbackStarted := make(chan string, 2)
	releaseFirst := make(chan struct{})
	var calls int
	var callsMu sync.Mutex

	runner := NewCronRunner([]CronEntry{{Period: 60, Task: "http://nginx/cron.php"}}, func(ctx context.Context, task string) {
		callsMu.Lock()
		calls++
		call := calls
		callsMu.Unlock()
		callbackStarted <- task
		if call == 1 {
			<-releaseFirst
		}
	})
	runner.logger = log.New(io.Discard, "", 0)
	runner.newTicker = func(period time.Duration) cronTicker {
		if period != time.Minute {
			t.Errorf("ticker period = %s, want 1m", period)
		}
		return ticker
	}

	ctx, cancel := context.WithCancel(context.Background())
	runner.Start(ctx)
	assertNoCronCall(t, callbackStarted)

	ticker.Tick()
	assertCronCall(t, callbackStarted, "http://nginx/cron.php")
	ticker.Tick()
	assertNoCronCall(t, callbackStarted)

	close(releaseFirst)
	waitForTickerDrain(t, ticker)
	ticker.Tick()
	assertCronCall(t, callbackStarted, "http://nginx/cron.php")

	cancel()
	runner.Wait()
	if !ticker.Stopped() {
		t.Fatal("ticker was not stopped")
	}
}

func TestCronRunnerCancelsActiveCallbackOnShutdown(t *testing.T) {
	ticker := newManualCronTicker()
	started := make(chan struct{})
	finished := make(chan struct{})
	runner := NewCronRunner([]CronEntry{{Period: 1, Task: "http://nginx/cron.php"}}, func(ctx context.Context, task string) {
		close(started)
		<-ctx.Done()
		close(finished)
	})
	runner.logger = log.New(io.Discard, "", 0)
	runner.newTicker = func(time.Duration) cronTicker { return ticker }

	ctx, cancel := context.WithCancel(context.Background())
	runner.Start(ctx)
	ticker.Tick()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("callback did not start")
	}

	cancel()
	done := make(chan struct{})
	go func() {
		runner.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("runner did not stop")
	}
	select {
	case <-finished:
	default:
		t.Fatal("active callback did not observe cancellation")
	}
}

type manualCronTicker struct {
	ticks   chan time.Time
	mu      sync.Mutex
	stopped bool
}

func newManualCronTicker() *manualCronTicker {
	return &manualCronTicker{ticks: make(chan time.Time, 8)}
}

func (ticker *manualCronTicker) C() <-chan time.Time {
	return ticker.ticks
}

func (ticker *manualCronTicker) Stop() {
	ticker.mu.Lock()
	ticker.stopped = true
	ticker.mu.Unlock()
}

func (ticker *manualCronTicker) Tick() {
	ticker.ticks <- time.Now()
}

func (ticker *manualCronTicker) Stopped() bool {
	ticker.mu.Lock()
	defer ticker.mu.Unlock()
	return ticker.stopped
}

func writeCronConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "cron.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write cron config: %v", err)
	}
	return path
}

func assertCronCall(t *testing.T, calls <-chan string, want string) {
	t.Helper()
	select {
	case got := <-calls:
		if got != want {
			t.Fatalf("callback task = %q, want %q", got, want)
		}
	case <-time.After(time.Second):
		t.Fatal("callback did not run")
	}
}

func assertNoCronCall(t *testing.T, calls <-chan string) {
	t.Helper()
	select {
	case task := <-calls:
		t.Fatalf("unexpected callback for %q", task)
	case <-time.After(20 * time.Millisecond):
	}
}

func waitForTickerDrain(t *testing.T, ticker *manualCronTicker) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for len(ticker.ticks) != 0 {
		if time.Now().After(deadline) {
			t.Fatal("missed tick was not discarded")
		}
		time.Sleep(time.Millisecond)
	}
}
