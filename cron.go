package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	defaultCronConfigPath = "./cron.json"
	maxCronConfigBytes    = 64 << 10
	maxCronPeriodSeconds  = int64((1<<63 - 1) / time.Second)
)

type CronEntry struct {
	Period int64  `json:"period"`
	Task   string `json:"task"`
}

type cronTicker interface {
	C() <-chan time.Time
	Stop()
}

type systemCronTicker struct {
	*time.Ticker
}

func (ticker systemCronTicker) C() <-chan time.Time {
	return ticker.Ticker.C
}

type CronRunner struct {
	entries   []CronEntry
	callback  func(context.Context, string)
	logger    *log.Logger
	newTicker func(time.Duration) cronTicker
	startOnce sync.Once
	waitGroup sync.WaitGroup
}

func LoadCronConfig(path string) ([]CronEntry, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open cron config %q: %w", path, err)
	}
	defer file.Close()

	raw, err := io.ReadAll(io.LimitReader(file, maxCronConfigBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read cron config %q: %w", path, err)
	}
	if len(raw) > maxCronConfigBytes {
		return nil, fmt.Errorf("cron config %q exceeds %d bytes", path, maxCronConfigBytes)
	}

	var entries []CronEntry
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&entries); err != nil {
		return nil, fmt.Errorf("decode cron config %q: %w", path, err)
	}
	if entries == nil {
		return nil, fmt.Errorf("decode cron config %q: expected a JSON array", path)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("decode cron config %q: expected one JSON array", path)
	}

	for index, entry := range entries {
		if err := validateCronEntry(entry); err != nil {
			return nil, fmt.Errorf("cron entry %d: %w", index, err)
		}
	}

	return entries, nil
}

func validateCronEntry(entry CronEntry) error {
	if entry.Period <= 0 || entry.Period > maxCronPeriodSeconds {
		return fmt.Errorf("period must be between 1 and %d seconds", maxCronPeriodSeconds)
	}
	if entry.Task == "" {
		return errors.New("task is required")
	}
	if len(entry.Task) > maxURLBytes || !utf8.ValidString(entry.Task) {
		return errors.New("task URL is invalid")
	}
	if _, err := parseHTTPURL(entry.Task); err != nil {
		return fmt.Errorf("invalid task URL: %w", err)
	}

	return nil
}

func NewCronRunner(entries []CronEntry, callback func(context.Context, string)) *CronRunner {
	return &CronRunner{
		entries:  append([]CronEntry(nil), entries...),
		callback: callback,
		logger:   log.Default(),
		newTicker: func(period time.Duration) cronTicker {
			return systemCronTicker{Ticker: time.NewTicker(period)}
		},
	}
}

func (runner *CronRunner) Start(ctx context.Context) {
	runner.startOnce.Do(func() {
		for _, entry := range runner.entries {
			entry := entry
			runner.waitGroup.Add(1)
			go func() {
				defer runner.waitGroup.Done()
				runner.runEntry(ctx, entry)
			}()
		}
	})
}

func (runner *CronRunner) Wait() {
	runner.waitGroup.Wait()
}

func (runner *CronRunner) runEntry(ctx context.Context, entry CronEntry) {
	period := time.Duration(entry.Period) * time.Second
	ticker := runner.newTicker(period)
	defer ticker.Stop()

	runner.logger.Printf("Starting CRON task %s with period %d sec", entry.Task, entry.Period)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C():
			if ctx.Err() != nil {
				return
			}

			runner.logger.Printf("CRON task %s starting", entry.Task)
			if runner.callback != nil {
				runner.callback(ctx, entry.Task)
			}
			runner.discardMissedTicks(ticker, entry.Task)
		}
	}
}

func (runner *CronRunner) discardMissedTicks(ticker cronTicker, task string) {
	for {
		select {
		case <-ticker.C():
			runner.logger.Printf("CRON task %s skipped because its previous run was still active", task)
		default:
			return
		}
	}
}
