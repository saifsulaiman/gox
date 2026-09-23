package config

type PipelineConfig struct {
	AppName   string
	BatchSize int
	Workers   int
}

var Global *PipelineConfig

func init() {
	Global = &PipelineConfig{
		AppName:   "GOX-StreamProcessor",
		BatchSize: 50,
		Workers:   4,
	}
}
