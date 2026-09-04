package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestAPICompatibility(t *testing.T) {
	scheduler := NewScheduler(nil)
	api := NewAPI(scheduler)

	response := requestAPI(api, http.MethodGet, "/", "")
	assertResponse(t, response, http.StatusOK, `{"maxCounter":0,"timersActive":0}`)

	response = requestAPI(
		api,
		http.MethodPost,
		"/",
		`{"tag":"demo","delay":3600,"url":"http://localhost:8080/task"}`,
	)
	assertResponse(t, response, http.StatusOK, `{"id":1}`)

	response = requestAPI(api, http.MethodGet, "/", "")
	assertResponse(t, response, http.StatusOK, `{"maxCounter":1,"timersActive":1}`)

	response = requestAPI(api, http.MethodDelete, "/", `{"tag":"demo"}`)
	assertResponse(t, response, http.StatusOK, `{"result":"ok","message":"timer deleted","code":0}`)

	response = requestAPI(api, http.MethodDelete, "/", `{"tag":"missing"}`)
	assertResponse(t, response, http.StatusOK, `{"result":"error","message":"timer not found","code":2}`)
}

func TestAPIAllowsUnknownJSONFieldsForCompatibility(t *testing.T) {
	scheduler := NewScheduler(nil)
	api := NewAPI(scheduler)
	t.Cleanup(func() { scheduler.Delete("demo") })

	response := requestAPI(
		api,
		http.MethodPost,
		"/",
		`{"tag":"demo","delay":3600,"url":"http://localhost/task","legacy":true}`,
	)
	assertResponse(t, response, http.StatusOK, `{"id":1}`)
}

func TestAPIRejectsInvalidAddTimerRequests(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "empty body", body: ""},
		{name: "malformed JSON", body: `{"tag":`},
		{name: "multiple objects", body: `{"tag":"a","delay":1,"url":"http://localhost"} {}`},
		{name: "missing tag", body: `{"delay":1,"url":"http://localhost"}`},
		{name: "empty tag", body: `{"tag":"  ","delay":1,"url":"http://localhost"}`},
		{name: "missing delay", body: `{"tag":"a","url":"http://localhost"}`},
		{name: "negative delay", body: `{"tag":"a","delay":-1,"url":"http://localhost"}`},
		{name: "excessive delay", body: fmt.Sprintf(`{"tag":"a","delay":%d,"url":"http://localhost"}`, maxDelaySeconds+1)},
		{name: "fractional delay", body: `{"tag":"a","delay":1.5,"url":"http://localhost"}`},
		{name: "missing URL", body: `{"tag":"a","delay":1}`},
		{name: "relative URL", body: `{"tag":"a","delay":1,"url":"/task"}`},
		{name: "unsupported URL scheme", body: `{"tag":"a","delay":1,"url":"file:///tmp/task"}`},
		{name: "URL credentials", body: `{"tag":"a","delay":1,"url":"http://user:pass@localhost/task"}`},
		{name: "tag too long", body: fmt.Sprintf(`{"tag":%q,"delay":1,"url":"http://localhost"}`, strings.Repeat("a", maxTagBytes+1))},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			api := NewAPI(NewScheduler(nil))
			response := requestAPI(api, http.MethodPost, "/", test.body)
			if response.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d; body = %s", response.Code, http.StatusBadRequest, response.Body.String())
			}
			if contentType := response.Header().Get("Content-Type"); contentType != "application/json" {
				t.Fatalf("Content-Type = %q, want application/json", contentType)
			}
		})
	}
}

func TestAPIRejectsOversizedBody(t *testing.T) {
	api := NewAPI(NewScheduler(nil))
	body := strings.Repeat(" ", maxRequestBodyBytes+1)
	response := requestAPI(api, http.MethodPost, "/", body)

	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusBadRequest)
	}
	if !strings.Contains(response.Body.String(), "request body exceeds") {
		t.Fatalf("body = %q, want request size error", response.Body.String())
	}
}

func TestAPIRejectsInvalidDeleteTimerRequest(t *testing.T) {
	api := NewAPI(NewScheduler(nil))

	for _, body := range []string{"", `{}`, `{"tag":""}`} {
		response := requestAPI(api, http.MethodDelete, "/", body)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("body %q: status = %d, want %d", body, response.Code, http.StatusBadRequest)
		}
	}
}

func TestAPILimitsActiveTimersWithoutBlockingReplacement(t *testing.T) {
	scheduler := NewScheduler(nil)
	api := NewAPI(scheduler)
	api.maxTimers = 1
	t.Cleanup(func() { scheduler.Delete("first") })

	first := `{"tag":"first","delay":3600,"url":"http://localhost/task"}`
	second := `{"tag":"second","delay":3600,"url":"http://localhost/task"}`

	assertResponse(t, requestAPI(api, http.MethodPost, "/", first), http.StatusOK, `{"id":1}`)
	response := requestAPI(api, http.MethodPost, "/", second)
	if response.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusTooManyRequests)
	}
	assertResponse(t, requestAPI(api, http.MethodPost, "/", first), http.StatusOK, `{"id":2}`)
}

func TestAPIMethodAndPathHandling(t *testing.T) {
	api := NewAPI(NewScheduler(nil))

	response := requestAPI(api, http.MethodPut, "/", "")
	if response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("PUT status = %d, want %d", response.Code, http.StatusMethodNotAllowed)
	}
	if allow := response.Header().Get("Allow"); allow != "GET, POST, DELETE" {
		t.Fatalf("Allow = %q", allow)
	}

	response = requestAPI(api, http.MethodGet, "/missing", "")
	if response.Code != http.StatusNotFound {
		t.Fatalf("unknown path status = %d, want %d", response.Code, http.StatusNotFound)
	}
}

func TestHTTPServerTimeouts(t *testing.T) {
	server := newHTTPServer(":0", NewAPI(NewScheduler(nil)))

	if server.ReadHeaderTimeout != 5*time.Second {
		t.Fatalf("ReadHeaderTimeout = %s", server.ReadHeaderTimeout)
	}
	if server.ReadTimeout != 10*time.Second {
		t.Fatalf("ReadTimeout = %s", server.ReadTimeout)
	}
	if server.WriteTimeout != 10*time.Second {
		t.Fatalf("WriteTimeout = %s", server.WriteTimeout)
	}
	if server.IdleTimeout != 60*time.Second {
		t.Fatalf("IdleTimeout = %s", server.IdleTimeout)
	}
	if server.MaxHeaderBytes != 1<<20 {
		t.Fatalf("MaxHeaderBytes = %d", server.MaxHeaderBytes)
	}
}

func requestAPI(handler http.Handler, method string, path string, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func assertResponse(t *testing.T, response *httptest.ResponseRecorder, status int, body string) {
	t.Helper()
	if response.Code != status {
		t.Fatalf("status = %d, want %d; body = %s", response.Code, status, response.Body.String())
	}
	if got := response.Body.String(); got != body {
		t.Fatalf("body = %q, want %q", got, body)
	}
	if contentType := response.Header().Get("Content-Type"); contentType != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", contentType)
	}
}
