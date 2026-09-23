package main

import (
	"fmt"

	"example.com/gox-pipeline/config"
	"example.com/gox-pipeline/worker"
)

func main() {
	cfg := config.Global
	result := worker.ProcessBatch(cfg.BatchSize, 10.0)

	fmt.Printf("[%s] Processed %d records, Sum: %.1f\n",
		cfg.AppName, result.TotalProcessed, result.AggregatedSum)
}
