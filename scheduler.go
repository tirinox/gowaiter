package main

import (
	"sync"
	"time"
)

type Timer struct {
	id    int
	tag   string
	delay time.Duration
	url   string
	timer *time.Timer
}

type Scheduler struct {
	mu      sync.RWMutex
	counter int
	timers  map[string]*Timer
	action  func(*Timer)
}

func NewScheduler(action func(*Timer)) *Scheduler {
	return &Scheduler{
		timers: make(map[string]*Timer),
		action: action,
	}
}

func (s *Scheduler) Add(tag string, delay time.Duration, url string) *Timer {
	s.mu.Lock()
	defer s.mu.Unlock()

	if old := s.timers[tag]; old != nil {
		s.stopTimerLocked(old)
	}

	s.counter++
	timer := &Timer{
		id:    s.counter,
		tag:   tag,
		delay: delay,
		url:   url,
	}

	s.timers[tag] = timer
	timer.timer = time.AfterFunc(delay, func() {
		s.fire(timer)
	})

	return timer
}

func (s *Scheduler) Delete(tag string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	timer := s.timers[tag]
	if timer == nil {
		return false
	}

	delete(s.timers, tag)
	s.stopTimerLocked(timer)
	return true
}

func (s *Scheduler) Stats() (maxCounter int, active int) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.counter, len(s.timers)
}

func (s *Scheduler) timerByTag(tag string) *Timer {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.timers[tag]
}

func (s *Scheduler) fire(timer *Timer) {
	s.mu.Lock()
	current := s.timers[timer.tag]
	if current != timer {
		s.mu.Unlock()
		return
	}

	// Stop also makes direct calls to fire deterministic in tests. When fire is
	// invoked by the timer itself, Stop simply reports that it has already fired.
	s.stopTimerLocked(timer)
	s.mu.Unlock()

	if s.action != nil {
		s.action(timer)
	}

	s.mu.Lock()
	if s.timers[timer.tag] == timer {
		delete(s.timers, timer.tag)
	}
	s.mu.Unlock()
}

func (s *Scheduler) stopTimerLocked(timer *Timer) {
	if timer.timer == nil {
		return
	}

	timer.timer.Stop()
	timer.timer = nil
}
