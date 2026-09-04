package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	bolt "go.etcd.io/bbolt"
)

var timersBucket = []byte("timers")

type PersistedTimer struct {
	Tag       string
	URL       string
	CreatedAt time.Time
	DueAt     time.Time
}

type TimerStore interface {
	Put(PersistedTimer) error
	Delete(string) error
	List() ([]PersistedTimer, error)
	Close() error
}

type BoltTimerStore struct {
	database *bolt.DB
}

type persistedTimerValue struct {
	URL             string `json:"url"`
	CreatedAtUnixNS int64  `json:"createdAtUnixNs"`
	DueAtUnixNS     int64  `json:"dueAtUnixNs"`
}

func OpenBoltTimerStore(path string) (*BoltTimerStore, error) {
	if path == "" {
		return nil, fmt.Errorf("timer database path is required")
	}

	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o750); err != nil {
		return nil, fmt.Errorf("create timer database directory %q: %w", directory, err)
	}

	database, err := bolt.Open(path, 0o600, &bolt.Options{Timeout: time.Second})
	if err != nil {
		return nil, fmt.Errorf("open timer database %q: %w", path, err)
	}

	store := &BoltTimerStore{database: database}
	if err := database.Update(func(transaction *bolt.Tx) error {
		_, err := transaction.CreateBucketIfNotExists(timersBucket)
		return err
	}); err != nil {
		_ = database.Close()
		return nil, fmt.Errorf("initialize timer database %q: %w", path, err)
	}

	return store, nil
}

func (store *BoltTimerStore) Put(timer PersistedTimer) error {
	value, err := json.Marshal(persistedTimerValue{
		URL:             timer.URL,
		CreatedAtUnixNS: timer.CreatedAt.UnixNano(),
		DueAtUnixNS:     timer.DueAt.UnixNano(),
	})
	if err != nil {
		return fmt.Errorf("encode timer %q: %w", timer.Tag, err)
	}

	if err := store.database.Update(func(transaction *bolt.Tx) error {
		return transaction.Bucket(timersBucket).Put([]byte(timer.Tag), value)
	}); err != nil {
		return fmt.Errorf("save timer %q: %w", timer.Tag, err)
	}

	return nil
}

func (store *BoltTimerStore) Delete(tag string) error {
	if err := store.database.Update(func(transaction *bolt.Tx) error {
		return transaction.Bucket(timersBucket).Delete([]byte(tag))
	}); err != nil {
		return fmt.Errorf("delete timer %q: %w", tag, err)
	}

	return nil
}

func (store *BoltTimerStore) List() ([]PersistedTimer, error) {
	var timers []PersistedTimer
	err := store.database.View(func(transaction *bolt.Tx) error {
		return transaction.Bucket(timersBucket).ForEach(func(key []byte, rawValue []byte) error {
			var value persistedTimerValue
			if err := json.Unmarshal(rawValue, &value); err != nil {
				return fmt.Errorf("decode timer %q: %w", string(key), err)
			}

			timers = append(timers, PersistedTimer{
				Tag:       string(key),
				URL:       value.URL,
				CreatedAt: time.Unix(0, value.CreatedAtUnixNS).UTC(),
				DueAt:     time.Unix(0, value.DueAtUnixNS).UTC(),
			})
			return nil
		})
	})
	if err != nil {
		return nil, fmt.Errorf("list timers: %w", err)
	}

	return timers, nil
}

func (store *BoltTimerStore) Close() error {
	if err := store.database.Close(); err != nil {
		return fmt.Errorf("close timer database: %w", err)
	}
	return nil
}
