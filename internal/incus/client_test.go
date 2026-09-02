package incus

import "testing"

// The address checks hold up on an instance whose state was never fetched.
func TestInstanceAddressesWithoutState(t *testing.T) {
	inst := &Instance{Name: "dev-x"}

	if got := inst.GlobalAddresses(); got != nil {
		t.Errorf("GlobalAddresses() = %v, want nil", got)
	}
	if inst.HasGlobalAddress() || inst.HasIPv4Address() {
		t.Error("reported an address despite there being no state")
	}
}
