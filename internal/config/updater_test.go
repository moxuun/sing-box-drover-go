package config

import (
	"math"
	"testing"
	"time"
)

func TestClampUpdateIntervalNeverOverflows(t *testing.T) {
	if got := ClampUpdateInterval(1); got != MinimumUpdateInterval {
		t.Fatalf("short interval = %s, want %s", got, MinimumUpdateInterval)
	}
	if got := ClampUpdateInterval(math.MaxInt32); got <= 0 {
		t.Fatalf("maximum interval overflowed: %s", got)
	}
	if got := ClampUpdateInterval(math.MaxInt32); got > maxDuration {
		t.Fatalf("maximum interval exceeds duration bound: %s", got)
	}
	if got := ClampUpdateInterval(15); got != 15*time.Minute {
		t.Fatalf("normal interval = %s, want 15m", got)
	}
}
