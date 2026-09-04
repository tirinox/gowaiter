package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

const (
	defaultCallbackTimeout        = 30 * time.Second
	defaultCallbackAttempts       = 3
	defaultCallbackBackoff        = time.Second
	defaultMaxConcurrentCallbacks = 100
	maxDiscardResponseBytes       = 64 << 10
	maxRedirects                  = 10
)

type callbackResult struct {
	StatusCode int
	Attempts   int
}

type callbackClient struct {
	client         *http.Client
	attempts       int
	initialBackoff time.Duration
	slots          chan struct{}
	sleep          func(context.Context, time.Duration) error
}

type ipResolver interface {
	LookupNetIP(context.Context, string, string) ([]netip.Addr, error)
}

type contextDialer interface {
	DialContext(context.Context, string, string) (net.Conn, error)
}

type restrictedDialer struct {
	resolver ipResolver
	dialer   contextDialer
}

type permanentOutboundError struct {
	err error
}

func (err *permanentOutboundError) Error() string {
	return err.err.Error()
}

func (err *permanentOutboundError) Unwrap() error {
	return err.err
}

func newDefaultCallbackClient() *callbackClient {
	dialer := &restrictedDialer{
		resolver: net.DefaultResolver,
		dialer: &net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		},
	}
	transport := &http.Transport{
		Proxy:                  nil,
		DialContext:            dialer.DialContext,
		ForceAttemptHTTP2:      true,
		MaxIdleConns:           defaultMaxConcurrentCallbacks,
		MaxIdleConnsPerHost:    10,
		MaxConnsPerHost:        defaultMaxConcurrentCallbacks,
		IdleConnTimeout:        90 * time.Second,
		TLSHandshakeTimeout:    10 * time.Second,
		ExpectContinueTimeout:  time.Second,
		MaxResponseHeaderBytes: 64 << 10,
	}

	return &callbackClient{
		client: &http.Client{
			Transport:     transport,
			Timeout:       defaultCallbackTimeout,
			CheckRedirect: checkCallbackRedirect,
		},
		attempts:       defaultCallbackAttempts,
		initialBackoff: defaultCallbackBackoff,
		slots:          make(chan struct{}, defaultMaxConcurrentCallbacks),
		sleep:          sleepWithContext,
	}
}

func (client *callbackClient) Get(ctx context.Context, rawURL string) (callbackResult, error) {
	if _, err := parseHTTPURL(rawURL); err != nil {
		return callbackResult{}, &permanentOutboundError{err: err}
	}

	select {
	case client.slots <- struct{}{}:
		defer func() { <-client.slots }()
	case <-ctx.Done():
		return callbackResult{}, ctx.Err()
	}

	var result callbackResult
	var lastError error
	for attempt := 1; attempt <= client.attempts; attempt++ {
		result.Attempts = attempt

		request, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
		if err != nil {
			return result, &permanentOutboundError{err: fmt.Errorf("create callback request: %w", err)}
		}

		response, err := client.client.Do(request)
		if err == nil {
			result.StatusCode = response.StatusCode
			discardAndClose(response.Body)

			if response.StatusCode >= http.StatusOK && response.StatusCode < http.StatusMultipleChoices {
				return result, nil
			}

			lastError = fmt.Errorf("callback returned HTTP %d", response.StatusCode)
			if !isRetryableStatus(response.StatusCode) {
				return result, lastError
			}
		} else {
			lastError = fmt.Errorf("send callback request: %w", err)
			if ctx.Err() != nil {
				return result, ctx.Err()
			}
			var permanentError *permanentOutboundError
			if errors.As(err, &permanentError) {
				return result, lastError
			}
		}

		if attempt == client.attempts {
			break
		}

		backoff := client.initialBackoff * time.Duration(1<<(attempt-1))
		if err := client.sleep(ctx, backoff); err != nil {
			return result, err
		}
	}

	return result, lastError
}

func (client *callbackClient) CloseIdleConnections() {
	client.client.CloseIdleConnections()
}

func (dialer *restrictedDialer) DialContext(ctx context.Context, network string, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, &permanentOutboundError{err: fmt.Errorf("parse callback address: %w", err)}
	}

	addresses, err := dialer.resolve(ctx, host)
	if err != nil {
		return nil, err
	}

	var dialErrors []error
	for _, resolvedAddress := range addresses {
		target := net.JoinHostPort(resolvedAddress.String(), port)
		connection, err := dialer.dialer.DialContext(ctx, network, target)
		if err == nil {
			return connection, nil
		}
		dialErrors = append(dialErrors, fmt.Errorf("dial %s: %w", target, err))
	}

	return nil, errors.Join(dialErrors...)
}

func (dialer *restrictedDialer) resolve(ctx context.Context, host string) ([]netip.Addr, error) {
	if address, err := netip.ParseAddr(host); err == nil {
		address = address.Unmap()
		if !isAllowedCallbackAddress(address) {
			return nil, forbiddenAddressError(address)
		}
		return []netip.Addr{address}, nil
	}

	addresses, err := dialer.resolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return nil, fmt.Errorf("resolve callback host %q: %w", host, err)
	}
	if len(addresses) == 0 {
		return nil, fmt.Errorf("resolve callback host %q: no addresses", host)
	}

	resolved := make([]netip.Addr, 0, len(addresses))
	for _, address := range addresses {
		address = address.Unmap()
		if !isAllowedCallbackAddress(address) {
			return nil, forbiddenAddressError(address)
		}
		resolved = append(resolved, address)
	}

	return resolved, nil
}

func isAllowedCallbackAddress(address netip.Addr) bool {
	return address.IsValid() && (address.IsLoopback() || address.IsPrivate())
}

func forbiddenAddressError(address netip.Addr) error {
	return &permanentOutboundError{
		err: fmt.Errorf("callback address %s is outside the allowed private networks", address),
	}
}

func parseHTTPURL(rawURL string) (*url.URL, error) {
	parsedURL, err := url.ParseRequestURI(rawURL)
	if err != nil || !parsedURL.IsAbs() || parsedURL.Host == "" {
		return nil, errors.New("url must be an absolute HTTP or HTTPS URL")
	}
	if parsedURL.User != nil {
		return nil, errors.New("url must not contain credentials")
	}
	switch strings.ToLower(parsedURL.Scheme) {
	case "http", "https":
		return parsedURL, nil
	default:
		return nil, errors.New("url must use HTTP or HTTPS")
	}
}

func checkCallbackRedirect(request *http.Request, previous []*http.Request) error {
	if len(previous) >= maxRedirects {
		return &permanentOutboundError{err: fmt.Errorf("stopped after %d redirects", maxRedirects)}
	}
	if _, err := parseHTTPURL(request.URL.String()); err != nil {
		return &permanentOutboundError{err: fmt.Errorf("invalid redirect URL: %w", err)}
	}
	return nil
}

func isRetryableStatus(status int) bool {
	return status == http.StatusRequestTimeout ||
		status == http.StatusTooEarly ||
		status == http.StatusTooManyRequests ||
		status >= http.StatusInternalServerError
}

func discardAndClose(body io.ReadCloser) {
	_, _ = io.Copy(io.Discard, io.LimitReader(body, maxDiscardResponseBytes))
	_ = body.Close()
}

func sleepWithContext(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()

	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
