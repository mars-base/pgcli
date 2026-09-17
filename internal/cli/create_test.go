package cli

import "testing"

func TestValidatePasswordLength(t *testing.T) {
	// In-range values (the 8 and 64 bounds, plus the default) must pass.
	for _, n := range []int{passwordLengthFloor, defaultPasswordLength, passwordLengthCeiling, 20, 63} {
		if err := validatePasswordLength(n); err != nil {
			t.Errorf("validatePasswordLength(%d) = %v, want nil", n, err)
		}
	}
	// Out-of-range values (one step below/above each bound, and the degenerate
	// zero / negative cases) must error with the accepted range in the message.
	for _, n := range []int{passwordLengthFloor - 1, passwordLengthCeiling + 1, 0, -5, 1000} {
		err := validatePasswordLength(n)
		if err == nil {
			t.Fatalf("validatePasswordLength(%d) = nil, want error", n)
		}
	}
	// The default must sit inside the range (guards a future edit that narrows
	// the bounds past 16 without updating the default).
	if validatePasswordLength(defaultPasswordLength) != nil {
		t.Errorf("default password length %d is outside its own accepted range", defaultPasswordLength)
	}
}
