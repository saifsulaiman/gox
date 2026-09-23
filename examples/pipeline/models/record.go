package models

type Record struct {
	ID    int
	Tag   string
	Value float64
}

type BatchResult struct {
	TotalProcessed int
	AggregatedSum  float64
}
