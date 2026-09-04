// main
package main

import (
	"context"
	"encoding/json"
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

func getUrl(url string) {
	resp, err := http.Get(url)
	if err == nil {
		fmt.Printf("Timer GET url %s success\n", url)
		resp.Body.Close()
	} else {
		fmt.Printf("Timer GET fail; error = %s\n", err)
	}
}

func doTimerAction(t *Timer) {
	fmt.Printf("Timer BOOM id = %d\n", t.id)
	getUrl(t.url)
}

func initTimers() {
	scheduler = NewScheduler(doTimerAction)
}

// ----------- CRON ------------

type CronEntry struct {
	Period int    `json:"period"`
	Task   string `json:"task"`
}

func readCronConfig() []CronEntry {

	var tasks []CronEntry

	raw, err := os.ReadFile("./cron.json")
	if err != nil {
		fmt.Println("can't read cron.json")
		return tasks
	}

	json.Unmarshal(raw, &tasks)
	return tasks
}

func runCron() {
	tasks := readCronConfig()
	for _, task := range tasks {
		if task.Period > 0 {
			fmt.Printf("Starting CRON task %s with period %d sec\n", task.Task, task.Period)
			ticker := time.NewTicker(time.Duration(task.Period) * time.Second)
			url := task.Task // capture by value
			go func() {
				for {
					select {
					case <-ticker.C:
						fmt.Printf("CRON task %s starting...\n", url)
						go func() {
							getUrl(url)
						}()
					}
				}
			}()
		} else {
			fmt.Printf("period for %s isn't > 0 sec\n", task.Task)
		}
	}
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

func main() {
	defaultBind := os.Getenv("BIND")
	if defaultBind == "" {
		defaultBind = ":10025"
	}
	bind := flag.String("bind", defaultBind, "HTTP listen address")
	flag.Parse()

	runCron()
	initTimers()

	server := newHTTPServer(*bind, NewAPI(scheduler))
	shutdownContext, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	log.Printf("Starting gowaiter on %s", *bind)
	if err := serveUntilShutdown(shutdownContext, server); err != nil {
		log.Fatalf("HTTP server failed: %v", err)
	}
}
