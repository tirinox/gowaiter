package main

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestSchedulerFireRunsActionAndRemovesTimer(t *testing.T) {
	var fired *Timer
	scheduler := NewScheduler(func(timer *Timer) {
		fired = timer
	})

	timer := addTestTimer(t, scheduler, "tag", time.Hour, "https://example.test")
	scheduler.fire(timer)

	if fired != timer {
		t.Fatalf("action received timer %p, want %p", fired, timer)
	}
	if got := scheduler.timerByTag("tag"); got != nil {
		t.Fatalf("fired timer is still active: %#v", got)
	}
	maxCounter, active := scheduler.Stats()
	if maxCounter != 1 || active != 0 {
		t.Fatalf("Stats() = (%d, %d), want (1, 0)", maxCounter, active)
	}
}

func TestSchedulerTimerFires(t *testing.T) {
	fired := make(chan *Timer, 1)
	scheduler := NewScheduler(func(timer *Timer) {
		fired <- timer
	})

	timer := addTestTimer(t, scheduler, "tag", 0, "https://example.test")
	select {
	case got := <-fired:
		if got != timer {
			t.Fatalf("action received timer %p, want %p", got, timer)
		}
	case <-time.After(time.Second):
		t.Fatal("timer did not fire")
	}

	deadline := time.Now().Add(time.Second)
	for {
		_, active := scheduler.Stats()
		if active == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("fired timer was not removed")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestSchedulerReplaceStopsOldTimer(t *testing.T) {
	var fired []*Timer
	scheduler := NewScheduler(func(timer *Timer) {
		fired = append(fired, timer)
	})

	old := addTestTimer(t, scheduler, "tag", time.Hour, "https://old.example.test")
	replacement := addTestTimer(t, scheduler, "tag", time.Hour, "https://new.example.test")

	if old.timer != nil {
		t.Fatal("replaced timer was not stopped")
	}
	if got := scheduler.timerByTag("tag"); got != replacement {
		t.Fatalf("active timer = %p, want replacement %p", got, replacement)
	}

	scheduler.fire(old)
	if len(fired) != 0 {
		t.Fatalf("replaced timer fired %d actions, want 0", len(fired))
	}

	scheduler.fire(replacement)
	if len(fired) != 1 || fired[0] != replacement {
		t.Fatalf("actions = %#v, want only replacement", fired)
	}
}

func TestSchedulerOldActionDoesNotDeleteReplacement(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	scheduler := NewScheduler(func(timer *Timer) {
		close(started)
		<-release
	})

	old := addTestTimer(t, scheduler, "tag", time.Hour, "https://old.example.test")
	done := make(chan struct{})
	go func() {
		scheduler.fire(old)
		close(done)
	}()

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("old timer action did not start")
	}
	replacement := addTestTimer(t, scheduler, "tag", time.Hour, "https://new.example.test")
	close(release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("old timer action did not finish")
	}

	if got := scheduler.timerByTag("tag"); got != replacement {
		t.Fatalf("old action removed replacement: got %p, want %p", got, replacement)
	}
	_, _ = scheduler.Delete("tag")
}

func TestSchedulerDeleteStopsTimer(t *testing.T) {
	fired := false
	scheduler := NewScheduler(func(timer *Timer) {
		fired = true
	})

	timer := addTestTimer(t, scheduler, "tag", time.Hour, "https://example.test")
	deleted, err := scheduler.Delete("tag")
	if err != nil || !deleted {
		t.Fatal("Delete() = false, want true")
	}
	deleted, err = scheduler.Delete("tag")
	if err != nil || deleted {
		t.Fatal("second Delete() = true, want false")
	}
	if timer.timer != nil {
		t.Fatal("deleted timer was not stopped")
	}

	scheduler.fire(timer)
	if fired {
		t.Fatal("deleted timer executed its action")
	}
}

func TestSchedulerAddWithLimit(t *testing.T) {
	scheduler := NewScheduler(nil)

	first, ok, err := scheduler.AddWithLimit("first", time.Hour, "https://example.test", 1)
	if err != nil || !ok || first == nil {
		t.Fatal("first timer was rejected")
	}
	if timer, ok, err := scheduler.AddWithLimit("second", time.Hour, "https://example.test", 1); err != nil || ok || timer != nil {
		t.Fatal("timer above the active limit was accepted")
	}

	replacement, ok, err := scheduler.AddWithLimit("first", time.Hour, "https://example.test", 1)
	if err != nil || !ok || replacement == nil {
		t.Fatal("replacement at the active limit was rejected")
	}
	if first.timer != nil {
		t.Fatal("replaced timer was not stopped")
	}
	_, _ = scheduler.Delete("first")
}

func TestSchedulerConcurrentAccess(t *testing.T) {
	const workers = 16
	const timersPerWorker = 50

	scheduler := NewScheduler(nil)
	ids := make(chan int, workers*timersPerWorker)

	var wg sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for i := 0; i < timersPerWorker; i++ {
				tag := fmt.Sprintf("%d-%d", worker, i)
				timer, err := scheduler.Add(tag, time.Hour, "https://example.test")
				if err != nil {
					t.Errorf("Add(%q) error = %v", tag, err)
					continue
				}
				ids <- timer.id
				_ = scheduler.timerByTag(tag)
				_, _ = scheduler.Stats()
				deleted, err := scheduler.Delete(tag)
				if err != nil || !deleted {
					t.Errorf("Delete(%q) = false, want true", tag)
				}
			}
		}(worker)
	}
	wg.Wait()
	close(ids)

	seen := make(map[int]struct{}, workers*timersPerWorker)
	for id := range ids {
		if _, exists := seen[id]; exists {
			t.Fatalf("duplicate timer id %d", id)
		}
		seen[id] = struct{}{}
	}

	maxCounter, active := scheduler.Stats()
	if maxCounter != workers*timersPerWorker || active != 0 {
		t.Fatalf(
			"Stats() = (%d, %d), want (%d, 0)",
			maxCounter,
			active,
			workers*timersPerWorker,
		)
	}
}

func addTestTimer(t *testing.T, scheduler *Scheduler, tag string, delay time.Duration, url string) *Timer {
	t.Helper()
	timer, err := scheduler.Add(tag, delay, url)
	if err != nil {
		t.Fatalf("Add(%q) error = %v", tag, err)
	}
	return timer
}
