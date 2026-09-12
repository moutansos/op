package config

import (
	"testing"
	"time"
)

func TestStateConfigurationMigration(t *testing.T) {
	cfg, warnings, err := Migrate([]byte(`{"server":{"state":{"instanceId":"child-one","parentUrl":"https://parent/v1/op/state","parentToken":"secret","refreshInterval":"2s","heartbeatInterval":"10s","staleAfter":"1m"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 {
		t.Fatalf("warnings: %v", warnings)
	}
	s := cfg.Server.State
	if s.InstanceID != "child-one" || s.ParentURL != "https://parent/v1/op/state" || s.ParentToken != "secret" || s.RefreshInterval.Duration != 2*time.Second || s.HeartbeatInterval.Duration != 10*time.Second || s.StaleAfter.Duration != time.Minute {
		t.Fatalf("lost state config: %+v", s)
	}
	for _, endpoint := range []string{"file:///tmp/parent", "https://user:pass@parent/state", "http://parent/state#fragment"} {
		cfg.Server.State.ParentURL = endpoint
		if err := validateServer(cfg.Server); err == nil {
			t.Fatalf("accepted %q", endpoint)
		}
	}
}
