package ocsrc

import (
	"testing"
	"time"
)

// The product default must meet the spec's "detect a silent disconnect within 45 seconds":
// at most 45 seconds,
// and long enough for at least two 15-second heartbeat intervals (so one delay is not misread).
func TestDefaultIdleTimeoutMeetsThe45SecondTarget(t *testing.T) {
	const heartbeat = 15 * time.Second
	if DefaultIdleTimeout > 45*time.Second {
		t.Fatalf("default idle timeout %v exceeds the 45-second target", DefaultIdleTimeout)
	}
	if DefaultIdleTimeout < 2*heartbeat {
		t.Fatalf("default idle timeout %v cannot fit two heartbeat intervals", DefaultIdleTimeout)
	}
}
