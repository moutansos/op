//go:build linux

package stats

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestIntegrationWindowsHostSamplerUnderWSL(t *testing.T) {
	if os.Getenv("OP_WSL_INTEGRATION") != "1" {
		t.Skip("set OP_WSL_INTEGRATION=1 inside WSL to sample the real Windows host")
	}
	source := newWindowsHostSource()
	if source == nil {
		t.Fatal("not running inside WSL")
	}
	deadline := time.Now().Add(20 * time.Second)
	for {
		sample := source.Sample(context.Background())
		if sample.Error != "" {
			t.Fatalf("sample error: %s", sample.Error)
		}
		if sample.CPUAvailable {
			if sample.CPUPercent < 0 || sample.CPUPercent > 100 || sample.LogicalCPUs < 1 ||
				sample.MemoryTotal == 0 || sample.MemoryUsed > sample.MemoryTotal || sample.UptimeSeconds == 0 {
				t.Fatalf("implausible sample: %+v", sample)
			}
			t.Logf("Windows host: %+v", sample)
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("no CPU sample within 20s; last = %+v", sample)
		}
		time.Sleep(500 * time.Millisecond)
	}
}
