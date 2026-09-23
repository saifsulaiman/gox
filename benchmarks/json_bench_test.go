package benchmarks

import (
	"bytes"
	"encoding/json"
	"fmt"
	"testing"
)

type EventLog struct {
	ID        int64             `json:"id"`
	Source    string            `json:"source"`
	Level     string            `json:"level"`
	Message   string            `json:"message"`
	Metrics   map[string]float64 `json:"metrics"`
	Timestamp int64             `json:"timestamp"`
}

type AggregatedSummary struct {
	TotalEvents int                `json:"total_events"`
	ErrorCount  int                `json:"error_count"`
	MetricSums  map[string]float64 `json:"metric_sums"`
}

func generateJSONBatch(count int) []byte {
	var events []EventLog
	for i := 0; i < count; i++ {
		level := "INFO"
		if i%7 == 0 {
			level = "ERROR"
		}
		events = append(events, EventLog{
			ID:      int64(i),
			Source:  fmt.Sprintf("node-%d", i%8),
			Level:   level,
			Message: "event processing transaction completed",
			Metrics: map[string]float64{
				"latency_ms": float64(10 + (i % 50)),
				"cpu_util":   float64(20 + (i % 60)),
			},
			Timestamp: int64(1700000000 + i),
		})
	}
	data, _ := json.Marshal(events)
	return data
}

func processEvents(data []byte) (*AggregatedSummary, error) {
	var events []EventLog
	dec := json.NewDecoder(bytes.NewReader(data))
	if err := dec.Decode(&events); err != nil {
		return nil, err
	}

	summary := &AggregatedSummary{
		TotalEvents: len(events),
		ErrorCount:  0,
		MetricSums:  make(map[string]float64),
	}

	for i := range events {
		ev := &events[i]
		if ev.Level == "ERROR" {
			summary.ErrorCount++
		}
		for k, v := range ev.Metrics {
			summary.MetricSums[k] += v
		}
	}

	return summary, nil
}

func BenchmarkJSONStreamProcessing(b *testing.B) {
	batchData := generateJSONBatch(100)

	b.ReportAllocs()

	for b.Loop() {
		summary, err := processEvents(batchData)
		if err != nil {
			b.Fatalf("process: %v", err)
		}
		if summary.TotalEvents != 100 {
			b.Fatalf("expected 100 events, got %d", summary.TotalEvents)
		}
	}
}
