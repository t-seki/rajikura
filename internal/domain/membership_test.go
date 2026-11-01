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

func TestMembershipAreaFreeAndMember(t *testing.T) {
	for m, want := range map[Membership][2]bool{
		MembershipFree:       {false, false},
		MembershipAreaFree:   {true, true},
		MembershipTimeFree30: {false, true},
		MembershipDouble:     {true, true},
	} {
		if got := [2]bool{m.AreaFree(), m.Member()}; got != want {
			t.Errorf("%s: AreaFree・Member = %v, want %v", m, got, want)
		}
	}
}
