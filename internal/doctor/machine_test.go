package doctor

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
		Outputs: []string{"eDP-1"},
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
func TestACompositorWithNoOutputsIsNotAnAllClear(t *testing.T) {
	g := drawing()
	g.Outputs = nil
	if g.Drew() != DrewUnknown {
		t.Fatalf("a compositor with no outputs reads as %v", g.Drew())
	}
	if !strings.Contains(g.Unsure(), "no outputs") {
		t.Errorf("the reason does not name the missing reading: %q", g.Unsure())
	}
}

// niri's other words about devices are kept as context and must never be read
// as a verdict, or a session that logged one line about an output would be
// reported as one that never drew.
func TestALineAboutADeviceIsNotAFallback(t *testing.T) {
	fell, saw, err := pickLines(fakeLog(
		"Output eDP-1 connected",
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

// The one thing this file narrows rather than admits to. root= and resume= are
// the identity of a filesystem, they travel with the file into whatever bug
// report it is pasted into, and neither of them can cause or explain a black
// screen. The parameters that can are on the same line and every one of them
// stays - including the ones this list has never heard of.
func TestTheCommandLineKeepsEveryParameterAndNoDiskIdentity(t *testing.T) {
	got := kernelCmdline(`initrd=\efi\nixos\abc-initrd.efi BOOT_IMAGE=/nix/store/abc-linux/bzImage ` +
		`root=UUID=1d0a1e2b-3c4d-5e6f-7a8b-9c0d1e2f3a4b resume=/dev/disk/by-uuid/deadbeef-0000-1111-2222-333344445555 ` +
		`resume_offset=533760 rd.luks.uuid=luks-9c0d1e2f ro quiet loglevel=4 nomodeset nvidia_drm.modeset=0 ` +
		`i915.enable_psr=0 something.nobody.has.heard.of=7`)
	for _, gone := range []string{
		"1d0a1e2b", "deadbeef-0000", "533760", "luks-9c0d1e2f",
	} {
		if strings.Contains(got, gone) {
			t.Errorf("%q names a disk and is still on the command line:\n%s", gone, got)
		}
	}
	for _, kept := range []string{
		"nomodeset",                       // the answer to half the black screens there are
		"nvidia_drm.modeset=0",            // and to most of the rest
		"i915.enable_psr=0",               // a driver option, which is the same kind of answer
		"something.nobody.has.heard.of=7", // and this is not a list of what may stay
		"BOOT_IMAGE=/nix/store/abc-linux/bzImage",
		"ro", "quiet", "loglevel=4",
	} {
		if !strings.Contains(got, kept) {
			t.Errorf("%q is what somebody reads this line for and it is gone:\n%s", kept, got)
		}
	}
	// The parameter's own name stays where its value went, because "this
	// machine resumes from something" is a fact about how it boots and only
	// which volume it resumes from is a serial number for a disk.
	for _, named := range []string{"root=", "resume=", "resume_offset=", "rd.luks.uuid="} {
		if !strings.Contains(got, named) {
			t.Errorf("%q was taken off the line entirely rather than emptied:\n%s", named, got)
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
