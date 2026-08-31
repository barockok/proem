package pool

import "testing"

func TestIsEnabled(t *testing.T) {
	m := Member{ID: "a"}
	if !m.IsEnabled() {
		t.Fatal("nil should be enabled")
	}
	f := false
	m.Enabled = &f
	if m.IsEnabled() {
		t.Fatal("false")
	}
	tr := true
	m.Enabled = &tr
	if !m.IsEnabled() {
		t.Fatal("true")
	}
}
