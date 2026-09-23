package analyzer

import (
	"reflect"
	"strings"
	"testing"
	"unsafe"
)

// Target types for immortal testing
type AppConfig struct {
	AppName string
	Port    int
	Debug   bool
}

type RouteTable struct {
	Routes map[string]string
	Size   int
}

type GlobalMetrics struct {
	RequestCount int64
	ErrorCount   int64
}

type ChannelContainer struct {
	Ch chan int
}

// 1. Package-level global singleton (immutable rodata)
var GlobalDefaultAppConfig *AppConfig

func init() {
	GlobalDefaultAppConfig = &AppConfig{
		AppName: "gox-service",
		Port:    8080,
		Debug:   false,
	}
}

// 2. Read-only lookup table populated in init()
var GlobalRouteTable *RouteTable

func init() {
	GlobalRouteTable = &RouteTable{
		Routes: make(map[string]string),
		Size:   10,
	}
}

// 3. Mutable static singleton (updated at runtime)
var GlobalMetricsTracker *GlobalMetrics

func init() {
	GlobalMetricsTracker = &GlobalMetrics{
		RequestCount: 0,
		ErrorCount:   0,
	}
}

func RecordMetric(success bool) {
	if success {
		GlobalMetricsTracker.RequestCount++
	} else {
		GlobalMetricsTracker.ErrorCount++
	}
}

// 4. Adversarial Hazard: Global Channel
var GlobalChannelSink chan int

func GlobalChannelHazard() {
	ch := make(chan int, 10)
	GlobalChannelSink = ch
}

// 5. Adversarial Hazard: Global escaping to unsafe.Pointer
var GlobalUnsafeSink *AppConfig

func GlobalUnsafeHazard(cfg *AppConfig) unsafe.Pointer {
	GlobalUnsafeSink = cfg
	return unsafe.Pointer(cfg)
}

// 6. Adversarial Hazard: Global escaping to reflection
var GlobalReflectSink *AppConfig

func GlobalReflectionHazard(cfg *AppConfig) reflect.Value {
	GlobalReflectSink = cfg
	return reflect.ValueOf(cfg)
}

// 7. Non-global frame-local allocation (should remain STACK)
func LocalStackNonImmortal(x int) int {
	cfg := &AppConfig{AppName: "local", Port: x, Debug: true}
	return cfg.Port + 10
}

func TestImmortalInferenceAndAdversarialPatterns(t *testing.T) {
	cfg := Config{
		Dir:            ".",
		Patterns:       []string{"."},
		IncludeTests:   true,
		MemoryStrategy: true,
	}

	result, err := Analyze(cfg)
	if err != nil {
		t.Fatalf("Analyze failed: %v", err)
	}

	// 1. Verify GlobalDefaultAppConfig is promoted to StrategyImmortal (IMMUTABLE_RODATA)
	t.Run("GlobalDefaultAppConfig_ImmutableRodata", func(t *testing.T) {
		found := false
		for _, d := range result.Decisions {
			if strings.Contains(d.Type, "AppConfig") && d.Strategy == StrategyImmortal {
				if d.Immortal != nil && d.Immortal.IsReadOnly && d.Immortal.PlacementKind == "IMMUTABLE_RODATA" {
					found = true
					if d.Confidence != ConfidenceProven {
						t.Errorf("expected ConfidenceProven for immortal config, got %s", d.Confidence)
					}
					break
				}
			}
		}
		if !found {
			t.Error("expected GlobalDefaultAppConfig to be proven StrategyImmortal with IMMUTABLE_RODATA")
		}
	})

	// 2. Verify GlobalRouteTable initialized in init() is promoted to StrategyImmortal
	t.Run("GlobalRouteTable_InitScope", func(t *testing.T) {
		found := false
		for _, d := range result.Decisions {
			if strings.Contains(d.Type, "RouteTable") && d.Strategy == StrategyImmortal {
				found = true
				if d.Immortal == nil {
					t.Fatal("Immortal metadata is nil")
				}
				if !d.Immortal.IsReadOnly {
					t.Errorf("expected RouteTable to be read-only, got false")
				}
				break
			}
		}
		if !found {
			t.Error("expected GlobalRouteTable to be proven StrategyImmortal")
		}
	})

	// 3. Verify GlobalMetricsTracker is StrategyImmortal (STATIC_DATA, mutable)
	t.Run("GlobalMetricsTracker_MutableStatic", func(t *testing.T) {
		found := false
		for _, d := range result.Decisions {
			if strings.Contains(d.Type, "GlobalMetrics") && d.Strategy == StrategyImmortal {
				found = true
				if d.Immortal == nil {
					t.Fatal("Immortal metadata is nil")
				}
				if d.Immortal.PlacementKind != "STATIC_DATA" {
					t.Errorf("expected STATIC_DATA for mutable global metrics, got %s", d.Immortal.PlacementKind)
				}
				break
			}
		}
		if !found {
			t.Error("expected GlobalMetricsTracker to be proven StrategyImmortal with STATIC_DATA")
		}
	})

	// 4. Verify Adversarial Hazards are strictly rejected to TRACING_FALLBACK
	adversarialHazards := []struct {
		fnName string
		hazard string
	}{
		{"GlobalChannelHazard", "channel operation"},
		{"GlobalUnsafeHazard", "unsafe.Pointer"},
		{"GlobalReflectionHazard", "reflection inspection"},
	}

	for _, tc := range adversarialHazards {
		t.Run(tc.fnName, func(t *testing.T) {
			for _, d := range result.Decisions {
				if strings.Contains(d.Function, tc.fnName) {
					if d.Strategy == StrategyImmortal && d.Confidence == ConfidenceProven {
						t.Errorf("ADVERSARIAL HAZARD: %s (%s) was falsely proven IMMORTAL at %s",
							tc.fnName, tc.hazard, d.SourcePosition)
					}
					if d.Strategy != StrategyTracingFallback {
						t.Errorf("expected TRACING_FALLBACK for %s, got %s", tc.fnName, d.Strategy)
					}
				}
			}
		})
	}

	// 5. Verify local non-escaping allocation remains STACK
	t.Run("LocalStackNonImmortal", func(t *testing.T) {
		found := false
		for _, d := range result.Decisions {
			if strings.Contains(d.Function, "LocalStackNonImmortal") && strings.Contains(d.Type, "AppConfig") {
				found = true
				if d.Strategy != StrategyStack {
					t.Errorf("expected StrategyStack for local allocation, got %s", d.Strategy)
				}
				if d.Confidence != ConfidenceProven {
					t.Errorf("expected ConfidenceProven for local allocation, got %s", d.Confidence)
				}
			}
		}
		if !found {
			t.Error("LocalStackNonImmortal allocation not found in decisions")
		}
	})

	// 6. Verify StrategySummary counts reflect immortal promotions
	if result.StrategySummary == nil {
		t.Fatal("StrategySummary is nil")
	}
	if result.StrategySummary.ProvenImmortalCount < 1 {
		t.Errorf("expected at least 1 proven Immortal allocation, got %d",
			result.StrategySummary.ProvenImmortalCount)
	}
}
