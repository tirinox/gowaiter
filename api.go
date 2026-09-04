package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	maxRequestBodyBytes = 64 << 10
	maxTagBytes         = 256
	maxURLBytes         = 4096
	maxDelaySeconds     = int64(365 * 24 * 60 * 60)
	defaultMaxTimers    = 10_000
)

type API struct {
	scheduler *Scheduler
	maxTimers int
}

type addTimerRequest struct {
	Delay *int64  `json:"delay"`
	Tag   *string `json:"tag"`
	URL   *string `json:"url"`
}

type deleteTimerRequest struct {
	Tag *string `json:"tag"`
}

type addTimerResponse struct {
	ID int `json:"id"`
}

type resultResponse struct {
	Result  string `json:"result"`
	Message string `json:"message"`
	Code    int    `json:"code"`
}

type infoResponse struct {
	MaxCounter   int `json:"maxCounter"`
	TimersActive int `json:"timersActive"`
}

func NewAPI(scheduler *Scheduler) *API {
	return &API{
		scheduler: scheduler,
		maxTimers: defaultMaxTimers,
	}
}

func (api *API) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}

	switch r.Method {
	case http.MethodGet:
		api.handleInfo(w)
	case http.MethodPost:
		api.handleAddTimer(w, r)
	case http.MethodDelete:
		api.handleDeleteTimer(w, r)
	default:
		w.Header().Set("Allow", "GET, POST, DELETE")
		writeJSON(w, http.StatusMethodNotAllowed, errorResponse(1, "method not allowed"))
	}
}

func (api *API) handleAddTimer(w http.ResponseWriter, r *http.Request) {
	var input addTimerRequest
	if err := decodeJSON(w, r, &input); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse(1, err.Error()))
		return
	}

	tag, delay, targetURL, err := validateAddTimerRequest(input)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse(1, err.Error()))
		return
	}

	timer, ok := api.scheduler.AddWithLimit(tag, delay, targetURL, api.maxTimers)
	if !ok {
		writeJSON(w, http.StatusTooManyRequests, errorResponse(1, "active timer limit reached"))
		return
	}

	fmt.Printf("SetTimer id = %d for %d sec; tag = %v\n", timer.id, *input.Delay, timer.tag)
	writeJSON(w, http.StatusOK, addTimerResponse{ID: timer.id})
}

func (api *API) handleDeleteTimer(w http.ResponseWriter, r *http.Request) {
	var input deleteTimerRequest
	if err := decodeJSON(w, r, &input); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse(1, err.Error()))
		return
	}

	if input.Tag == nil || strings.TrimSpace(*input.Tag) == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse(1, "tag is required"))
		return
	}
	if len(*input.Tag) > maxTagBytes || !utf8.ValidString(*input.Tag) {
		writeJSON(w, http.StatusBadRequest, errorResponse(1, "tag is invalid"))
		return
	}

	if !api.scheduler.Delete(*input.Tag) {
		// Keep the legacy HTTP status and response body for existing clients.
		writeJSON(w, http.StatusOK, errorResponse(2, "timer not found"))
		return
	}

	writeJSON(w, http.StatusOK, successResponse(0, "timer deleted"))
}

func (api *API) handleInfo(w http.ResponseWriter) {
	maxCounter, timersActive := api.scheduler.Stats()
	writeJSON(w, http.StatusOK, infoResponse{
		MaxCounter:   maxCounter,
		TimersActive: timersActive,
	})
}

func decodeJSON(w http.ResponseWriter, r *http.Request, destination any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)
	decoder := json.NewDecoder(r.Body)

	if err := decoder.Decode(destination); err != nil {
		var maxBytesError *http.MaxBytesError
		if errors.As(err, &maxBytesError) {
			return fmt.Errorf("request body exceeds %d bytes", maxRequestBodyBytes)
		}
		return errors.New("invalid JSON body")
	}

	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("request body must contain one JSON object")
	}

	return nil
}

func validateAddTimerRequest(input addTimerRequest) (string, time.Duration, string, error) {
	if input.Tag == nil || strings.TrimSpace(*input.Tag) == "" {
		return "", 0, "", errors.New("tag is required")
	}
	if len(*input.Tag) > maxTagBytes || !utf8.ValidString(*input.Tag) {
		return "", 0, "", errors.New("tag is invalid")
	}
	if input.Delay == nil {
		return "", 0, "", errors.New("delay is required")
	}
	if *input.Delay < 0 || *input.Delay > maxDelaySeconds {
		return "", 0, "", fmt.Errorf("delay must be between 0 and %d seconds", maxDelaySeconds)
	}
	if input.URL == nil || *input.URL == "" {
		return "", 0, "", errors.New("url is required")
	}
	if len(*input.URL) > maxURLBytes || !utf8.ValidString(*input.URL) {
		return "", 0, "", errors.New("url is invalid")
	}

	parsedURL, err := url.ParseRequestURI(*input.URL)
	if err != nil || !parsedURL.IsAbs() || parsedURL.Host == "" {
		return "", 0, "", errors.New("url must be an absolute HTTP or HTTPS URL")
	}
	if parsedURL.User != nil {
		return "", 0, "", errors.New("url must not contain credentials")
	}
	switch strings.ToLower(parsedURL.Scheme) {
	case "http", "https":
	default:
		return "", 0, "", errors.New("url must use HTTP or HTTPS")
	}

	return *input.Tag, time.Duration(*input.Delay) * time.Second, *input.URL, nil
}

func successResponse(code int, message string) resultResponse {
	return resultResponse{Result: "ok", Message: message, Code: code}
}

func errorResponse(code int, message string) resultResponse {
	return resultResponse{Result: "error", Message: message, Code: code}
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	data, err := json.Marshal(payload)
	if err != nil {
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_, _ = w.Write(data)
}
