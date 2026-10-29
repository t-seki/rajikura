package domain

import "testing"

func TestMaxAvailableDays(t *testing.T) {
	for m, want := range map[Membership]int{
		MembershipFree:       7,
		MembershipAreaFree:   7,
		MembershipTimeFree30: 30,
		MembershipDouble:     30,
	} {
		if got := MaxAvailableDays(m); got != want {
			t.Errorf("MaxAvailableDays(%s) = %d, want %d", m, got, want)
		}
	}
}
