// Package incus is where Incus instance lifecycle operations are concentrated.
//
// It is the boundary that keeps the CLI layer from assembling Incus calls
// itself (spec 05-incus.md 5.7).
package incus

import (
	"cmp"
	"context"
	"errors"
	"io"
	"slices"
	"time"
)

// ErrInstanceNotFound reports that the instance does not exist.
var ErrInstanceNotFound = errors.New("instance not found")

// ErrInstanceExists reports that the instance was created by someone else
// while idev was creating it.
var ErrInstanceExists = errors.New("instance already exists")

// ErrSnapshotExists reports that the instance already has a snapshot of that
// name.
var ErrSnapshotExists = errors.New("snapshot already exists")

// InstanceChange is what one write does to an instance.
//
// An empty change writes nothing. SetDevices replaces a device rather than
// merging into it: every device idev names here is one it owns entirely, so
// the declaration is the whole truth about it (spec 05-incus.md 5.4.4).
type InstanceChange struct {
	SetConfig     map[string]string
	UnsetConfig   []string
	SetDevices    map[string]Device
	RemoveDevices []string
}

// Empty reports whether the change would write nothing.
func (c InstanceChange) Empty() bool {
	return len(c.SetConfig) == 0 && len(c.UnsetConfig) == 0 &&
		len(c.SetDevices) == 0 && len(c.RemoveDevices) == 0
}

// ErrChanged reports that the instance was changed by something else between
// the read the caller is working from and the write it asked for.
//
// idev computes what to record -- which volumes are its own, which devices,
// whether a restart is owed -- from a reading of the instance, several calls
// before it writes the answer back. Another idev in another terminal reads and
// writes in the same window, and the later write silently erases what the
// earlier one recorded: a volume drops out of the record and no command names
// it again. Refusing the write is what turns that into something the user can
// see and act on.
var ErrChanged = errors.New("the instance changed while idev was working on it")

// ErrOutcomeUnknown reports that a request reached the daemon and idev stopped
// waiting before it finished, so whether it took effect is not known.
//
// The daemon does not stop because idev did: interrupting the wait for a
// delete leaves it deleting. Callers that must know whether the instance
// survived have to distinguish this from the failures that leave everything
// in place -- a lookup, a force stop, a rejected request -- and a lookup made
// afterwards cannot, because the operation is still running when it answers.
var ErrOutcomeUnknown = errors.New("the daemon may carry it out anyway")

// ErrPoolNotFound reports that the storage pool itself is not there.
//
// It is not the same as a missing volume: no volume can exist on a pool that
// has no row, so there is nothing to create, delete or keep a record of.
var ErrPoolNotFound = errors.New("storage pool not found")

// ErrNetworkNotReady reports that no network address was assigned to the
// instance.
//
// Some configurations never show an address — static addressing, say — so
// whether this is fatal is the caller's decision.
var ErrNetworkNotReady = errors.New("network address not assigned")

// Device is an Incus device definition: keys and values, passed through.
type Device map[string]string

// Type returns the device type.
func (d Device) Type() string { return d["type"] }

// Instance is the state of an Incus instance.
type Instance struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	// LastUsedAt is when the instance was last started. It is how idev tells
	// that something restarted it since a change was applied.
	LastUsedAt time.Time         `json:"last_used_at"`
	Profiles   []string          `json:"profiles"`
	Config     map[string]string `json:"config"`
	Devices    map[string]Device `json:"devices"`
	// ExpandedDevices are the effective devices, including those from profiles.
	ExpandedDevices map[string]Device `json:"expanded_devices"`
	State           *InstanceState    `json:"state"`
	// ETag identifies this reading of the instance. Handing it back to
	// UpdateConfig refuses the write if anything has changed the instance
	// since, which is how a second idev is stopped from overwriting what this
	// one recorded. It is not part of the instance's own state, so it is not
	// serialised.
	ETag string `json:"-"`
}

// InstanceState is an instance's run-time state.
type InstanceState struct {
	Network map[string]NetworkState `json:"network"`
}

// NetworkState is the state of a network interface.
type NetworkState struct {
	Addresses []NetworkAddress `json:"addresses"`
}

// NetworkAddress is an assigned address.
type NetworkAddress struct {
	Family  string `json:"family"`
	Address string `json:"address"`
	Scope   string `json:"scope"`
}

// IsRunning reports whether the instance is running.
func (i *Instance) IsRunning() bool { return i.Status == "Running" }

// IsStopped reports whether the instance is stopped.
//
// Intermediate states such as Frozen and Starting count as not stopped.
func (i *Instance) IsStopped() bool { return i.Status == "Stopped" }

// HasNIC reports whether it has a network interface.
func (i *Instance) HasNIC() bool {
	for _, dev := range i.ExpandedDevices {
		if dev.Type() == "nic" {
			return true
		}
	}
	return false
}

// GlobalAddresses returns the global addresses that were assigned.
func (i *Instance) GlobalAddresses() []NetworkAddress {
	if i.State == nil {
		return nil
	}

	var out []NetworkAddress
	for name, net := range i.State.Network {
		if name == loopbackInterface {
			continue
		}
		for _, addr := range net.Addresses {
			if addr.Scope == globalScope {
				out = append(out, addr)
			}
		}
	}
	return out
}

// Addresses returns every global address, in the order idev hands them out
// (spec 04-cli.md 4.4.1).
//
// IPv4 first, then interface name. The state arrives as a map, so without an
// order two runs can answer differently -- and the caller of `idev ip` is a
// command substitution that cannot see it happen.
func (i *Instance) Addresses() []InterfaceAddress {
	if i.State == nil {
		return nil
	}

	var out []InterfaceAddress
	for name, net := range i.State.Network {
		if name == loopbackInterface {
			continue
		}
		for _, addr := range net.Addresses {
			if addr.Scope == globalScope {
				out = append(out, InterfaceAddress{Interface: name, NetworkAddress: addr})
			}
		}
	}

	slices.SortFunc(out, func(a, b InterfaceAddress) int {
		if a.Family != b.Family {
			// inet before inet6: on the default Incus bridge the IPv6 address
			// arrives first and nothing reaches the outside until IPv4 is up.
			if a.Family == familyIPv4 {
				return -1
			}
			if b.Family == familyIPv4 {
				return 1
			}
		}
		if c := cmp.Compare(a.Interface, b.Interface); c != 0 {
			return c
		}
		return cmp.Compare(a.Address, b.Address)
	})
	return out
}

// PrimaryAddress is the address to reach the instance on, or "" when it has
// none.
func (i *Instance) PrimaryAddress() string {
	addrs := i.Addresses()
	if len(addrs) == 0 {
		return ""
	}
	return addrs[0].Address
}

// InterfaceAddress is an address together with the interface carrying it.
type InterfaceAddress struct {
	Interface string `json:"interface"`
	NetworkAddress
}

const (
	loopbackInterface = "lo"
	globalScope       = "global"
	familyIPv4        = "inet"
)

// HasGlobalAddress reports whether any global address was assigned.
func (i *Instance) HasGlobalAddress() bool {
	return len(i.GlobalAddresses()) > 0
}

// HasIPv4Address reports whether a global IPv4 address was assigned.
//
// On the default Incus bridge an IPv6 (ULA) address arrives first, and at that
// point there is still no default route. Traffic only reaches the outside once
// IPv4 is up, so this is what readiness is judged by.
func (i *Instance) HasIPv4Address() bool {
	for _, addr := range i.GlobalAddresses() {
		if addr.Family == familyIPv4 {
			return true
		}
	}
	return false
}

// InstanceSpec describes an instance to create.
type InstanceSpec struct {
	Name  string
	Image string
	// Profiles names the profiles to apply.
	Profiles []string
	// NoProfiles applies no profile at all, matching profiles: [].
	NoProfiles bool
	Config     map[string]string
	// Devices are set at creation time. Without a profile, the root disk has
	// to be there from the start.
	Devices map[string]Device
}

// ExecOptions are the options for running a command inside the container.
type ExecOptions struct {
	// Env holds user-supplied environment variables. They may be secrets, so
	// their values are hidden when displayed.
	Env map[string]string
	// PublicEnv holds the environment variables idev injects. They help with
	// diagnosis, so they are shown.
	PublicEnv map[string]string
	Cwd       string
	// User and Group are the numeric ids to run as. The Incus exec API takes
	// no names, so resolving one is the caller's job; without Group the
	// process runs in root's group whatever User says.
	User  string
	Group string
	// TTY allocates a pseudo-terminal and hands the standard streams through.
	TTY bool
	// Term is the host terminal type (TERM), passed to the container only when
	// a TTY is allocated. Without it, vim and less cannot tell what terminal
	// they are on.
	Term   string
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
}

// Snapshot is a snapshot of an instance.
type Snapshot struct {
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
}

// WaitOptions controls how long to wait for an instance to become ready.
type WaitOptions struct {
	// Timeout is how long to wait until commands can run.
	Timeout time.Duration
	// NetworkTimeout is how long to wait for a network address.
	NetworkTimeout time.Duration
	// IPv4Grace is how long to keep waiting for IPv4 once IPv6 alone has
	// arrived. Keep it short so an IPv6-only host is not held up.
	IPv4Grace time.Duration
	Interval  time.Duration
}

// Client is the interface to Incus. Tests replace it with a fake.
//
// It lists only the operations actually used, so that replacing the
// implementation stays cheap. Existence is checked with Instance and
// errors.Is(ErrInstanceNotFound).
type Client interface {
	Instance(ctx context.Context, name string) (*Instance, error)
	// ListInstances lists the containers in the project, with their config, so
	// an instance idev made under a name it no longer derives can be found.
	ListInstances(ctx context.Context) ([]Instance, error)

	CreateInstance(ctx context.Context, spec InstanceSpec) error
	StartInstance(ctx context.Context, name string) error
	StopInstance(ctx context.Context, name string) error
	DeleteInstance(ctx context.Context, name string) error

	// UpdateInstance applies a whole set of changes in one write.
	//
	// etag, when not empty, is the one Instance returned: the write is refused
	// with ErrChanged if the instance has changed since.
	//
	// Config and devices go together because they are one decision taken from
	// one reading of the instance. Split into separate writes, the second
	// would be judged against an etag the first had already spent -- and a
	// failure between them leaves a record describing devices the instance
	// does not have, or devices nothing records.
	UpdateInstance(ctx context.Context, name string, change InstanceChange, etag string) error

	// ProfileNames lists the profiles on the host.
	//
	// The whole list rather than one name at a time: Incus has no call that
	// answers for a single profile, so every such answer is this list
	// filtered, and asking per name fetches it again for each one.
	ProfileNames(ctx context.Context) ([]string, error)

	// CheckImage reports whether an image reference resolves, without
	// creating anything.
	CheckImage(ctx context.Context, ref string) error

	VolumeExists(ctx context.Context, pool, name string) (bool, error)
	CreateVolume(ctx context.Context, pool, name string, config map[string]string) error
	DeleteVolume(ctx context.Context, pool, name string) error

	CreateSnapshot(ctx context.Context, instance, snapshot string) error
	Snapshots(ctx context.Context, instance string) ([]Snapshot, error)
	RestoreSnapshot(ctx context.Context, instance, snapshot string) error
	DeleteSnapshot(ctx context.Context, instance, snapshot string) error

	Exec(ctx context.Context, name string, argv []string, opt ExecOptions) (int, error)
	WaitReady(ctx context.Context, name string, opt WaitOptions) error
}
