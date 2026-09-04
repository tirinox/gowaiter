package main

import (
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const defaultPersistedTimerMaxAge = time.Hour

var ErrSchedulerClosed = errors.New("scheduler is closed")

type Timer struct {
	id    int
	tag   string
	delay time.Duration
	url   string
	timer *time.Timer

	createdAt time.Time
	dueAt     time.Time
	running   bool
}

type Scheduler struct {
	mu          sync.RWMutex
	counter     int
	timers      map[string]*Timer
	action      func(*Timer)
	store       TimerStore
	now         func() time.Time
	logger      *log.Logger
	closing     bool
	activeCalls sync.WaitGroup
}

func NewScheduler(action func(*Timer)) *Scheduler {
	return NewPersistentScheduler(action, nil)
}

func NewPersistentScheduler(action func(*Timer), store TimerStore) *Scheduler {
	return &Scheduler{
		timers: make(map[string]*Timer),
		action: action,
		store:  store,
		now:    time.Now,
		logger: log.Default(),
	}
}

func (s *Scheduler) Add(tag string, delay time.Duration, url string) (*Timer, error) {
	timer, _, err := s.AddWithLimit(tag, delay, url, 0)
	return timer, err
}

func (s *Scheduler) AddWithLimit(tag string, delay time.Duration, url string, maxActive int) (*Timer, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closing {
		return nil, false, ErrSchedulerClosed
	}

	old := s.timers[tag]
	if old == nil && maxActive > 0 && len(s.timers) >= maxActive {
		return nil, false, nil
	}

	createdAt := s.now().UTC()
	dueAt := createdAt.Add(delay)
	if s.store != nil {
		if err := s.store.Put(PersistedTimer{
			Tag:       tag,
			URL:       url,
			CreatedAt: createdAt,
			DueAt:     dueAt,
		}); err != nil {
			return nil, false, err
		}
	}

	s.counter++
	timer := &Timer{
		id:        s.counter,
		tag:       tag,
		delay:     delay,
		url:       url,
		createdAt: createdAt,
		dueAt:     dueAt,
	}

	if old != nil {
		s.stopTimerLocked(old)
	}
	s.timers[tag] = timer
	timer.timer = time.AfterFunc(delay, func() {
		s.fire(timer)
	})

	return timer, true, nil
}

func (s *Scheduler) Delete(tag string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	timer := s.timers[tag]
	if timer == nil {
		return false, nil
	}
	if s.store != nil {
		if err := s.store.Delete(tag); err != nil {
			return false, err
		}
	}

	delete(s.timers, tag)
	s.stopTimerLocked(timer)
	return true, nil
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
	if current != timer || s.closing {
		s.mu.Unlock()
		return
	}

	// Stop also makes direct calls to fire deterministic in tests. When fire is
	// invoked by the timer itself, Stop simply reports that it has already fired.
	s.stopTimerLocked(timer)
	timer.running = true
	s.activeCalls.Add(1)
	s.mu.Unlock()
	defer s.activeCalls.Done()

	if s.action != nil {
		s.action(timer)
	}

	s.mu.Lock()
	if s.timers[timer.tag] == timer {
		if s.store != nil {
			if err := s.store.Delete(timer.tag); err != nil {
				s.logger.Printf("Failed to delete completed persisted timer tag=%q: %v", timer.tag, err)
			}
		}
		delete(s.timers, timer.tag)
	}
	s.mu.Unlock()
}

func (s *Scheduler) Restore(maxAge time.Duration, maxActive int) (restored int, ignored int, err error) {
	if s.store == nil {
		return 0, 0, nil
	}

	records, err := s.store.List()
	if err != nil {
		return 0, 0, err
	}
	now := s.now().UTC()

	for _, record := range records {
		reason := invalidPersistedTimerReason(record, now, maxAge)
		if reason != "" {
			s.logger.Printf("Ignoring persisted timer tag=%q: %s", record.Tag, reason)
			if err := s.store.Delete(record.Tag); err != nil {
				return restored, ignored, err
			}
			ignored++
			continue
		}

		s.mu.Lock()
		if maxActive > 0 && len(s.timers) >= maxActive {
			s.mu.Unlock()
			return restored, ignored, fmt.Errorf("persisted timer limit %d exceeded", maxActive)
		}

		delay := record.DueAt.Sub(now)
		if delay < 0 {
			delay = 0
		}
		s.counter++
		timer := &Timer{
			id:        s.counter,
			tag:       record.Tag,
			delay:     delay,
			url:       record.URL,
			createdAt: record.CreatedAt,
			dueAt:     record.DueAt,
		}
		s.timers[timer.tag] = timer
		timer.timer = time.AfterFunc(delay, func() {
			s.fire(timer)
		})
		s.mu.Unlock()
		restored++
	}

	return restored, ignored, nil
}

func (s *Scheduler) Shutdown() {
	s.mu.Lock()
	if !s.closing {
		s.closing = true
		for tag, timer := range s.timers {
			if timer.running {
				continue
			}
			s.stopTimerLocked(timer)
			delete(s.timers, tag)
		}
	}
	s.mu.Unlock()

	s.activeCalls.Wait()
}

func invalidPersistedTimerReason(timer PersistedTimer, now time.Time, maxAge time.Duration) string {
	if strings.TrimSpace(timer.Tag) == "" || len(timer.Tag) > maxTagBytes || !utf8.ValidString(timer.Tag) {
		return "invalid tag"
	}
	if len(timer.URL) > maxURLBytes || !utf8.ValidString(timer.URL) {
		return "invalid URL"
	}
	if _, err := parseHTTPURL(timer.URL); err != nil {
		return "invalid URL"
	}
	if timer.DueAt.Before(timer.CreatedAt) {
		return "due time is before creation time"
	}
	if now.Sub(timer.CreatedAt) > maxAge {
		return fmt.Sprintf("older than %s", maxAge)
	}
	return ""
}

func (s *Scheduler) stopTimerLocked(timer *Timer) {
	if timer.timer == nil {
		return
	}

	timer.timer.Stop()
	timer.timer = nil
}
