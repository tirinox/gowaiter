// main
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"
)

var scheduler *Scheduler
var callbacks = newDefaultCallbackClient()

func getURL(ctx context.Context, source string, rawURL string) {
	result, err := callbacks.Get(ctx, rawURL)
	if err != nil {
		log.Printf("%s GET failed; attempts = %d; error = %s", source, result.Attempts, err)
		return
	}

	log.Printf(
		"%s GET url %s succeeded; status = %d; attempts = %d",
		source,
		rawURL,
		result.StatusCode,
		result.Attempts,
	)
}

func doTimerAction(t *Timer) {
	fmt.Printf("Timer BOOM id = %d\n", t.id)
	getURL(context.Background(), "Timer", t.url)
}

func initTimers(store TimerStore) {
	scheduler = NewPersistentScheduler(doTimerAction, store)
}

func newHTTPServer(bind string, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              bind,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
}

func serveUntilShutdown(ctx context.Context, server *http.Server) error {
	serverError := make(chan error, 1)
	go func() {
		serverError <- server.ListenAndServe()
	}()

	select {
	case err := <-serverError:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdownContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownContext); err != nil {
			return fmt.Errorf("shut down HTTP server: %w", err)
		}

		err := <-serverError
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

func run() error {
	defer callbacks.CloseIdleConnections()

	defaultBind := os.Getenv("BIND")
	if defaultBind == "" {
		defaultBind = ":10025"
	}
	defaultTimerDatabase := os.Getenv("TIMER_DB")
	if defaultTimerDatabase == "" {
		defaultTimerDatabase = "./gowaiter.db"
	}
	bind := flag.String("bind", defaultBind, "HTTP listen address")
	cronConfig := flag.String("cron-config", defaultCronConfigPath, "path to the cron JSON configuration")
	timerDatabase := flag.String("timer-db", defaultTimerDatabase, "path to the persistent timer database")
	flag.Parse()

	cronEntries, err := LoadCronConfig(*cronConfig)
	if err != nil {
		return fmt.Errorf("load CRON configuration: %w", err)
	}
	timerStore, err := OpenBoltTimerStore(*timerDatabase)
	if err != nil {
		return err
	}
	initTimers(timerStore)
	restored, ignored, err := scheduler.Restore(defaultPersistedTimerMaxAge, defaultMaxTimers)
	if err != nil {
		scheduler.Shutdown()
		_ = timerStore.Close()
		return fmt.Errorf("restore persisted timers: %w", err)
	}
	log.Printf("Persistent timers restored=%d ignored=%d", restored, ignored)

	shutdownContext, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cronRunner := NewCronRunner(cronEntries, func(ctx context.Context, rawURL string) {
		getURL(ctx, "CRON", rawURL)
	})
	cronRunner.Start(shutdownContext)

	var ready atomic.Bool
	server := newHTTPServer(*bind, NewAPIWithReadiness(scheduler, ready.Load))
	ready.Store(true)
	stopReadiness := context.AfterFunc(shutdownContext, func() {
		ready.Store(false)
	})
	defer stopReadiness()

	log.Printf("Starting gowaiter on %s", *bind)
	err = serveUntilShutdown(shutdownContext, server)
	ready.Store(false)
	stop()
	cronRunner.Wait()
	scheduler.Shutdown()
	closeError := timerStore.Close()
	if err != nil {
		return fmt.Errorf("serve HTTP: %w", err)
	}
	if closeError != nil {
		return closeError
	}

	return nil
}

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}
