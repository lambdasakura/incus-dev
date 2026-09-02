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

// The address idev hands to ssh is chosen the same way every run (spec
// 04-cli.md 4.4.1).
func TestPrimaryAddress(t *testing.T) {
	state := func(nets map[string][]NetworkAddress) *InstanceState {
		out := &InstanceState{Network: map[string]NetworkState{}}
		for name, addrs := range nets {
			out.Network[name] = NetworkState{Addresses: addrs}
		}
		return out
	}

	tests := []struct {
		name  string
		state *InstanceState
		want  string
	}{
		{"no state at all", nil, ""},
		{
			"the one address there is",
			state(map[string][]NetworkAddress{
				"eth0": {{Family: "inet", Address: "10.0.0.2", Scope: "global"}},
			}),
			"10.0.0.2",
		},
		{
			// The addresses are chosen so that sorting them as strings puts
			// the IPv6 one first: otherwise the family rule and the tiebreak
			// agree, and a test cannot tell which one decided.
			"IPv4 wins over IPv6",
			state(map[string][]NetworkAddress{
				"eth0": {
					{Family: "inet6", Address: "2001:db8::1", Scope: "global"},
					{Family: "inet", Address: "203.0.113.5", Scope: "global"},
				},
			}),
			"203.0.113.5",
		},
		{
			"IPv6 when that is all there is",
			state(map[string][]NetworkAddress{
				"eth0": {{Family: "inet6", Address: "fd42::1", Scope: "global"}},
			}),
			"fd42::1",
		},
		{
			// Likewise: eth0 carries the higher address, so ordering by
			// address alone would answer eth1.
			"two NICs settle on the lower interface name",
			state(map[string][]NetworkAddress{
				"eth1": {{Family: "inet", Address: "10.0.1.2", Scope: "global"}},
				"eth0": {{Family: "inet", Address: "10.0.9.2", Scope: "global"}},
			}),
			"10.0.9.2",
		},
		{
			"family decides before the interface does",
			state(map[string][]NetworkAddress{
				"eth0": {{Family: "inet6", Address: "2001:db8::1", Scope: "global"}},
				"eth1": {{Family: "inet", Address: "203.0.113.5", Scope: "global"}},
			}),
			"203.0.113.5",
		},
		{
			"loopback is not an address to hand out",
			state(map[string][]NetworkAddress{
				"lo": {{Family: "inet", Address: "127.0.0.1", Scope: "local"}},
			}),
			"",
		},
		{
			"a link-local address is not reachable from the host",
			state(map[string][]NetworkAddress{
				"eth0": {{Family: "inet6", Address: "fe80::1", Scope: "link"}},
			}),
			"",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Ten runs: the addresses come out of a map, and a caller
			// substituting this into ssh cannot see it change.
			for range 10 {
				inst := &Instance{Name: "dev-x", Status: "Running", State: tt.state}
				if got := inst.PrimaryAddress(); got != tt.want {
					t.Fatalf("PrimaryAddress() = %q, want %q", got, tt.want)
				}
			}
		})
	}
}
