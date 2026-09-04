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

func initTimers() {
	scheduler = NewScheduler(doTimerAction)
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
	bind := flag.String("bind", defaultBind, "HTTP listen address")
	cronConfig := flag.String("cron-config", defaultCronConfigPath, "path to the cron JSON configuration")
	flag.Parse()

	initTimers()
	shutdownContext, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cronEntries, err := LoadCronConfig(*cronConfig)
	if err != nil {
		return fmt.Errorf("load CRON configuration: %w", err)
	}
	cronRunner := NewCronRunner(cronEntries, func(ctx context.Context, rawURL string) {
		getURL(ctx, "CRON", rawURL)
	})
	cronRunner.Start(shutdownContext)

	server := newHTTPServer(*bind, NewAPI(scheduler))
	log.Printf("Starting gowaiter on %s", *bind)
	err = serveUntilShutdown(shutdownContext, server)
	stop()
	cronRunner.Wait()
	if err != nil {
		return fmt.Errorf("serve HTTP: %w", err)
	}

	return nil
}

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}
