package benchmarks

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
)

// RequestPayload represents an incoming JSON request.
type RequestPayload struct {
	UserID    int      `json:"user_id"`
	Action    string   `json:"action"`
	Tags      []string `json:"tags"`
	Timestamp int64    `json:"timestamp"`
}

// ResponsePayload represents the outgoing JSON response.
type ResponsePayload struct {
	Status    string            `json:"status"`
	UserID    int               `json:"user_id"`
	EchoTag   string            `json:"echo_tag"`
	Processed bool              `json:"processed"`
	Metadata  map[string]string `json:"metadata"`
}

// UserContext simulates per-request state tracking.
type UserContext struct {
	TraceID string
	Payload RequestPayload
}

func handleAPIRequest(w http.ResponseWriter, r *http.Request) {
	var payload RequestPayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	ctx := &UserContext{
		TraceID: "trace-" + strconv.Itoa(payload.UserID),
		Payload: payload,
	}

	echoTag := "none"
	if len(ctx.Payload.Tags) > 0 {
		echoTag = ctx.Payload.Tags[0]
	}

	resp := &ResponsePayload{
		Status:    "success",
		UserID:    ctx.Payload.UserID,
		EchoTag:   echoTag,
		Processed: true,
		Metadata: map[string]string{
			"trace_id": ctx.TraceID,
			"source":   "gox_benchmark",
		},
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func BenchmarkHTTPServerStandard(b *testing.B) {
	reqBody, _ := json.Marshal(RequestPayload{
		UserID:    42,
		Action:    "query_profile",
		Tags:      []string{"production", "v1.0", "standard_lib"},
		Timestamp: 1726000000,
	})

	handler := http.HandlerFunc(handleAPIRequest)

	b.ReportAllocs()

	for b.Loop() {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/profile", bytes.NewReader(reqBody))
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			b.Fatalf("expected 200 OK, got %d", rec.Code)
		}
	}
}

func BenchmarkHTTPServerParallel(b *testing.B) {
	reqBody, _ := json.Marshal(RequestPayload{
		UserID:    101,
		Action:    "batch_event",
		Tags:      []string{"concurrent", "stream"},
		Timestamp: 1726000000,
	})

	handler := http.HandlerFunc(handleAPIRequest)

	b.ReportAllocs()
	b.ResetTimer()

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			req := httptest.NewRequest(http.MethodPost, "/api/v1/profile", bytes.NewReader(reqBody))
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				b.Fatalf("expected 200 OK, got %d", rec.Code)
			}
		}
	})
}
