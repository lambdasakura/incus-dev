package incus

import (
	"context"
	"fmt"
	"time"
)

// waitReady waits until the instance can be provisioned.
//
// By the time commands can run, a network address may still not have been
// assigned, so it waits for that too — otherwise a step that installs a
// package fails on the very first run.
//
// It takes a Client so that it works against any implementation.
func waitReady(ctx context.Context, c Client, name string, opt WaitOptions) error {
	if opt.Timeout <= 0 {
		opt.Timeout = 60 * time.Second
	}
	if opt.NetworkTimeout <= 0 {
		opt.NetworkTimeout = 30 * time.Second
	}
	if opt.IPv4Grace <= 0 {
		opt.IPv4Grace = 5 * time.Second
	}
	if opt.Interval <= 0 {
		opt.Interval = 500 * time.Millisecond
	}

	if err := waitExec(ctx, c, name, opt); err != nil {
		return err
	}
	return waitNetwork(ctx, c, name, opt)
}

// waitExec waits until commands can run inside the container.
func waitExec(ctx context.Context, c Client, name string, opt WaitOptions) error {
	// One exec has no bound of its own: a wedged init or a half-open websocket
	// would never come back. Bound the attempts, not just the gaps between
	// them.
	parent := ctx
	ctx, cancel := context.WithTimeout(ctx, opt.Timeout)
	defer cancel()

	for {
		code, err := c.Exec(ctx, name, []string{"true"}, ExecOptions{})
		if err == nil && code == 0 {
			return nil
		}

		select {
		case <-ctx.Done():
			if parent.Err() != nil {
				// Interrupted rather than timed out.
				return parent.Err()
			}
			return notReadyError(name, opt.Timeout, code, err)
		case <-time.After(opt.Interval):
		}
	}
}

// notReadyError says why the last attempt failed.
//
// Without it there is nothing to go on beyond "it does not start", and no way
// to act on that.
func notReadyError(name string, timeout time.Duration, code int, err error) error {
	if err != nil {
		return fmt.Errorf("instance %s did not become ready within %s: %w", name, timeout, err)
	}
	return fmt.Errorf("instance %s did not become ready within %s (last exit code %d)",
		name, timeout, code)
}

// waitNetwork waits until traffic can reach the outside.
//
// On the default Incus bridge an IPv6 (ULA) address arrives first, and nothing
// gets out until IPv4 DHCP completes and a default route appears. It waits for
// the IPv4 address so that a step installing a package does not fail on the
// first run.
//
// So an IPv6-only host is not held up, it waits only IPv4Grace longer once
// IPv6 has arrived. An instance with no NIC is not waited on at all. When it
// times out with no address whatsoever, it returns ErrNetworkNotReady —
// whether that is fatal is the caller's decision, since static addressing is
// possible.
func waitNetwork(ctx context.Context, c Client, name string, opt WaitOptions) error {
	limit := time.Now().Add(opt.NetworkTimeout)
	var graceLimit time.Time

	for {
		inst, err := c.Instance(ctx, name)
		if err != nil {
			return err
		}

		switch {
		case !inst.HasNIC(), inst.HasIPv4Address():
			return nil

		case inst.HasGlobalAddress():
			// IPv6 only so far. Wait for IPv4, but not forever.
			if graceLimit.IsZero() {
				graceLimit = time.Now().Add(opt.IPv4Grace)
			}
			if time.Now().After(graceLimit) {
				return nil
			}

		case time.Now().After(limit):
			return fmt.Errorf("%w: instance %s has no address after %s",
				ErrNetworkNotReady, name, opt.NetworkTimeout)
		}

		// Whichever path we took, stop once NetworkTimeout has passed.
		if time.Now().After(limit) {
			return nil
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(opt.Interval):
		}
	}
}
