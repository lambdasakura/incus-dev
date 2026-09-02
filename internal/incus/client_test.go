package incus

import "testing"

// 状態を取得していないinstanceでもアドレス判定が成り立つこと
func TestInstanceAddressesWithoutState(t *testing.T) {
	inst := &Instance{Name: "dev-x"}

	if got := inst.GlobalAddresses(); got != nil {
		t.Errorf("GlobalAddresses() = %v, want nil", got)
	}
	if inst.HasGlobalAddress() || inst.HasIPv4Address() {
		t.Error("状態が無いのにアドレスがあると判定している")
	}
}
