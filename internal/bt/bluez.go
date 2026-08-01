package bt

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"
)

// Every call carries a deadline, and there are two of them.
//
// askFor is the questions and the bookkeeping: reading what bluez has, writing
// a property, starting a scan, registering the agent. Each of those is
// bluetoothd talking to itself and none has any business taking two seconds.
// Having no deadline at all is the thing to avoid: zded answers keybinds on one
// socket, and against a bluetoothd wedged on a driver an unbounded call is a
// goroutine that never comes back, with every later bluetooth verb queued
// behind it.
//
// waitFor is the calls that wait on something outside this machine. Pairing
// waits on a person comparing six digits, connecting waits on a headset's
// radio, and forgetting a connected device tears the link down first. It is a
// backstop and not a policy: what decides a pairing is the agent's own refusal
// (answerWait, agent.go), and a Pair cancelled from under it would be zde
// saying no on somebody's behalf while they were still reading the number. So
// the three bounds nest - the question gives up at 45 seconds, the call at 75,
// and the CLI's own watch at 90.
const (
	askFor  = 2 * time.Second
	waitFor = answerWait + 30*time.Second
)

// bus is the little of D-Bus this package uses, behind an interface.
//
// Not for elegance: it is what makes the verbs testable at all. CI has no
// radio, and the things worth pinning need none - which object a verb reaches,
// what an address that is not one does, how long each call is given, and above
// all which calls pairing does not make. A test can watch every call that went
// out; a machine with bluetooth cannot be assumed.
//
// Every method takes its own deadline rather than sharing one hidden in the
// implementation, so that the bound is visible where the call is made and a
// test can see that there is one.
type bus interface {
	// Managed is ObjectManager: everything org.bluez has, in one reply.
	Managed(within time.Duration) (map[dbus.ObjectPath]map[string]map[string]dbus.Variant, error)
	// Call is a method on an object org.bluez owns.
	Call(within time.Duration, path dbus.ObjectPath, method string, args ...any) error
	// Set writes one property. Its own method rather than another Call,
	// because Trusted is the one property zde ever writes on a device and the
	// test that says pairing does not write it has to be able to see one.
	Set(within time.Duration, path dbus.ObjectPath, iface, prop string, value any) error
	Close() error
}

// systemBus is that, on the real bus.
type systemBus struct{ conn *dbus.Conn }

func (s systemBus) Managed(within time.Duration) (map[dbus.ObjectPath]map[string]map[string]dbus.Variant, error) {
	ctx, cancel := context.WithTimeout(context.Background(), within)
	defer cancel()
	var objs map[dbus.ObjectPath]map[string]map[string]dbus.Variant
	err := s.conn.Object(busName, rootPath).
		CallWithContext(ctx, objectManager+".GetManagedObjects", 0).Store(&objs)
	return objs, err
}

func (s systemBus) Call(within time.Duration, path dbus.ObjectPath, method string, args ...any) error {
	ctx, cancel := context.WithTimeout(context.Background(), within)
	defer cancel()
	return s.conn.Object(busName, path).CallWithContext(ctx, method, 0, args...).Err
}

func (s systemBus) Set(within time.Duration, path dbus.ObjectPath, iface, prop string, value any) error {
	ctx, cancel := context.WithTimeout(context.Background(), within)
	defer cancel()
	return s.conn.Object(busName, path).
		CallWithContext(ctx, properties+".Set", 0, iface, prop, dbus.MakeVariant(value)).Err
}

func (s systemBus) Close() error { return s.conn.Close() }

// Client is zde's whole conversation with BlueZ: one connection, held open for
// as long as the daemon runs, with the pairing agent exported on it.
//
// Held open because of the agent. A connection per call would register an agent
// and drop it again, and a device pairing to this machine while nothing was
// asking would reach nobody - which is fail-closed, and also a bluetooth stack
// that only works in the second somebody is looking at it.
type Client struct {
	bus   bus
	agent *Agent

	mu sync.Mutex
	// What a long call is doing and how the last one ended (see State).
	doing  string
	failed string
}

// Dial connects to the system bus and puts the agent on it.
//
// A bus it cannot reach is an error and not an absent adapter: "there is no
// system bus here" is a broken machine, and saying "no bluetooth" about it
// would send somebody looking at their radio.
func Dial() (*Client, error) {
	conn, err := dbus.ConnectSystemBus()
	if err != nil {
		return nil, fmt.Errorf("system bus: %w", err)
	}
	c := &Client{bus: systemBus{conn}, agent: NewAgent()}
	// Exported before registering, so that bluetoothd cannot call an object
	// that is not there yet.
	//
	// No introspection XML alongside it, unlike the notification server: that
	// one is called by every app on the machine and by whoever is debugging
	// them, and this one is called by bluetoothd, which knows the interface it
	// asked for.
	if err := conn.Export(&agent1{a: c.agent}, AgentPath, agentIface); err != nil {
		conn.Close()
		return nil, err
	}
	// Best effort: a machine with no bluetoothd has nothing to register with,
	// and that is the ordinary state of a desktop rather than a failure to
	// start. The verbs say "no adapter" from the same reading either way.
	c.register()
	return c, nil
}

// Close gives up the connection, and the agent registration with it.
func (c *Client) Close() error { return c.bus.Close() }

// register offers the agent to bluetoothd.
//
// Idempotent by way of AlreadyExists, which is what a second call gets, so this
// can be run again before a pairing without asking first whether it is needed.
// That matters: bluetoothd restarts - on a firmware reload, on a suspend that
// went badly - and takes every registration with it, and a zded that noticed
// only at the next login would have a pairing key that quietly did nothing.
//
// The gap it does not close is the other direction. A device pairing to this
// machine, in the window after a bluetoothd restart and before anything here
// asks for anything, reaches no agent at all, and BlueZ refuses it. That is the
// right way round to fail, and closing it properly means watching
// NameOwnerChanged for org.bluez, which is not here.
func (c *Client) register() error {
	err := c.bus.Call(askFor, managerPath, agentManagerIface+".RegisterAgent", AgentPath, Capability)
	if err != nil && !alreadyRegistered(err) {
		return err
	}
	// The default agent, so that a device pairing to this machine reaches a
	// person too, and not only one this machine asked to pair. zde is the
	// session's pairing agent the same way zded is its notification server. If
	// something else already holds it, this fails and the agent stays
	// registered without being the default - which still serves everything zde
	// itself starts.
	c.bus.Call(askFor, managerPath, agentManagerIface+".RequestDefaultAgent", AgentPath)
	return nil
}

func alreadyRegistered(err error) bool {
	var derr dbus.Error
	return errors.As(err, &derr) && derr.Name == "org.bluez.Error.AlreadyExists"
}

// State is the whole answer, and the only method that answers rather than
// acting: what the radio is doing, what is around, and any question waiting.
func (c *Client) State() (State, error) {
	snap, err := read(c.bus)
	if err != nil {
		return State{}, err
	}
	if req, ok := c.agent.Pending(); ok {
		// The name comes from the device list rather than from the agent: the
		// agent is handed a path by bluetoothd and asking the bus for a name
		// inside that callback would be a call made while bluetoothd waits.
		if d, known := snap.byAddr[req.Device]; known {
			req.Name = d.Name
		}
		snap.state.Pending = &req
	}
	c.mu.Lock()
	snap.state.Doing, snap.state.Failed = c.doing, c.failed
	c.mu.Unlock()
	return snap.state, nil
}

// Answer is the person saying yes or no to the question that is waiting.
func (c *Client) Answer(yes bool) error { return c.agent.Answer(yes) }

// Power turns the radio itself on or off.
//
// A verb of its own because it is the one that matters on a machine that is
// carried around: a radio that is off is not answering anybody, and everything
// else here needs it on. zde does not turn it on for you as a side effect of
// scanning - the radio starting to talk should be something somebody asked for.
func (c *Client) Power(on bool) error {
	snap, err := c.ready()
	if err != nil {
		return err
	}
	return c.bus.Set(askFor, snap.adapter, adapterIface, "Powered", on)
}

// Discover starts and stops looking for what is around.
//
// Stopping is as much of a verb as starting. Discovery keeps the radio talking
// and keeps the device list moving under the hand, so a surface that opens it
// closes it again, and `zde system bluetooth` says whether it is on.
func (c *Client) Discover(on bool) error {
	snap, err := c.ready()
	if err != nil {
		return err
	}
	if !snap.state.Adapter.Powered {
		return errors.New("the adapter is powered off: `zde system bluetooth power on` first")
	}
	method := ".StopDiscovery"
	if on {
		method = ".StartDiscovery"
	}
	return c.bus.Call(askFor, snap.adapter, adapterIface+method)
}

// Pair asks a device to pair, and answers before it is done.
//
// It has to. Pairing waits for a person to compare six digits on two screens,
// which is longer than any socket round trip should be, so this starts the
// attempt and the answer arrives through State: the question to answer while it
// runs, and Paired on the device afterwards.
//
// What it does not do is the point: it calls Pair and stops. It does not set
// Trusted, and no other path here does either. A device that paired once can
// reconnect and be asked about again; that is what untrusted means, and turning
// it off is a person's decision (Trust).
func (c *Client) Pair(addr string) error {
	_, path, dev, err := c.find(addr)
	if err != nil {
		return err
	}
	if dev.Paired {
		return errors.New(dev.Address + " is already paired")
	}
	// Registered again just before, rather than trusted from startup: see
	// register. This is the one path where a lost registration would show up as
	// a pairing that hangs and then fails for no visible reason.
	c.register()
	return c.start("pairing "+dev.Address, func() error {
		defer c.agent.Clear() // a passkey on screen for an attempt that is over
		return c.bus.Call(waitFor, path, deviceIface+".Pair")
	})
}

// Connect opens the link to a device that is already paired. Started rather
// than waited on, like pairing and for a smaller version of the same reason: a
// headset takes seconds to come up, and a keypress should not hold a socket
// open for them.
func (c *Client) Connect(addr string) error {
	_, path, dev, err := c.find(addr)
	if err != nil {
		return err
	}
	return c.start("connecting "+dev.Address, func() error {
		return c.bus.Call(waitFor, path, deviceIface+".Connect")
	})
}

// Disconnect drops the link. Not started in the background: taking a link down
// is local and immediate, and a person who asked for it should be told it
// happened.
func (c *Client) Disconnect(addr string) error {
	_, path, _, err := c.find(addr)
	if err != nil {
		return err
	}
	return c.bus.Call(waitFor, path, deviceIface+".Disconnect")
}

// Forget removes the device: the pairing key, the trust, the lot. It is
// RemoveDevice on the adapter rather than anything on the device, because after
// this there is no device object to call.
func (c *Client) Forget(addr string) error {
	snap, path, _, err := c.find(addr)
	if err != nil {
		return err
	}
	return c.bus.Call(waitFor, snap.adapter, adapterIface+".RemoveDevice", path)
}

// Trust says this device may reconnect and use its services without asking
// again - and untrust takes that back without unpairing.
//
// The verb exists so that the answer to "must I confirm this headset every
// morning" is a decision rather than a default. Nothing else in this package
// writes this property.
func (c *Client) Trust(addr string, yes bool) error {
	_, path, _, err := c.find(addr)
	if err != nil {
		return err
	}
	return c.bus.Set(askFor, path, deviceIface, "Trusted", yes)
}

// ready is one read of the bus with the adapter checked, for the verbs that act
// on the radio itself.
func (c *Client) ready() (snapshot, error) {
	snap, err := read(c.bus)
	if err != nil {
		return snapshot{}, err
	}
	if !snap.state.Adapter.Present {
		return snapshot{}, errors.New("no bluetooth adapter: " + snap.state.Adapter.Why)
	}
	return snap, nil
}

// find is the same, plus the device an address names.
//
// By lookup and not by building a path out of the address. bluez's device paths
// are derivable, and deriving one would mean a call sent at a path assembled
// from a string somebody typed. Looking it up in what the bus just listed costs
// the same read and cannot address anything that is not a device this adapter
// knows.
func (c *Client) find(addr string) (snapshot, dbus.ObjectPath, Device, error) {
	addr, err := normalAddr(addr)
	if err != nil {
		return snapshot{}, "", Device{}, err
	}
	snap, err := c.ready()
	if err != nil {
		return snapshot{}, "", Device{}, err
	}
	path, ok := snap.devices[addr]
	if !ok {
		return snapshot{}, "", Device{},
			fmt.Errorf("no device %s: `zde system bluetooth scan on` and look again", addr)
	}
	return snap, path, snap.byAddr[addr], nil
}

// start runs a long call and remembers how it went, so that the next State can
// say.
//
// One at a time. Two pairings at once is two questions at once, which the agent
// refuses anyway - and refusing here says so in words about the thing somebody
// is already in the middle of, rather than in words about an agent.
func (c *Client) start(what string, call func() error) error {
	c.mu.Lock()
	if c.doing != "" {
		busy := c.doing
		c.mu.Unlock()
		return errors.New("already " + busy)
	}
	c.doing, c.failed = what, ""
	c.mu.Unlock()
	go func() {
		err := call()
		c.mu.Lock()
		c.doing = ""
		if err != nil {
			c.failed = what + ": " + err.Error()
		}
		c.mu.Unlock()
	}()
	return nil
}
