package worker

import (
	"example.com/gox-pipeline/models"
)

// IngestRecord creates a new record for ingestion (unique ownership transferred to caller).
func IngestRecord(id int, val float64) *models.Record {
	return &models.Record{
		ID:    id,
		Tag:   "sensor-stream",
		Value: val,
	}
}

// ProcessBatch aggregates an array of records and produces a summary result.
func ProcessBatch(count int, baseVal float64) *models.BatchResult {
	total := 0.0
	for i := 0; i < count; i++ {
		rec := IngestRecord(i+1, baseVal+float64(i))
		total += rec.Value
	}

	return &models.BatchResult{
		TotalProcessed: count,
		AggregatedSum:  total,
	}
}
