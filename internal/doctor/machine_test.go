package doctor

import (
	"bufio"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crispuscrew/zde/internal/niri"
)

// The two lines docs/verify.md names as the tell for the most misleading
// failure in the project: niri comes up, opens its sockets, and never draws.
// They are here verbatim, as a session actually logs them, because the whole
// graphics answer is read off them and a paraphrase would pass a test the real
// thing fails.
const (
	fellLine1 = "failed to initialize renderer, falling back to primary gpu: software EGL renderers are skipped"
	fellLine2 = "no allocator available for device /dev/dri/card0"
)

// fakeLog is niri's log as this machine's journal would hand it over.
func fakeLog(lines ...string) logReader {
	return func() ([]string, error) { return lines, nil }
}

// noLog is a machine where the log could not be read at all: no journalctl, no
// journal, or a unit that has never existed.
func noLog(err error) logReader {
	return func() ([]string, error) { return nil, err }
}

// drawing is a compositor that is up and has a screen, which is every reading
// but the log.
func drawing() Graphics {
	return Graphics{
		Socket:  "/run/user/1000/niri.wayland-1.sock",
		Screens: []string{"eDP-1"},
		Cards:   []Card{{Node: "/dev/dri/card0", Driver: "amdgpu", PCI: "1002:1636"}},
	}
}

// Each of the two on its own, and not the pair together. A session logs them a
// line apart and a reader may only have one of them - and tested as a pair,
// dropping the sign that recognises the second would pass on the first.
func TestTheFallbackLinesAreReadAsNeverDrew(t *testing.T) {
	for _, line := range []string{fellLine1, fellLine2} {
		g := drawing()
		var err error
		g.Fell, g.Saw, err = pickLines(fakeLog("starting version 26.04", line))
		if err != nil {
			t.Fatal(err)
		}
		if g.Drew() != DrewNo {
			t.Fatalf("a compositor that logged %q reads as %v", line, g.Drew())
		}
		// Verbatim, because a person searching the web for what happened to
		// their machine searches for niri's words and not for ours.
		text := section(t, renderGraphics(g), secGraphics)
		if !strings.Contains(text, line) {
			t.Errorf("the line niri actually printed is not in the file:\n%s", text)
		}
		if !strings.Contains(text, "answer     NO") {
			t.Errorf("the answer is not the first thing the section says:\n%s", text)
		}
	}
}

// The reading that decides this is niri's own log, so a log that could not be
// read is never an all-clear - however healthy everything else looks. That is
// the exact shape of the failure: a compositor answering every question and
// drawing nothing.
func TestALogThatCouldNotBeReadIsNeverAnAllClear(t *testing.T) {
	g := drawing()
	g.LogErr = errors.New("journalctl: command not found")
	if g.Drew() != DrewUnknown {
		t.Fatalf("a session whose log could not be read reads as %v, and only the log can say", g.Drew())
	}
	text := section(t, renderGraphics(g), secGraphics)
	if !strings.Contains(text, "not known") {
		t.Errorf("the answer does not say it is not known:\n%s", text)
	}
	if !strings.Contains(text, "journalctl: command not found") {
		t.Errorf("the answer does not say why it could not be taken:\n%s", text)
	}
}

// And the other way: nothing here may claim it saw a photon. The strongest
// thing four readings can support is that nothing said otherwise.
func TestACompositorWithNoFallbackLineIsNotCalledCertain(t *testing.T) {
	g := drawing()
	if g.Drew() != DrewProbably {
		t.Fatalf("a compositor with outputs and a clean log reads as %v", g.Drew())
	}
	text := section(t, renderGraphics(g), secGraphics)
	if !strings.Contains(text, "probably yes") {
		t.Errorf("the answer is stated more firmly than the readings support:\n%s", text)
	}
	if !strings.Contains(text, "after the renderer") {
		t.Errorf("the answer does not say where to look next when the screen is still black:\n%s", text)
	}
}

// A compositor that is up, has a clean log and has nowhere to put a window is
// a black screen too, and it is the one state a check for "niri is answering"
// reads as health.
//
// The reading is the screens and not every connector niri lists, which is what
// makes this state reachable on an ordinary laptop rather than only on a machine
// with nothing plugged in: niri switches the panel off by itself when the lid
// closes, and the connector stays in its Outputs reply (internal/niri, Screens).
// Counted off that reply, a session drawing on nothing would read here as one
// drawing on one monitor, and this file would have talked somebody out of
// looking further.
func TestACompositorWithNoScreenIsNotAnAllClear(t *testing.T) {
	g := drawing()
	g.Screens = nil
	if g.Drew() != DrewUnknown {
		t.Fatalf("a compositor with nowhere to draw reads as %v", g.Drew())
	}
	if !strings.Contains(g.Unsure(), "a screen for none of its outputs") {
		t.Errorf("the reason does not name the missing reading: %q", g.Unsure())
	}
}

// The same state, from the end that reads the snapshot on a machine.
//
// The smoke test accepts the verdicts "probably yes" and "not known", and a
// compositor with a screen for none of its outputs reaches "not known" (see
// Unsure) - which is the black screen that section exists to catch. So the
// reason is checked there too, and the fragment it matches on lives in one
// place: quoted in smoke.nix, read from there here, applied to the line doctor
// really writes. Either half moving fails this.
func TestTheSmokeGuardRefusesTheBlackScreen(t *testing.T) {
	guard := smokeBlackScreenGuard(t)

	black := drawing()
	black.Screens = nil
	line := answerLine(t, black)
	// Why the verdict alone cannot refuse this state: it is one of the two the
	// VM is allowed to give.
	if !strings.HasPrefix(strings.TrimSpace(line), "answer     not known") {
		t.Fatalf("the black screen no longer answers %q, so the smoke test's verdict check "+
			"may be enough on its own again and this pinning should be reconsidered", line)
	}
	if !strings.Contains(line, guard) {
		t.Errorf("the smoke test would accept this line:\n  %s\nit refuses one holding %q, "+
			"and a compositor with nowhere to draw no longer says that", line, guard)
	}

	// And the answers the VM may honestly give, which the guard must let past
	// or every smoke run fails on a healthy machine.
	honest := drawing()
	if l := answerLine(t, honest); strings.Contains(l, guard) {
		t.Errorf("the smoke test would refuse a compositor that is drawing:\n  %s", l)
	}
	noJournal := drawing()
	noJournal.LogErr = errors.New("no journal here")
	if l := answerLine(t, noJournal); strings.Contains(l, guard) {
		t.Errorf("the smoke test would refuse a machine with no niri.service log:\n  %s", l)
	}
}

// smokeBlackScreenGuard is the fragment nix/tests/smoke.nix refuses the graphics
// answer for. Read out of that file rather than copied, so the two cannot drift.
func smokeBlackScreenGuard(t *testing.T) string {
	t.Helper()
	const mark = "# pinned: graphics-black-screen"
	path := filepath.Join("..", "..", "nix", "tests", "smoke.nix")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the VM test cannot be read, so nothing here can be pinned to it: %v", err)
	}
	for _, l := range strings.Split(string(data), "\n") {
		if !strings.Contains(l, mark) {
			continue
		}
		_, rest, ok := strings.Cut(l, "'")
		if !ok {
			t.Fatalf("%s carries %s but no quoted string: %q", path, mark, l)
		}
		guard, _, ok := strings.Cut(rest, "'")
		if !ok || guard == "" {
			t.Fatalf("%s carries %s and no fragment to match on: %q", path, mark, l)
		}
		return guard
	}
	t.Fatalf("%s no longer refuses the graphics answer for a compositor with nowhere to draw: "+
		"there is no line marked %s in it, so the VM test accepts the black screen again", path, mark)
	return ""
}

// answerLine is the verdict line of the graphics section, which is the one line
// the VM test reads.
func answerLine(t *testing.T, g Graphics) string {
	t.Helper()
	for _, l := range strings.Split(renderGraphics(g), "\n") {
		if strings.HasPrefix(l, "  answer ") {
			return l
		}
	}
	t.Fatalf("the graphics section has no answer line:\n%s", renderGraphics(g))
	return ""
}

// And the reading itself, over a socket, against a niri answering the way a
// laptop with its lid shut answers.
//
// Through Dial and the wire rather than against a stub, because what this pins
// is which question this file puts to niri and not how the answer is parsed.
// niri's Outputs reply names every connector that has a crtc, including one it
// has switched off itself; the screens are the ones it has somewhere to put a
// window (internal/niri, Screens). Every sentence in the graphics section is
// about whether there was anywhere to draw, so the two sets differ exactly
// where this file's answer is decided - and a test that read the same method
// name this file calls would agree with it about the name and prove nothing
// about the meaning.
func TestTheMonitorsAreTheOnesNiriHasAScreenFor(t *testing.T) {
	t.Setenv(niri.SocketEnv, fakeNiri(t, `{"Ok":{"Outputs":{`+
		`"eDP-1":{"name":"eDP-1","logical":null},`+
		`"DP-1":{"name":"DP-1","logical":{"x":0,"y":0,"width":2560,"height":1440,"scale":1.0,"transform":"Normal"}}`+
		`}}}`))
	got, _, err := askNiri()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != "DP-1" {
		t.Errorf("the report names %v, want only the monitor niri has a screen for: the panel it "+
			"switched off when the lid closed is still in the reply and is not somewhere a window can be", got)
	}
}

// A machine with more outputs than the list may hold is counted the way niri
// counted it: the bound is on the names and never on the number. Sixteen
// outputs is unusual and not absurd - a video wall, a capture rig, a couple of
// DP-MST chains - and a snapshot telling somebody with a black screen that niri
// has sixteen screens when it named twenty is a reading they cannot check,
// because they are reading it on another machine.
//
// And the row carrying the names says where it stopped, which is what this file
// does with every other cut reading (see lineMax, and cut).
func TestScreensPastTheBoundAreCountedAndTheCutIsSaid(t *testing.T) {
	const outputs = screensMax + 4
	var named []string
	for i := 1; i <= outputs; i++ {
		name := fmt.Sprintf("DP-%d", i)
		named = append(named, `"`+name+`":{"name":"`+name+`","logical":`+
			`{"x":0,"y":0,"width":2560,"height":1440,"scale":1.0,"transform":"Normal"}}`)
	}
	t.Setenv(niri.SocketEnv, fakeNiri(t, `{"Ok":{"Outputs":{`+strings.Join(named, ",")+`}}}`))

	got, more, err := askNiri()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != screensMax || more != outputs-screensMax {
		t.Fatalf("%d names and %d more, want %d names and %d more: the reading has to carry what "+
			"the bound took off it, or nothing downstream can say how many screens there were",
			len(got), more, screensMax, outputs-screensMax)
	}

	g := drawing()
	g.Screens, g.ScreensMore = got, more
	text := section(t, renderGraphics(g), secGraphics)
	if !strings.Contains(text, fmt.Sprintf("niri has %d screen(s)", outputs)) {
		t.Errorf("the answer counts the list it was cut to and not the screens niri named:\n%s", text)
	}
	if !strings.Contains(text, fmt.Sprintf("... (%d more)", outputs-screensMax)) {
		t.Errorf("the screens row stops without saying it was cut, so it reads as the whole "+
			"of what niri has:\n%s", text)
	}
}

// Two snapshots of one machine differ where the machine differs and nowhere
// else, because a diff is what they are for. Screens ranges a Go map, so
// without the sort in askNiri the screens row moves between two readings of an
// unchanged machine and reads as a monitor having moved.
//
// Past screensMax, because that is where it is easiest to lose: cut before the
// sort, two readings keep a different sixteen of one machine's outputs, so the
// row is a different set of monitors and not the same set reordered.
//
// Twelve readings, because Go randomises where a range of a map starts and one
// reading proves nothing.
func TestTwoReadingsOfOneMachineNameItsScreensTheSameWay(t *testing.T) {
	var outputs []string
	for i := 1; i <= screensMax+4; i++ {
		name := fmt.Sprintf("DP-%d", i)
		outputs = append(outputs, `"`+name+`":{"name":"`+name+`","logical":`+
			`{"x":0,"y":0,"width":2560,"height":1440,"scale":1.0,"transform":"Normal"}}`)
	}
	reply := `{"Ok":{"Outputs":{` + strings.Join(outputs, ",") + `}}}`

	var first []string
	for i := 0; i < 12; i++ {
		t.Setenv(niri.SocketEnv, fakeNiri(t, reply))
		got, _, err := askNiri()
		if err != nil {
			t.Fatal(err)
		}
		if first == nil {
			first = got
			continue
		}
		if strings.Join(got, " ") != strings.Join(first, " ") {
			t.Fatalf("one machine, two readings:\n  %v\n  %v\nthe screens row moves between two "+
				"snapshots of an unchanged machine, and a person diffing them reads that as a monitor moving",
				first, got)
		}
	}
}

// fakeNiri is a niri that answers each request with the next reply and then
// stops listening. One line in, one line out, which is the whole protocol.
func fakeNiri(t *testing.T, replies ...string) string {
	t.Helper()
	// Its own short directory: a unix socket path is capped near 108 bytes and
	// a Go temp directory under a long TMPDIR has spent most of that already.
	dir, err := os.MkdirTemp("", "niri")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "s")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close(); os.RemoveAll(dir) })
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		r := bufio.NewReader(conn)
		for _, rep := range replies {
			if _, err := r.ReadBytes('\n'); err != nil {
				return
			}
			if _, err := conn.Write([]byte(rep + "\n")); err != nil {
				return
			}
		}
	}()
	return path
}

// niri's other words about a renderer are kept as context and must never be
// read as a verdict, or a session that logged one line about its GPU would be
// reported as one that never drew.
func TestALineAboutADeviceIsNotAFallback(t *testing.T) {
	fell, saw, err := pickLines(fakeLog(
		"Found a DRM device at /dev/dri/card0",
		"EGL Initialized",
	))
	if err != nil {
		t.Fatal(err)
	}
	if len(fell) != 0 {
		t.Errorf("ordinary lines were read as a fallback: %q", fell)
	}
	if len(saw) != 2 {
		t.Errorf("the context around the answer was dropped: %q", saw)
	}
}

// The bounds hold over a log, because a log is the one input here whose length
// somebody else decides.
func TestOneLoudCompositorCannotFillTheFile(t *testing.T) {
	var lines []string
	for i := 0; i < 500; i++ {
		lines = append(lines, fellLine1, "gpu chatter "+strings.Repeat("x", 4000))
	}
	fell, saw, err := pickLines(fakeLog(lines...))
	if err != nil {
		t.Fatal(err)
	}
	if len(fell) != fellMax || len(saw) != sawMax {
		t.Fatalf("kept %d fallback and %d context lines, against %d and %d", len(fell), len(saw), fellMax, sawMax)
	}
	for _, l := range append(append([]string{}, fell...), saw...) {
		if len(l) > lineMax+64 {
			t.Errorf("a line of %d bytes reached the file", len(l))
		}
	}
}

// fakeRoot builds the parts of a machine this file reads: a /dev/dri with
// device nodes in it, and the sysfs that says what is behind them. Plain files
// rather than device nodes, which is the whole reason this takes a root at all -
// a test cannot mknod, and the readings are all off sysfs and an open.
func fakeRoot(t *testing.T, cards map[string]string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "dev", "dri"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, driver := range cards {
		if err := os.WriteFile(filepath.Join(root, "dev", "dri", name), nil, 0o600); err != nil {
			t.Fatal(err)
		}
		dev := filepath.Join(root, "sys", "class", "drm", name, "device")
		if err := os.MkdirAll(dev, 0o755); err != nil {
			t.Fatal(err)
		}
		if driver != "" {
			// The shape sysfs actually has: a symlink into the bus topology
			// whose last component is the module that bound.
			if err := os.Symlink("../../../bus/pci/drivers/"+driver, filepath.Join(dev, "driver")); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.WriteFile(filepath.Join(dev, "vendor"), []byte("0x1002\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dev, "device"), []byte("0x1636\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestACardIsTheDriverBoundToItAndNotTheVendorIdGuessedAt(t *testing.T) {
	root := fakeRoot(t, map[string]string{"card0": "amdgpu", "renderD128": "amdgpu"})
	cards, err := probeCards(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(cards) != 2 {
		t.Fatalf("found %d nodes in a directory with two: %+v", len(cards), cards)
	}
	if cards[0].Driver != "amdgpu" {
		t.Errorf("the driver bound to card0 reads as %q", cards[0].Driver)
	}
	if cards[0].PCI != "1002:1636" {
		t.Errorf("the chip behind card0 reads as %q", cards[0].PCI)
	}
	// The render node is the same chip counted twice, so the device line names
	// the card alone.
	g := Graphics{Cards: cards}
	if strings.Contains(g.Device(), "renderD") {
		t.Errorf("the device line counts one chip twice: %q", g.Device())
	}
}

// A card with nothing bound to it is the shape of the fault, so it is said
// rather than left blank - a device line ending in nothing reads as a bug in
// whatever wrote it.
func TestACardWithNoDriverSaysSo(t *testing.T) {
	root := fakeRoot(t, map[string]string{"card0": ""})
	cards, err := probeCards(root)
	if err != nil {
		t.Fatal(err)
	}
	g := Graphics{Cards: cards}
	if !strings.Contains(g.Device(), "no driver bound") {
		t.Errorf("a card with no driver reads as %q", g.Device())
	}
}

// Absent is a state. A machine with no /dev/dri at all - a container, a headless
// build host, a session where the seat went away - still gets a file.
func TestNoDriDirectoryIsAReadingAndNotAFailure(t *testing.T) {
	cards, err := probeCards(t.TempDir())
	if err == nil {
		t.Fatal("a root with no /dev/dri answered with no error")
	}
	if len(cards) != 0 {
		t.Errorf("cards came back from a machine with no /dev/dri: %+v", cards)
	}
	g := Graphics{CardsErr: err}
	text := section(t, renderGraphics(g), secGraphics)
	if !strings.Contains(text, "no DRM card node at all") {
		t.Errorf("the file does not say the machine has no card:\n%s", text)
	}
}

// /proc answers a single read with as much as it has ready and no more, which
// counted two cores on an eight-core machine until the read was done properly.
// A cpuinfo longer than one page is the case, so this one is.
func TestCoresAreCountedOverTheWholeOfProcCpuinfo(t *testing.T) {
	var b strings.Builder
	const want = 64
	for i := 0; i < want; i++ {
		b.WriteString("processor\t: ")
		b.WriteString(strings.Repeat("0", 3))
		b.WriteString("\nmodel name\t: AMD Ryzen 9 7950X 16-Core Processor\n")
		// The rest of a real block, which is what makes the file long enough to
		// need more than one read.
		b.WriteString("flags\t\t: " + strings.Repeat("sse ", 200) + "\n\n")
	}
	path := filepath.Join(t.TempDir(), "cpuinfo")
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	model, cores := cpuInfo(path)
	if cores != want {
		t.Errorf("counted %d of %d processors, which is a partial read", cores, want)
	}
	if !strings.HasPrefix(model, "AMD Ryzen") {
		t.Errorf("the processor reads as %q", model)
	}
}

// The names the kernel gives input devices, and nothing else off that file: it
// also carries vendor and product ids and the event node, and the question this
// answers is "which keyboard is this".
func TestInputDevicesAreNamesAndNothingElse(t *testing.T) {
	path := filepath.Join(t.TempDir(), "devices")
	err := os.WriteFile(path, []byte(`I: Bus=0011 Vendor=0001 Product=0001 Version=ab41
N: Name="AT Translated Set 2 keyboard"
P: Phys=isa0060/serio0/input0
H: Handlers=sysrq kbd event0 leds

I: Bus=0019 Vendor=0000 Product=0005 Version=0000
N: Name="Lid Switch"
H: Handlers=event1
`), 0o644)
	if err != nil {
		t.Fatal(err)
	}
	got := inputNames(path)
	want := []string{"AT Translated Set 2 keyboard", "Lid Switch"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("input devices read as %q", got)
	}
}

// The reading this file narrows hardest, and the shape of the narrowing is the
// point: an allowlist, because the last one was a denylist of nine names and a
// denylist keeps the promises somebody thought of.
//
// The line below is what a netbooted machine that unlocks a disk from a key
// file actually boots with. Under the nine names, root= and resume= came out
// and the MAC of the boot NIC, this machine's address and hostname, the NFS
// server, the path to the LUKS key, the machine-id and an API key set through
// systemd.setenv= all went into the file whole - against a header that promises,
// on three separate lines, no MAC, no IP, no machine-id and an environment that
// is an allowlist.
func TestTheCommandLineKeepsWhatDecidesWhatIsDrawnAndNoIdentityAtAll(t *testing.T) {
	got := kernelCmdline(`initrd=\efi\nixos\abc-initrd.efi BOOT_IMAGE=/nix/store/abc-linux/bzImage ` +
		`root=UUID=1d0a1e2b-3c4d-5e6f-7a8b-9c0d1e2f3a4b resume=/dev/disk/by-uuid/deadbeef-0000-1111-2222-333344445555 ` +
		`resume_offset=533760 rd.luks.uuid=luks-9c0d1e2f rd.luks.key=/keys/alice.key ` +
		`BOOTIF=01-3c-97-0e-3f-1a-2b ip=192.168.7.31:192.168.7.10:192.168.7.1:255.255.255.0:alice-thinkpad:eth0:none ` +
		`nfsroot=192.168.7.10:/srv/nixos systemd.machine_id=8f3c2b1a4d5e6f708192a3b4c5d6e7f8 ` +
		`systemd.setenv=ANTHROPIC_API_KEY=sk-ant-0123456789abcdef ` +
		`ro quiet loglevel=4 nomodeset nvidia_drm.modeset=0 i915.enable_psr=0 module_blacklist=nouveau ` +
		`video=HDMI-A-1:1920x1080 systemd.unit=rescue.target something.nobody.has.heard.of=7`)
	for _, gone := range []string{
		"1d0a1e2b", "deadbeef-0000", "533760", "luks-9c0d1e2f", // the disk
		"/keys/alice.key",                          // and the key that unlocks it
		"3c-97-0e", "192.168.7.31", "192.168.7.10", // the network
		"alice-thinkpad", "/srv/nixos", // and who this machine is on it
		"8f3c2b1a",                     // the machine-id
		"ANTHROPIC_API_KEY", "sk-ant-", // and the environment, name and value
		"/nix/store/abc-linux", "initrd.efi", // the paths that came for free
	} {
		if strings.Contains(got, gone) {
			t.Errorf("%q is somebody's identity and is still on the command line:\n%s", gone, got)
		}
	}
	for _, kept := range []string{
		"nomodeset",                  // the answer to half the black screens there are
		"nvidia_drm.modeset=0",       // and to most of the rest
		"i915.enable_psr=0",          // a driver option, which is the same kind of answer
		"module_blacklist=nouveau",   // a driver kept out of the machine on purpose
		"video=HDMI-A-1:1920x1080",   // and a mode forced onto a connector
		"systemd.unit=rescue.target", // why there may have been no session at all
		"ro", "quiet", "loglevel=4",
	} {
		if !strings.Contains(got, kept) {
			t.Errorf("%q is what somebody reads this line for and it is gone:\n%s", kept, got)
		}
	}
	// The parameter's own name stays where its value went, on every one of them,
	// because "this machine netboots" and "this machine resumes from something"
	// are facts about how it boots and only what follows is somebody's identity.
	for _, named := range []string{
		"root=", "resume=", "resume_offset=", "rd.luks.uuid=", "rd.luks.key=",
		"BOOTIF=", "ip=", "nfsroot=", "systemd.machine_id=", "systemd.setenv=",
		"initrd=", "BOOT_IMAGE=",
	} {
		if !strings.Contains(got, named+"<removed>") {
			t.Errorf("%q was taken off the line entirely rather than emptied:\n%s", named, got)
		}
	}
	// And this is what the inversion costs, said out loud: a parameter nobody
	// put on the list keeps its name and loses its value, even where the value
	// would have explained something. The reader is told there is a question to
	// ask rather than told nothing, which is the deal root= has always had.
	if !strings.Contains(got, "something.nobody.has.heard.of=<removed>") {
		t.Errorf("a parameter nobody listed is not shown as one whose value was taken:\n%s", got)
	}
}

// What is picked up out of niri's log decides what leaves this machine, because
// those lines go into the file whole and in niri's own words. So the signs are a
// renderer's vocabulary and nothing wider: "output" was one of them, and niri
// uses that word for a monitor, for a block of the config file and for anything
// it prints about either - which is a line the header could not honestly
// describe, carrying whatever else happened to be on it.
func TestALogLineThatIsNotAboutARendererIsNotPickedUp(t *testing.T) {
	fell, saw, err := pickLines(fakeLog(
		"Error creating renderer for primary GPU: no allocator available for device",
		`error in config: unknown field "outputs" at /home/alice/.config/niri/config.kdl:41`,
		"Trying to initialize EGL on /dev/dri/card0",
	))
	if err != nil {
		t.Fatal(err)
	}
	// The verdict and its context both survive: this is a narrowing and not a
	// file that stopped saying anything.
	if len(fell) != 1 || len(saw) != 1 {
		t.Fatalf("the renderer lines did not survive the narrowing: fell=%q saw=%q", fell, saw)
	}
	for _, line := range append(append([]string{}, fell...), saw...) {
		if strings.Contains(line, "config.kdl") || strings.Contains(line, "/home/alice") {
			t.Errorf("a line that is not about a renderer was picked up as one: %q", line)
		}
	}
}

// And the whole of it reaches the file, however long it is: a queue row's bound
// would have cut it off two store paths before nomodeset, which is the word it
// is being read for.
func TestALongCommandLineReachesTheFileWhole(t *testing.T) {
	long := strings.Repeat("init=/nix/store/0123456789abcdef0123456789abcdef-nixos-system-x-26.05/init ", 8) + "nomodeset"
	var b strings.Builder
	writeHardware(&b, Hardware{Firmware: "UEFI", Cmdline: long})
	if !strings.Contains(b.String(), "nomodeset") {
		t.Errorf("the last word of a %d character command line did not reach the file:\n%s", len(long), b.String())
	}
	// Filled onto rows this package wrote, so nothing on any of them came from
	// a value: every one is either the label's row or an indent of its own.
	for _, line := range strings.Split(strings.TrimRight(b.String(), "\n"), "\n") {
		if line != "" && line != secHardware && !strings.HasPrefix(line, "  ") {
			t.Errorf("the command line wrote a line of the file: %q", line)
		}
	}
}

// A rebuild swaps /run/current-system and leaves /run/booted-system where it
// was, so the running kernel and mesa are one generation's and everything
// started since is another's. It is the ordinary cause of a black screen after
// a switch and it is invisible in every log there is.
func TestARebuiltAndUnrebootedMachineIsSaidPlainly(t *testing.T) {
	v := Versions{
		Zde:     "0.1.0",
		Booted:  "/nix/store/aaa-nixos-system-zdebox-26.05",
		Current: "/nix/store/bbb-nixos-system-zdebox-26.05",
	}
	if !v.Skewed() {
		t.Fatal("two different generations do not read as a machine that was not rebooted")
	}
	var b strings.Builder
	writeVersions(&b, v)
	if !strings.Contains(b.String(), "rebuilt and not rebooted") {
		t.Errorf("the skew is not said in the file:\n%s", b.String())
	}
	// And a machine with neither is not a machine with skew, which is every
	// machine that is not NixOS.
	if (Versions{}).Skewed() {
		t.Error("a machine with no generations reads as one that was not rebooted")
	}
}

// The environment is an allowlist and never the environment. This is the file
// people paste into bug reports, and an environment carries API keys.
func TestOnlyTheNamedEnvironmentReachesTheFile(t *testing.T) {
	t.Setenv("ZDE_TEST_API_KEY", "sk-do-not-write-this-down")
	t.Setenv("LIBGL_ALWAYS_SOFTWARE", "1")
	g := probeGraphics(t.TempDir(), noLog(errors.New("no journal here")))
	text := section(t, renderGraphics(g), secGraphics)
	if strings.Contains(text, "sk-do-not-write-this-down") {
		t.Fatalf("a variable nobody named reached the file:\n%s", text)
	}
	if !strings.Contains(text, "LIBGL_ALWAYS_SOFTWARE=1") {
		t.Errorf("the variable that decides whether this renders in software is missing:\n%s", text)
	}
}

// renderGraphics is the graphics section on its own, which is what most of the
// assertions above read.
func renderGraphics(g Graphics) string {
	var b strings.Builder
	writeGraphics(&b, g)
	return b.String()
}

// section is one section of a report, so that an assertion about the graphics
// answer cannot pass on a word that landed in the hardware inventory.
func section(t *testing.T, text, heading string) string {
	t.Helper()
	i := strings.Index(text, heading)
	if i < 0 {
		t.Fatalf("there is no %s section in:\n%s", heading, text)
	}
	rest := text[i:]
	// The next heading, whichever it is: every one of them starts a line with a
	// bracket, and nothing a machine puts in this file is written that way.
	if j := strings.Index(rest[1:], "\n["); j >= 0 {
		rest = rest[:j+1]
	}
	return rest
}
