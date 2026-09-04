package main

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestDefaultCallbackClientConfiguration(t *testing.T) {
	client := newDefaultCallbackClient()
	t.Cleanup(client.CloseIdleConnections)

	if client.client.Timeout != 30*time.Second {
		t.Fatalf("timeout = %s, want 30s", client.client.Timeout)
	}
	if client.attempts != 3 {
		t.Fatalf("attempts = %d, want 3", client.attempts)
	}
	if client.initialBackoff != time.Second {
		t.Fatalf("initial backoff = %s, want 1s", client.initialBackoff)
	}
	if capacity := cap(client.slots); capacity != 100 {
		t.Fatalf("concurrency limit = %d, want 100", capacity)
	}

	transport, ok := client.client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport type = %T", client.client.Transport)
	}
	if transport.Proxy != nil {
		t.Fatal("proxy support must be disabled for restricted callbacks")
	}
	if transport.MaxConnsPerHost != 100 {
		t.Fatalf("MaxConnsPerHost = %d, want 100", transport.MaxConnsPerHost)
	}
	if transport.MaxResponseHeaderBytes != 64<<10 {
		t.Fatalf("MaxResponseHeaderBytes = %d, want 65536", transport.MaxResponseHeaderBytes)
	}
}

func TestAllowedCallbackAddresses(t *testing.T) {
	tests := []struct {
		address string
		allowed bool
	}{
		{address: "127.0.0.1", allowed: true},
		{address: "127.255.255.255", allowed: true},
		{address: "::1", allowed: true},
		{address: "10.1.2.3", allowed: true},
		{address: "172.16.0.1", allowed: true},
		{address: "172.31.255.255", allowed: true},
		{address: "192.168.1.1", allowed: true},
		{address: "fc00::1", allowed: true},
		{address: "fd12:3456::1", allowed: true},
		{address: "0.0.0.0", allowed: false},
		{address: "8.8.8.8", allowed: false},
		{address: "169.254.169.254", allowed: false},
		{address: "172.32.0.1", allowed: false},
		{address: "224.0.0.1", allowed: false},
		{address: "::", allowed: false},
		{address: "fe80::1", allowed: false},
		{address: "2001:4860:4860::8888", allowed: false},
	}

	for _, test := range tests {
		t.Run(test.address, func(t *testing.T) {
			address := netip.MustParseAddr(test.address)
			if got := isAllowedCallbackAddress(address); got != test.allowed {
				t.Fatalf("isAllowedCallbackAddress(%s) = %t, want %t", address, got, test.allowed)
			}
		})
	}
}

func TestRestrictedDialerRejectsMixedAndForbiddenDNSResults(t *testing.T) {
	dialer := &restrictedDialer{
		resolver: staticResolver{
			"mixed": {
				netip.MustParseAddr("172.18.0.2"),
				netip.MustParseAddr("8.8.8.8"),
			},
			"metadata": {netip.MustParseAddr("169.254.169.254")},
		},
		dialer: &recordingDialer{},
	}

	for _, host := range []string{"mixed", "metadata"} {
		_, err := dialer.resolve(context.Background(), host)
		if err == nil {
			t.Fatalf("resolve(%q) succeeded, want rejection", host)
		}
		var permanentError *permanentOutboundError
		if !errors.As(err, &permanentError) {
			t.Fatalf("resolve(%q) error %T is not permanent", host, err)
		}
	}
}

func TestRestrictedDialerResolvesAllowedHostAndLiteral(t *testing.T) {
	dialer := &restrictedDialer{
		resolver: staticResolver{
			"worker": {
				netip.MustParseAddr("172.18.0.7"),
				netip.MustParseAddr("fd00::7"),
			},
		},
		dialer: &recordingDialer{},
	}

	addresses, err := dialer.resolve(context.Background(), "worker")
	if err != nil {
		t.Fatalf("resolve(worker) error = %v", err)
	}
	want := []netip.Addr{
		netip.MustParseAddr("172.18.0.7"),
		netip.MustParseAddr("fd00::7"),
	}
	if !reflect.DeepEqual(addresses, want) {
		t.Fatalf("addresses = %#v, want %#v", addresses, want)
	}

	addresses, err = dialer.resolve(context.Background(), "127.0.0.1")
	if err != nil {
		t.Fatalf("resolve(loopback) error = %v", err)
	}
	if !reflect.DeepEqual(addresses, []netip.Addr{netip.MustParseAddr("127.0.0.1")}) {
		t.Fatalf("loopback addresses = %#v", addresses)
	}

	if _, err := dialer.resolve(context.Background(), "8.8.8.8"); err == nil {
		t.Fatal("public IP literal was accepted")
	}
}

func TestRestrictedDialerDialsValidatedIPAddress(t *testing.T) {
	recorder := &recordingDialer{}
	dialer := &restrictedDialer{
		resolver: staticResolver{
			"worker": {netip.MustParseAddr("172.18.0.7")},
		},
		dialer: recorder,
	}

	connection, err := dialer.DialContext(context.Background(), "tcp", "worker:8080")
	if err != nil {
		t.Fatalf("DialContext() error = %v", err)
	}
	_ = connection.Close()

	if got := recorder.addresses(); !reflect.DeepEqual(got, []string{"172.18.0.7:8080"}) {
		t.Fatalf("dialed addresses = %#v", got)
	}
}

func TestCallbackClientRetriesWithExponentialBackoff(t *testing.T) {
	statuses := []int{
		http.StatusInternalServerError,
		http.StatusTooManyRequests,
		http.StatusNoContent,
	}
	var calls int
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		status := statuses[calls]
		calls++
		return responseWithStatus(status), nil
	})

	var backoffs []time.Duration
	client := testCallbackClient(transport, 10)
	client.sleep = func(ctx context.Context, duration time.Duration) error {
		backoffs = append(backoffs, duration)
		return nil
	}

	result, err := client.Get(context.Background(), "http://localhost/task")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if result.Attempts != 3 || result.StatusCode != http.StatusNoContent {
		t.Fatalf("result = %#v", result)
	}
	if !reflect.DeepEqual(backoffs, []time.Duration{time.Second, 2 * time.Second}) {
		t.Fatalf("backoffs = %#v", backoffs)
	}
}

func TestCallbackClientDoesNotRetryPermanentHTTPError(t *testing.T) {
	var calls int
	client := testCallbackClient(roundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls++
		return responseWithStatus(http.StatusBadRequest), nil
	}), 10)

	result, err := client.Get(context.Background(), "http://localhost/task")
	if err == nil {
		t.Fatal("Get() succeeded, want HTTP error")
	}
	if calls != 1 || result.Attempts != 1 || result.StatusCode != http.StatusBadRequest {
		t.Fatalf("calls = %d, result = %#v", calls, result)
	}
}

func TestCallbackClientStopsAfterThreeNetworkErrors(t *testing.T) {
	var calls int
	var backoffs []time.Duration
	client := testCallbackClient(roundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls++
		return nil, errors.New("temporary network failure")
	}), 10)
	client.sleep = func(ctx context.Context, duration time.Duration) error {
		backoffs = append(backoffs, duration)
		return nil
	}

	result, err := client.Get(context.Background(), "http://localhost/task")
	if err == nil {
		t.Fatal("Get() succeeded, want network error")
	}
	if calls != 3 || result.Attempts != 3 {
		t.Fatalf("calls = %d, attempts = %d", calls, result.Attempts)
	}
	if !reflect.DeepEqual(backoffs, []time.Duration{time.Second, 2 * time.Second}) {
		t.Fatalf("backoffs = %#v", backoffs)
	}
}

func TestCallbackClientDoesNotRetryForbiddenAddress(t *testing.T) {
	var calls int
	client := testCallbackClient(roundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls++
		return nil, &permanentOutboundError{err: errors.New("forbidden")}
	}), 10)

	result, err := client.Get(context.Background(), "http://localhost/task")
	if err == nil {
		t.Fatal("Get() succeeded, want permanent error")
	}
	if calls != 1 || result.Attempts != 1 {
		t.Fatalf("calls = %d, attempts = %d", calls, result.Attempts)
	}
}

func TestCallbackClientLimitsConcurrency(t *testing.T) {
	started := make(chan struct{}, 3)
	release := make(chan struct{})
	var active atomic.Int32
	var maximum atomic.Int32
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		current := active.Add(1)
		for {
			previous := maximum.Load()
			if current <= previous || maximum.CompareAndSwap(previous, current) {
				break
			}
		}
		started <- struct{}{}
		<-release
		active.Add(-1)
		return responseWithStatus(http.StatusOK), nil
	})

	client := testCallbackClient(transport, 2)
	results := make(chan error, 3)
	for i := 0; i < 3; i++ {
		go func() {
			_, err := client.Get(context.Background(), "http://localhost/task")
			results <- err
		}()
	}

	for i := 0; i < 2; i++ {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("two callbacks did not start")
		}
	}
	select {
	case <-started:
		t.Fatal("third callback started before a slot was released")
	case <-time.After(25 * time.Millisecond):
	}

	close(release)
	for i := 0; i < 3; i++ {
		select {
		case err := <-results:
			if err != nil {
				t.Fatalf("callback error = %v", err)
			}
		case <-time.After(time.Second):
			t.Fatal("callback did not finish")
		}
	}
	if got := maximum.Load(); got != 2 {
		t.Fatalf("maximum concurrency = %d, want 2", got)
	}
}

func TestCallbackClientTimeout(t *testing.T) {
	client := testCallbackClient(roundTripFunc(func(request *http.Request) (*http.Response, error) {
		<-request.Context().Done()
		return nil, request.Context().Err()
	}), 1)
	client.client.Timeout = 10 * time.Millisecond
	client.attempts = 1

	result, err := client.Get(context.Background(), "http://localhost/task")
	if err == nil {
		t.Fatal("Get() succeeded, want timeout")
	}
	if result.Attempts != 1 {
		t.Fatalf("attempts = %d, want 1", result.Attempts)
	}
}

func TestCallbackClientHonorsContextWhileWaitingForSlot(t *testing.T) {
	client := testCallbackClient(roundTripFunc(func(request *http.Request) (*http.Response, error) {
		t.Fatal("transport must not run without a callback slot")
		return nil, nil
	}), 1)
	client.slots <- struct{}{}
	defer func() { <-client.slots }()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err := client.Get(ctx, "http://localhost/task")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Get() error = %v, want context.Canceled", err)
	}
	if result.Attempts != 0 {
		t.Fatalf("attempts = %d, want 0", result.Attempts)
	}
}

func TestCallbackRedirectValidation(t *testing.T) {
	request, _ := http.NewRequest(http.MethodGet, "file:///tmp/task", nil)
	if err := checkCallbackRedirect(request, nil); err == nil {
		t.Fatal("file redirect was accepted")
	}

	request, _ = http.NewRequest(http.MethodGet, "http://localhost/task", nil)
	previous := make([]*http.Request, maxRedirects)
	if err := checkCallbackRedirect(request, previous); err == nil {
		t.Fatal("redirect limit was not enforced")
	}
}

func TestSleepWithContext(t *testing.T) {
	if err := sleepWithContext(context.Background(), 0); err != nil {
		t.Fatalf("zero-duration sleep error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := sleepWithContext(ctx, time.Hour); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled sleep error = %v, want context.Canceled", err)
	}
}

type staticResolver map[string][]netip.Addr

func (resolver staticResolver) LookupNetIP(ctx context.Context, network string, host string) ([]netip.Addr, error) {
	addresses, ok := resolver[host]
	if !ok {
		return nil, errors.New("host not found")
	}
	return addresses, nil
}

type recordingDialer struct {
	mu     sync.Mutex
	dialed []string
}

func (dialer *recordingDialer) DialContext(ctx context.Context, network string, address string) (net.Conn, error) {
	dialer.mu.Lock()
	dialer.dialed = append(dialer.dialed, address)
	dialer.mu.Unlock()

	client, server := net.Pipe()
	_ = server.Close()
	return client, nil
}

func (dialer *recordingDialer) addresses() []string {
	dialer.mu.Lock()
	defer dialer.mu.Unlock()
	return append([]string(nil), dialer.dialed...)
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func responseWithStatus(status int) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(&emptyReader{}),
	}
}

type emptyReader struct{}

func (reader *emptyReader) Read(buffer []byte) (int, error) {
	return 0, io.EOF
}

func testCallbackClient(transport http.RoundTripper, concurrency int) *callbackClient {
	return &callbackClient{
		client: &http.Client{
			Transport: transport,
			Timeout:   defaultCallbackTimeout,
		},
		attempts:       defaultCallbackAttempts,
		initialBackoff: defaultCallbackBackoff,
		slots:          make(chan struct{}, concurrency),
		sleep:          sleepWithContext,
	}
}
