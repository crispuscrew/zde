// Package zded owns the journal, compositor and private user socket.
package zded

import (
	"context"
	"net"
	"sync"
	"time"

	"github.com/crispuscrew/zde/internal/attn"
	"github.com/crispuscrew/zde/internal/bt"
	"github.com/crispuscrew/zde/internal/clip"
	"github.com/crispuscrew/zde/internal/journal"
	"github.com/crispuscrew/zde/internal/link"
	"github.com/crispuscrew/zde/internal/power"
	"github.com/crispuscrew/zde/internal/zinc"
)

// Server owns the journal, compositor and socket. mu protects session state;
// linkMu and powerMu protect their managers. bluetoothDial serializes dials,
// while bluetoothMu protects the radio without holding Close behind a dial.
// Subprocess claims and runCtx cancellation share mu to prevent Add during Wait.
type Server struct {
	version string
	jrn     *journal.Journal
	niri    Compositor
	desks   Desks
	launch  func(ctx context.Context, address string) error
	spawn   func(argv []string) error

	notifier Notifier
	history  attn.History
	written  written

	popups         chan Event
	startPump      sync.Once
	popupStop      chan struct{}
	stopPump       sync.Once
	saidQueueFull  sync.Once
	saidSenderFull sync.Once

	clips     clip.History
	clipboard Clipboard
	clipWhy   string

	sound     Sound
	panicking *panicHold

	linkMu      sync.Mutex
	link        link.Manager
	openLink    func() (link.Manager, error)
	noManager   error
	noManagerAt time.Time

	bluetoothDial sync.Mutex
	bluetoothMu   sync.Mutex
	bluetooth     Bluetooth
	bluetoothGone uint64
	openBluetooth func() (Bluetooth, error)

	powerMu    sync.Mutex
	logind     power.Manager
	openPower  func() (power.Manager, error)
	noLogind   error
	noLogindAt time.Time
	dialing    *dial

	runCtx  context.Context
	runStop context.CancelFunc
	runs    sync.WaitGroup

	mu        sync.Mutex
	ln        net.Listener
	problems  []string
	unplaced  uint64
	asks      int
	launching map[string]struct{}
	conns     map[*sink]struct{}
	dropped   uint64
	subs      map[*sink]struct{}
	waiting   map[string]chan struct{}
	tokens    uint64
}

// New builds the daemon. Device integrations are lazy or explicitly injected;
// constructing a test server must not read the clipboard or mute the host.
func New(version string, jrn *journal.Journal, compositor Compositor, desks Desks) *Server {
	if desks == nil {
		desks = noDesks{}
	}
	runCtx, runStop := context.WithCancel(context.Background())
	return &Server{
		version:       version,
		jrn:           jrn,
		niri:          compositor,
		desks:         desks,
		launch:        zinc.Run,
		spawn:         spawnDetached,
		openLink:      link.Open,
		runCtx:        runCtx,
		runStop:       runStop,
		openBluetooth: func() (Bluetooth, error) { return bt.Dial() },
		popupStop:     make(chan struct{}),
	}
}
