package benchmarks

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHTTPHandlerCorrectness(t *testing.T) {
	reqBody, _ := json.Marshal(RequestPayload{
		UserID:    99,
		Action:    "test_ping",
		Tags:      []string{"unit_test"},
		Timestamp: 1726000000,
	})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/profile", bytes.NewReader(reqBody))
	rec := httptest.NewRecorder()
	handleAPIRequest(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}

	var resp ResponsePayload
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if resp.UserID != 99 || resp.EchoTag != "unit_test" || !resp.Processed {
		t.Errorf("unexpected response payload: %+v", resp)
	}
}

func TestJSONProcessingCorrectness(t *testing.T) {
	batchData := generateJSONBatch(14)
	summary, err := processEvents(batchData)
	if err != nil {
		t.Fatalf("failed to process events: %v", err)
	}
	if summary.TotalEvents != 14 {
		t.Errorf("expected 14 events, got %d", summary.TotalEvents)
	}
	if summary.ErrorCount != 2 { // 0, 7 are errors (0 % 7 == 0, 7 % 7 == 0)
		t.Errorf("expected 2 errors, got %d", summary.ErrorCount)
	}
}

func TestPipelineCorrectness(t *testing.T) {
	total := runWorkerPipeline(2, 10)
	// items: 1..10, value = i+1. Sum = 1+2+...+10 = 55. Each value * 3 = 165.
	if total != 165 {
		t.Errorf("expected total 165, got %d", total)
	}
}

func TestBinaryTreeCorrectness(t *testing.T) {
	root := buildTree(3, 1)
	// depth 3:
	// root: 1
	// left: 2 (left: 4, right: 5)
	// right: 3 (left: 6, right: 7)
	// sum = 1 + 2 + 4 + 5 + 3 + 6 + 7 = 28
	sum := sumTree(root)
	if sum != 28 {
		t.Errorf("expected sum 28, got %d", sum)
	}
}
