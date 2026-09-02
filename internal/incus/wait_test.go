package incus

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/lxc/incus/v6/shared/api"
)

// nic gives an instance an eth0.
func nic(full *api.InstanceFull) *api.InstanceFull {
	full.ExpandedDevices = map[string]map[string]string{"eth0": {"type": "nic"}}
	return full
}

// addresses sets the assigned addresses.
func addresses(full *api.InstanceFull, addrs ...api.InstanceStateNetworkAddress) *api.InstanceFull {
	full.State = &api.InstanceState{Network: map[string]api.InstanceStateNetwork{
		"eth0": {Addresses: addrs},
		"lo":   {Addresses: []api.InstanceStateNetworkAddress{{Family: "inet", Address: "127.0.0.1", Scope: "local"}}},
	}}
	return full
}

var (
	ipv4 = api.InstanceStateNetworkAddress{Family: "inet", Address: "10.0.0.2", Scope: "global"}
	ipv6 = api.InstanceStateNetworkAddress{Family: "inet6", Address: "fd42::2", Scope: "global"}
)

// fastWait is a test configuration with the waits shortened.
func fastWait() WaitOptions {
	return WaitOptions{
		Timeout:        200 * time.Millisecond,
		NetworkTimeout: 200 * time.Millisecond,
		IPv4Grace:      20 * time.Millisecond,
		Interval:       time.Millisecond,
	}
}

// With commands runnable and IPv4 assigned, it does not wait.
//
// The count is the assertion: without it the test passes even when every
// idev up sits through the whole IPv4 grace period before provisioning.
func TestWaitReadyReturnsWhenReady(t *testing.T) {
	f := newFakeServer()
	addresses(nic(f.addInstance("dev-x", api.InstancePut{})), ipv4)
	a, _ := newAPI(f)

	if err := a.WaitReady(context.Background(), "dev-x", fastWait()); err != nil {
		t.Errorf("WaitReady() error = %v", err)
	}
	var polls int
	for _, c := range f.calls {
		if c == "GetInstanceFull" {
			polls++
		}
	}
	if polls != 1 {
		t.Errorf("polled the instance %d times, want it to return on the first look", polls)
	}
}

// It works on the defaults, so callers may omit the options.
func TestWaitReadyUsesDefaults(t *testing.T) {
	f := newFakeServer()
	addresses(nic(f.addInstance("dev-x", api.InstancePut{})), ipv4)
	a, _ := newAPI(f)

	if err := a.WaitReady(context.Background(), "dev-x", WaitOptions{}); err != nil {
		t.Errorf("WaitReady() error = %v", err)
	}
}

// exec does not work right after a start, so it retries until it does.
func TestWaitReadyRetriesUntilExecSucceeds(t *testing.T) {
	f := newFakeServer()
	addresses(nic(f.addInstance("dev-x", api.InstancePut{})), ipv4)
	f.execMeta = map[string]any{"return": float64(1)}
	a, _ := newAPI(f)

	attempts := 0
	f.beforeExec = func() {
		attempts++
		if attempts >= 3 {
			f.execMeta = map[string]any{"return": float64(0)}
		}
	}

	if err := a.WaitReady(context.Background(), "dev-x", fastWait()); err != nil {
		t.Fatalf("WaitReady() error = %v", err)
	}
	if attempts < 3 {
		t.Errorf("attempts = %d, want it to retry until it succeeds", attempts)
	}
}

// A timeout produces an error that says how long it waited.
func TestWaitReadyTimesOut(t *testing.T) {
	tests := []struct {
		name string
		exec func(*fakeServer)
		want string
	}{
		{
			name: "it runs but never exits 0",
			exec: func(f *fakeServer) { f.execMeta = map[string]any{"return": float64(1)} },
			want: "did not become ready within",
		},
		{
			name: "running it keeps failing",
			exec: func(f *fakeServer) { f.err["ExecInstance"] = errAPI },
			want: "api failed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFakeServer()
			addresses(nic(f.addInstance("dev-x", api.InstancePut{})), ipv4)
			tt.exec(f)
			a, _ := newAPI(f)

			err := a.WaitReady(context.Background(), "dev-x", fastWait())
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error = %v, want %q", err, tt.want)
			}
		})
	}
}

// Once interrupted, it returns without waiting.
func TestWaitReadyStopsOnCancel(t *testing.T) {
	f := newFakeServer()
	addresses(nic(f.addInstance("dev-x", api.InstancePut{})), ipv4)
	f.execMeta = map[string]any{"return": float64(1)}
	a, _ := newAPI(f)

	ctx, cancel := context.WithCancel(context.Background())
	f.beforeExec = cancel

	if err := a.WaitReady(ctx, "dev-x", fastWait()); !errors.Is(err, context.Canceled) {
		t.Errorf("error = %v, want context.Canceled", err)
	}
}

// An instance with no NIC is not waited on for an address.
func TestWaitReadyWithoutNIC(t *testing.T) {
	f := newFakeServer()
	f.addInstance("dev-x", api.InstancePut{})
	a, _ := newAPI(f)

	if err := a.WaitReady(context.Background(), "dev-x", fastWait()); err != nil {
		t.Errorf("WaitReady() error = %v", err)
	}
}

// It waits for IPv4 DHCP to complete.
func TestWaitReadyWaitsForIPv4(t *testing.T) {
	f := newFakeServer()
	full := addresses(nic(f.addInstance("dev-x", api.InstancePut{})))
	a, _ := newAPI(f)

	calls := 0
	f.beforeInstance = func() {
		calls++
		if calls >= 3 {
			addresses(full, ipv6, ipv4)
		}
	}

	if err := a.WaitReady(context.Background(), "dev-x", fastWait()); err != nil {
		t.Fatalf("WaitReady() error = %v", err)
	}
	if calls < 3 {
		t.Errorf("fetches = %d, want it to wait for IPv4", calls)
	}
}

// On an IPv6-only host it does not wait forever.
func TestWaitReadyGivesUpWaitingForIPv4(t *testing.T) {
	f := newFakeServer()
	addresses(nic(f.addInstance("dev-x", api.InstancePut{})), ipv6)
	a, _ := newAPI(f)

	start := time.Now()
	if err := a.WaitReady(context.Background(), "dev-x", fastWait()); err != nil {
		t.Fatalf("WaitReady() error = %v", err)
	}
	elapsed := time.Since(start)
	if elapsed > 150*time.Millisecond {
		t.Errorf("waited %v, want it not to exceed the IPv4 grace period", elapsed)
	}
	// The lower bound is the point of the grace period: a first-run apt-get
	// fails if provisioning starts before IPv4 is up.
	if elapsed < fastWait().IPv4Grace {
		t.Errorf("waited %v, want it to wait out the %v grace period", elapsed, fastWait().IPv4Grace)
	}
}

// With no address at all, the error is one the caller can act on.
func TestWaitReadyReportsNetworkNotReady(t *testing.T) {
	f := newFakeServer()
	addresses(nic(f.addInstance("dev-x", api.InstancePut{})))
	a, _ := newAPI(f)

	err := a.WaitReady(context.Background(), "dev-x", fastWait())
	if !errors.Is(err, ErrNetworkNotReady) {
		t.Errorf("error = %v, want ErrNetworkNotReady", err)
	}
}

// Interrupted while waiting for an address.
func TestWaitNetworkStopsOnCancel(t *testing.T) {
	f := newFakeServer()
	addresses(nic(f.addInstance("dev-x", api.InstancePut{})))
	a, _ := newAPI(f)

	ctx, cancel := context.WithCancel(context.Background())
	f.beforeInstance = cancel

	if err := a.WaitReady(ctx, "dev-x", fastWait()); !errors.Is(err, context.Canceled) {
		t.Errorf("error = %v, want context.Canceled", err)
	}
}

// Failing to fetch the instance state is returned as it is.
func TestWaitNetworkPropagatesError(t *testing.T) {
	f := newFakeServer()
	addresses(nic(f.addInstance("dev-x", api.InstancePut{})))
	a, _ := newAPI(f)

	f.beforeInstance = func() { f.err["GetInstanceFull"] = errAPI }

	if err := a.WaitReady(context.Background(), "dev-x", fastWait()); !errors.Is(err, errAPI) {
		t.Errorf("error = %v, want %v", err, errAPI)
	}
}

// Even with IPv6 alone assigned, it stops once NetworkTimeout has passed.
// If the grace check were the only way out, a regression would leave it waiting
// forever.
func TestWaitNetworkStopsAtTimeout(t *testing.T) {
	f := newFakeServer()
	addresses(nic(f.addInstance("dev-x", api.InstancePut{})), ipv6)
	a, _ := newAPI(f)

	opt := fastWait()
	opt.IPv4Grace = time.Hour // the grace period offers no way out
	opt.NetworkTimeout = 50 * time.Millisecond

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := a.WaitReady(ctx, "dev-x", opt); err != nil {
		t.Fatalf("WaitReady() error = %v", err)
	}
	if ctx.Err() != nil {
		t.Error("still waiting past NetworkTimeout")
	}
}

// When the instance can no longer be fetched.
func TestStopInstanceMissing(t *testing.T) {
	a, _ := newAPI(newFakeServer())

	if err := a.StopInstance(context.Background(), "dev-missing"); !errors.Is(err, ErrInstanceNotFound) {
		t.Errorf("error = %v, want ErrInstanceNotFound", err)
	}
}

// hangingClient never finishes an exec of its own accord.
type hangingClient struct{ Client }

func (hangingClient) Exec(ctx context.Context, _ string, _ []string, _ ExecOptions) (int, error) {
	<-ctx.Done()
	return 0, ctx.Err()
}

// The timeout bounds the wait even when a single attempt never comes back.
//
// The deadline was only checked between attempts, and one exec has no bound of
// its own: a wedged init or a half-open websocket left idev waiting with
// nothing said, and only Ctrl-C got out.
func TestWaitExecIsBoundedByTheTimeout(t *testing.T) {
	done := make(chan error, 1)
	go func() {
		done <- waitExec(context.Background(), hangingClient{}, "dev-x", fastWait())
	}()

	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "did not become ready") {
			t.Errorf("error = %v, want the timeout reported", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("waitExec() ignored its own timeout")
	}
}

// The timeout says why the last attempt failed, in both of its forms.
//
// Reaching them through waitExec depends on whether the deadline lands during
// an attempt or between two, so they are checked here directly.
func TestNotReadyError(t *testing.T) {
	if got := notReadyError("dev-x", time.Second, 1, nil).Error(); !strings.Contains(got, "last exit code 1") {
		t.Errorf("error = %q, want the exit code of the last attempt", got)
	}
	if got := notReadyError("dev-x", time.Second, 0, errAPI).Error(); !strings.Contains(got, "api failed") {
		t.Errorf("error = %q, want the error of the last attempt", got)
	}
}
