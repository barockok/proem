package router

import "testing"

func TestErrorStrings(t *testing.T) {
	if ErrNoMember.Error() != "no pool members" {
		t.Fatalf("ErrNoMember: %q", ErrNoMember.Error())
	}
	if ErrNoHealthy.Error() != "all members in cooldown" {
		t.Fatalf("ErrNoHealthy: %q", ErrNoHealthy.Error())
	}
}
