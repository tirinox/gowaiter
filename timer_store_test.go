package main

import (
	"errors"
	"log"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestBoltTimerStorePersistsAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "timers.db")
	createdAt := time.Date(2026, time.September, 4, 12, 0, 0, 123, time.UTC)
	want := PersistedTimer{
		Tag:       "game-42",
		URL:       "http://nginx/first",
		CreatedAt: createdAt,
		DueAt:     createdAt.Add(10 * time.Minute),
	}

	store, err := OpenBoltTimerStore(path)
	if err != nil {
		t.Fatalf("OpenBoltTimerStore() error = %v", err)
	}
	if err := store.Put(want); err != nil {
		t.Fatalf("Put() error = %v", err)
	}
	want.URL = "http://nginx/replaced"
	if err := store.Put(want); err != nil {
		t.Fatalf("replacement Put() error = %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	store, err = OpenBoltTimerStore(path)
	if err != nil {
		t.Fatalf("reopen error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	timers, err := store.List()
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if !reflect.DeepEqual(timers, []PersistedTimer{want}) {
		t.Fatalf("timers = %#v, want %#v", timers, []PersistedTimer{want})
	}

	if err := store.Delete(want.Tag); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	timers, err = store.List()
	if err != nil || len(timers) != 0 {
		t.Fatalf("List() after delete = %#v, %v", timers, err)
	}
}

func TestPersistentSchedulerSavesReplacesAndDeletes(t *testing.T) {
	now := time.Date(2026, time.September, 4, 12, 0, 0, 0, time.UTC)
	store := newMemoryTimerStore()
	scheduler := NewPersistentScheduler(nil, store)
	scheduler.now = func() time.Time { return now }

	first := addTestTimer(t, scheduler, "game", 10*time.Minute, "http://nginx/first")
	stored := store.timer("game")
	if stored.CreatedAt != now || stored.DueAt != now.Add(10*time.Minute) {
		t.Fatalf("stored timestamps = (%s, %s)", stored.CreatedAt, stored.DueAt)
	}
	if stored.URL != first.url {
		t.Fatalf("stored URL = %q, want %q", stored.URL, first.url)
	}

	now = now.Add(time.Minute)
	replacement := addTestTimer(t, scheduler, "game", 20*time.Minute, "http://nginx/replaced")
	if first.timer != nil {
		t.Fatal("replaced timer was not stopped")
	}
	stored = store.timer("game")
	if stored.URL != replacement.url || stored.CreatedAt != now {
		t.Fatalf("replacement was not persisted: %#v", stored)
	}

	deleted, err := scheduler.Delete("game")
	if err != nil || !deleted {
		t.Fatalf("Delete() = (%t, %v), want (true, nil)", deleted, err)
	}
	if store.has("game") {
		t.Fatal("deleted timer remains in the store")
	}
}

func TestPersistentSchedulerDoesNotReplaceTimerWhenSaveFails(t *testing.T) {
	store := newMemoryTimerStore()
	scheduler := NewPersistentScheduler(nil, store)
	first := addTestTimer(t, scheduler, "game", time.Hour, "http://nginx/first")

	store.putError = errors.New("disk full")
	replacement, ok, err := scheduler.AddWithLimit("game", time.Hour, "http://nginx/replaced", 10)
	if err == nil || ok || replacement != nil {
		t.Fatalf("AddWithLimit() = (%#v, %t, %v), want persistence error", replacement, ok, err)
	}
	if got := scheduler.timerByTag("game"); got != first {
		t.Fatal("failed replacement removed the original timer")
	}
	scheduler.Shutdown()
}

func TestPersistentSchedulerRestoresAndIgnoresOldTimers(t *testing.T) {
	now := time.Date(2026, time.September, 4, 12, 0, 0, 0, time.UTC)
	store := newMemoryTimerStore()
	store.timers["future"] = PersistedTimer{
		Tag: "future", URL: "http://nginx/future",
		CreatedAt: now.Add(-10 * time.Minute), DueAt: now.Add(10 * time.Minute),
	}
	store.timers["overdue"] = PersistedTimer{
		Tag: "overdue", URL: "http://nginx/overdue",
		CreatedAt: now.Add(-30 * time.Minute), DueAt: now.Add(-time.Minute),
	}
	store.timers["stale"] = PersistedTimer{
		Tag: "stale", URL: "http://nginx/stale",
		CreatedAt: now.Add(-time.Hour - time.Second), DueAt: now.Add(time.Hour),
	}
	store.timers["invalid"] = PersistedTimer{
		Tag: "invalid", URL: "file:///tmp/task",
		CreatedAt: now.Add(-time.Minute), DueAt: now.Add(time.Minute),
	}

	fired := make(chan string, 1)
	scheduler := NewPersistentScheduler(func(timer *Timer) {
		fired <- timer.tag
	}, store)
	scheduler.now = func() time.Time { return now }
	var logs strings.Builder
	scheduler.logger = log.New(&logs, "", 0)

	restored, ignored, err := scheduler.Restore(time.Hour, 10)
	if err != nil {
		t.Fatalf("Restore() error = %v", err)
	}
	if restored != 2 || ignored != 2 {
		t.Fatalf("Restore() = (%d, %d), want (2, 2)", restored, ignored)
	}
	if store.has("stale") || store.has("invalid") {
		t.Fatal("ignored timers remain in the store")
	}
	if !strings.Contains(logs.String(), `tag="stale"`) {
		t.Fatalf("stale timer was not logged: %q", logs.String())
	}

	select {
	case tag := <-fired:
		if tag != "overdue" {
			t.Fatalf("fired tag = %q, want overdue", tag)
		}
	case <-time.After(time.Second):
		t.Fatal("overdue fresh timer did not fire immediately")
	}
	waitForInactiveTimer(t, scheduler, "overdue")
	if store.has("overdue") {
		t.Fatal("completed restored timer remains in the store")
	}

	future := scheduler.timerByTag("future")
	if future == nil || future.createdAt != now.Add(-10*time.Minute) || future.dueAt != now.Add(10*time.Minute) {
		t.Fatalf("future timer was not restored: %#v", future)
	}
	scheduler.Shutdown()
	if !store.has("future") {
		t.Fatal("pending timer was deleted during shutdown")
	}
}

func TestPersistentSchedulerRejectsAddsAfterShutdown(t *testing.T) {
	store := newMemoryTimerStore()
	scheduler := NewPersistentScheduler(nil, store)
	scheduler.Shutdown()

	if timer, err := scheduler.Add("late", time.Minute, "http://nginx/task"); !errors.Is(err, ErrSchedulerClosed) || timer != nil {
		t.Fatalf("Add() = (%#v, %v), want ErrSchedulerClosed", timer, err)
	}
}

type memoryTimerStore struct {
	mu          sync.Mutex
	timers      map[string]PersistedTimer
	putError    error
	deleteError error
}

func newMemoryTimerStore() *memoryTimerStore {
	return &memoryTimerStore{timers: make(map[string]PersistedTimer)}
}

func (store *memoryTimerStore) Put(timer PersistedTimer) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.putError != nil {
		return store.putError
	}
	store.timers[timer.Tag] = timer
	return nil
}

func (store *memoryTimerStore) Delete(tag string) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.deleteError != nil {
		return store.deleteError
	}
	delete(store.timers, tag)
	return nil
}

func (store *memoryTimerStore) List() ([]PersistedTimer, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	timers := make([]PersistedTimer, 0, len(store.timers))
	for _, timer := range store.timers {
		timers = append(timers, timer)
	}
	return timers, nil
}

func (store *memoryTimerStore) Close() error {
	return nil
}

func (store *memoryTimerStore) timer(tag string) PersistedTimer {
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.timers[tag]
}

func (store *memoryTimerStore) has(tag string) bool {
	store.mu.Lock()
	defer store.mu.Unlock()
	_, ok := store.timers[tag]
	return ok
}

func waitForInactiveTimer(t *testing.T, scheduler *Scheduler, tag string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for scheduler.timerByTag(tag) != nil {
		if time.Now().After(deadline) {
			t.Fatalf("timer %q is still active", tag)
		}
		time.Sleep(time.Millisecond)
	}
}

var _ TimerStore = (*memoryTimerStore)(nil)
