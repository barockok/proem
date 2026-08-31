package metrics

import "testing"

func TestMetricsExist(t *testing.T) {
	if Requests == nil {
		t.Fatal()
	}
	if Failovers == nil {
		t.Fatal()
	}
	if Tokens == nil {
		t.Fatal()
	}
	if Latency == nil {
		t.Fatal()
	}
	if CooldownGauge == nil {
		t.Fatal()
	}
	if StickyHits == nil {
		t.Fatal()
	}
	if ConfigReloads == nil {
		t.Fatal()
	}
}
