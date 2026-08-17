package doctor

// What the journal never records: state.
//
// services.journald.storage is persistent on every config in this flake, so a
// real install already keeps greetd's, niri's, zded's and the shell's logs
// across a reboot - the events survive. What does not is what the machine
// looked like while they happened: which card was in it, which driver was bound
// to it, which system generation was booted against which one was current. A
// log line saying a renderer could not be created is only half an answer
// without the device it could not be created for.
//
// So everything in this file is a reading rather than an event, and every one
// of them is taken the way internal/doctor already takes its own: bounded,
// with absent as a state and never as a failure. It runs on a machine where
// something has already gone wrong, which is exactly the machine where a probe
// that hangs costs somebody the one file that was going to explain it.

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"

	"github.com/crispuscrew/zde/internal/niri"
)

// Machine is the state a snapshot is made of: the graphics answer, what
// versions of things this is, and what hardware is under them.
type Machine struct {
	Graphics Graphics
	Versions Versions
	Hardware Hardware
}

// Graphics is the question this whole file exists for: did niri reach a real
// renderer, and on which device.
//
// It is four readings and not one, because no single one of them can answer it.
// niri answering on its IPC socket proves the compositor is alive and proves
// nothing about whether it draws - that is precisely the failure this is for
// (docs/verify.md, the virtual GPU). The DRM devices say what there was to draw
// on. niri's own log says what it did about them. And the environment says
// whether something told it to do otherwise.
type Graphics struct {
	// Socket is where niri says its IPC socket is, empty when nothing said.
	Socket string
	// Screens is what niri has somewhere to draw on, and NiriErr is why it did
	// not answer. Not every connector it reports: an output niri has switched
	// off is still in that reply and is not a place a window can be, and the
	// difference is the whole of what this reading is for (internal/niri,
	// Screens). Names only: what is on a screen is nobody's business but the
	// person's, so nothing here asks niri about windows (see reportHeader).
	Screens []string
	NiriErr error

	// Cards is one entry per DRM device node this machine has.
	Cards []Card
	// CardsErr is /dev/dri not being readable at all, which is itself an
	// answer: a session that cannot see the directory cannot open a card in it.
	CardsErr error

	// Fell is the lines in niri's own log that say it never reached a renderer,
	// verbatim. Verbatim because a paraphrase of a compositor's log line is a
	// sentence somebody then cannot search the web for.
	Fell []string
	// Saw is the rest of what niri said about renderers, devices and outputs,
	// bounded. It is context for a verdict rather than part of it.
	Saw []string
	// LogErr is why the log could not be read, which on this machine is a
	// reading too: no journalctl, no journal, or a unit that never existed.
	LogErr error

	// Env is the handful of variables that change what a compositor renders
	// with, each with its value or the fact that it is unset. An allowlist and
	// never the environment, because an environment holds API keys.
	Env []EnvVar
}

// Card is one DRM device node and what is behind it.
type Card struct {
	// Node is the path, /dev/dri/card0 or /dev/dri/renderD128.
	Node string
	// Driver is the kernel driver bound to it - amdgpu, i915, nouveau, nvidia,
	// virtio-pci - read off sysfs rather than guessed from the vendor id,
	// because which driver bound is the thing that decides whether there is a
	// renderer to reach. Empty when sysfs does not say.
	Driver string
	// PCI is the vendor and device id, which is what identifies the chip in a
	// bug report when the driver name does not (four generations of Intel
	// graphics are all i915).
	PCI string
	// OpenErr is this session trying to open the node, and it is the check
	// nothing else makes: a card present, a driver bound, and a user not in the
	// group that may open it is a black screen with a perfectly healthy log
	// above it.
	OpenErr error
}

// EnvVar is one variable and whether it is set. Set-to-empty and unset are kept
// apart, because LIBGL_ALWAYS_SOFTWARE= and no LIBGL_ALWAYS_SOFTWARE are not
// the same instruction to a stack that reads it.
type EnvVar struct {
	Name  string
	Value string
	Set   bool
}

// Versions is what this is a version of, and what it was built from.
type Versions struct {
	// Zde is this binary's own version, Zded the running daemon's. They can
	// differ, and when they do it is worth knowing: a rebuild replaces the
	// binary on disk and does not replace a daemon already running.
	Zde  string
	Zded string

	Niri    string
	NiriErr error

	// Kernel is uname -r, which is the other half of a driver question.
	Kernel string
	OS     string

	// Booted and Current are the two system generations, and the reason this
	// section is here at all. A rebuild swaps /run/current-system and leaves
	// /run/booted-system where it was, so a machine whose mesa or kernel moved
	// under a running session has these two disagreeing - which is the cause
	// this project's own flake names for a black screen off a TTY (flake.nix,
	// the niri input).
	Booted  string
	Current string
	// Revision is the commit the running system was built from, where the
	// config recorded one (system.configurationRevision).
	Revision string
}

// Hardware is the inventory: what this is, so that a report read cold is about
// a machine rather than about nothing.
type Hardware struct {
	// Model is what the firmware calls this machine, and Board its motherboard.
	// It is the first thing anybody matching a graphics fault against somebody
	// else's needs.
	Model string
	Board string

	CPU   string
	Cores int
	// Memory is MemTotal as the kernel reports it.
	Memory string

	// Firmware is UEFI or BIOS, decided by whether the kernel exposed EFI
	// variables. It is here because it decides which half of the boot path a
	// machine took to get to a black screen.
	Firmware string
	// Cmdline is what the kernel was started with. nomodeset and
	// nvidia_drm.modeset=0 are both in it and both are a black screen, and
	// neither leaves a trace anywhere else on this list.
	//
	// With the disk out of it - see kernelCmdline. Every parameter is here and
	// the values that name a filesystem are not.
	Cmdline string

	// Inputs is what the kernel calls each input device, which is the answer to
	// "which keyboard is this" - the question the input layer turns on
	// (docs/verify.md, section 2).
	Inputs []string
}

// The bounds on what a machine may put in this file. Each one is here rather
// than inline because each is a list something else decides the length of: a
// machine with forty input devices, a compositor with a thousand log lines, a
// rack with sixteen GPUs. The arithmetic they add up to is in report.go.
const (
	// logLinesMax is how many of niri's own lines are read before anything is
	// picked out of them. A session's worth of niri at INFO is a few hundred;
	// two thousand is a session that has been up for days.
	logLinesMax = 2000
	// fellMax and sawMax are how many survive the picking. Every line that says
	// niri did not reach a renderer is worth having, and they arrive in a run of
	// two or three; the context around them is worth a screenful and no more.
	fellMax = 12
	sawMax  = 20
	// lineMax is how much of any one reading is kept. A tracing line naming a
	// device path and an error is under 200 characters, and this stops one line
	// of somebody's log being the whole file.
	//
	// Under the bound the row filter carries with it (internal/attn,
	// summaryMax at 300), and that is the point of the number rather than a
	// coincidence: a bound that sat above it would never fire, the filter would
	// do the cutting instead, and the filter cuts in silence. This file says
	// where it was cut everywhere else it cuts, and a reading that quietly lost
	// its last forty characters is the one kind of lie a snapshot cannot
	// afford - the reader is on another machine and cannot go and look.
	lineMax = 256
	// cmdlineMax is the kernel command line's own bound, and it is larger for a
	// reason the rest of this list does not have: that line is three store
	// paths and an initrd before it reaches nomodeset, and nomodeset is what
	// somebody is reading it for. Two kilobytes is more than any bootloader
	// writes and a thirtieth of the file's own ceiling.
	cmdlineMax = 2 << 10
	// cardsMax, inputsMax and screensMax bound the three lists a machine
	// supplies. Eight graphics devices is a workstation with two cards and their
	// render nodes twice over; sixty-four input devices is a machine with a
	// keyboard, a mouse, a touchpad, a lid switch, a power button and fifty-nine
	// things nobody has.
	cardsMax   = 16
	inputsMax  = 64
	screensMax = 16
)

// niriUnit is the unit niri's own session runs it under (its shipped
// niri.service, which nix/system.nix installs through programs.niri.enable).
// The log this reads is that unit's, in the systemd journal, which is where
// every word niri says about a renderer goes.
const niriUnit = "niri.service"

// fellSigns are the lines niri prints when it did not reach a renderer.
//
// They are literal fragments of niri 26.04's own messages, checked against the
// binary nixpkgs builds, and the first two are the pair docs/verify.md names as
// the tell for the most misleading failure in the whole project: niri comes up,
// opens its Wayland and IPC sockets, and never draws. A session in that state
// answers every question zde can put to it and shows a black screen.
//
// Fragments and not whole lines, because the whole line carries a device path
// and an error from below it - which is the part worth reading and the part
// that cannot be matched on.
//
// Matched case-insensitively, since niri's own capitalisation varies between
// the messages and the errors it wraps.
var fellSigns = []string{
	// The one the VM script was written around: niri refuses software EGL on
	// purpose, so a display-only card gets this and nothing else.
	"software egl renderers are skipped",
	"no allocator available for device",
	"failed to initialize renderer",
	"error creating renderer for primary gpu",
	"error opening the primary gpu drm node",
	"couldn't find a gpu",
	"primary node is missing",
}

// sawSigns is the rest of what niri says about the same subject: kept as
// context, never as a verdict. A line matching one of these and none of the
// fellSigns is niri talking about a device, which is what somebody reading this
// cold wants under the answer.
var sawSigns = []string{
	"renderer",
	"gpu",
	"drm",
	"egl",
	"gbm",
	"/dev/dri",
	"output",
}

// graphicsEnv is every variable that can change what a compositor renders with,
// and it is a list rather than a pattern on purpose: this file is written to be
// pasted into a bug report, and a pattern over the environment is how an API key
// ends up in one (nix/home.nix, zde.ask.tiers - nothing in zde holds a key, and
// the tier that does is a program with its own environment).
var graphicsEnv = []string{
	"WAYLAND_DISPLAY",
	niri.SocketEnv,
	"XDG_SESSION_TYPE",
	"XDG_SESSION_ID",
	"XDG_CURRENT_DESKTOP",
	"XDG_SEAT",
	"LIBGL_ALWAYS_SOFTWARE",
	"GALLIUM_DRIVER",
	"MESA_LOADER_DRIVER_OVERRIDE",
	"WLR_RENDERER",
	"WLR_BACKENDS",
	"LIBVA_DRIVER_NAME",
	"__GLX_VENDOR_LIBRARY_NAME",
}

// logReader is where niri's own words come from. A parameter and not a call,
// so that the verdict below can be exercised against the exact lines a machine
// with no GPU produces, on a machine that has one and no niri at all.
type logReader func() ([]string, error)

// Self is what the running command already knows and no probe can find out:
// which build of zde is asking, and which build of zde answered on the socket.
//
// Passed in rather than looked up, because both are held by callers that have
// them already - the version is set at link time in package main (nix/zde.nix),
// and the daemon's came back with the status the report has gathered anyway.
type Self struct {
	Zde string
	// Zded is empty when the daemon is not answering, which is a different
	// reading from a daemon answering with an empty version.
	Zded string
}

// GatherMachine is every reading in this file, taken off the running machine.
func GatherMachine(self Self) Machine {
	return gatherMachine("/", niriLog, self)
}

// gatherMachine is GatherMachine with the machine handed to it: a filesystem
// root and a way to read a log. Both are parameters because both are what makes
// this testable at all - a test builds a sysfs holding a card with no driver, or
// a log holding the fallback lines, and asks what this says about it.
func gatherMachine(root string, log logReader, self Self) Machine {
	return Machine{
		Graphics: probeGraphics(root, log),
		Versions: probeVersions(root, self),
		Hardware: probeHardware(root),
	}
}

// probeGraphics takes the four readings the answer is made of.
func probeGraphics(root string, log logReader) Graphics {
	g := Graphics{Socket: os.Getenv(niri.SocketEnv)}
	g.Screens, g.NiriErr = askNiri()
	g.Cards, g.CardsErr = probeCards(root)
	g.Fell, g.Saw, g.LogErr = pickLines(log)
	for _, name := range graphicsEnv {
		v, set := os.LookupEnv(name)
		g.Env = append(g.Env, EnvVar{Name: name, Value: v, Set: set})
	}
	return g
}

// askNiri asks the compositor what it has to draw on.
//
// Monitors and not windows, and that is a privacy decision rather than an
// economy: a window list is titles, and a title is what somebody is reading.
// A monitor's name is hardware.
//
// Screens and not every connector niri reports, because the question this
// reading answers is whether there is anywhere to draw. niri's Outputs reply is
// built by walking every connector that has a crtc, so a monitor niri has
// switched off is still in it - and switching one off is niri's own default
// when a laptop lid closes (internal/niri, Screens). Counting those would give
// this file the one answer it must never give: "probably yes" about a session
// whose only panel niri had turned off, which is a black screen with a clean
// log and healthy sockets, which is the failure this whole section exists to
// catch. What is lost with them is the name of a monitor that is plugged in and
// dark, and a monitor niri is not drawing on cannot be the one somebody is
// failing to see anything on.
//
// Its own dial rather than the daemon's answer, for the reason probeDesks reads
// the disk instead of asking zded: the session this runs on is often one where
// zded is what is wrong, and a reading that needed it would go blank exactly
// when it is wanted. internal/niri bounds its own connect and its own request,
// so nothing here has to.
func askNiri() ([]string, error) {
	c, err := niri.Dial()
	if err != nil {
		return nil, err
	}
	defer c.Close()
	names, err := c.Screens()
	if err != nil {
		return nil, err
	}
	if len(names) > screensMax {
		names = names[:screensMax]
	}
	sort.Strings(names)
	return names, nil
}

// probeCards is every DRM device node, with the driver bound to it, the chip
// behind it, and whether this session may open it.
//
// Off sysfs and the device nodes rather than through a tool, because there is
// no tool this is allowed to need: lspci is not installed on a zde machine, and
// a probe that answers "not known" on the ordinary machine answers nothing.
func probeCards(root string) ([]Card, error) {
	dir := filepath.Join(root, "dev", "dri")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []Card
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, "card") && !strings.HasPrefix(name, "renderD") {
			continue // by-path/ and anything else that is not a node
		}
		if len(out) >= cardsMax {
			break
		}
		c := Card{Node: filepath.Join("/dev/dri", name)}
		// The sysfs device directory for a node is /sys/class/drm/<name>/device,
		// and `driver` under it is a symlink whose last component is the module
		// that bound. Read as a link rather than followed, because the answer is
		// the name and the target is a path into a bus topology nobody reads.
		devDir := filepath.Join(root, "sys", "class", "drm", name, "device")
		if link, err := os.Readlink(filepath.Join(devDir, "driver")); err == nil {
			c.Driver = filepath.Base(link)
		}
		vendor := firstLine(readFile(filepath.Join(devDir, "vendor")))
		device := firstLine(readFile(filepath.Join(devDir, "device")))
		if vendor != "" || device != "" {
			c.PCI = strings.TrimPrefix(vendor, "0x") + ":" + strings.TrimPrefix(device, "0x")
		}
		// Opened and closed, which is the whole of the check: whether this uid
		// may. O_NONBLOCK so that a driver that would block on open does not
		// take the file this is being written into with it - the same reason
		// internal/plainfile opens that way.
		if f, err := os.OpenFile(filepath.Join(dir, name), os.O_RDONLY|unix.O_NONBLOCK, 0); err == nil {
			f.Close()
		} else {
			c.OpenErr = err
		}
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Node < out[j].Node })
	return out, nil
}

// niriLog is what niri said this boot, off the systemd journal.
//
// --user, because niri runs as a user unit and that is where its words are.
// This is readable by the person who owns the session without any privilege at
// all, which is what makes it work from a second VT when the first one is
// black - and it is the same journal that survives the reboot, so the file this
// ends up in and the journal a person reads later agree with each other.
//
// -o cat: the message and nothing else. The timestamps are in the journal for
// anybody who goes there, and what this file needs is the sentence.
func niriLog() ([]string, error) {
	out, err := run(probeTimeout, "journalctl",
		"--user", "--boot", "--unit", niriUnit,
		"--output", "cat", "--no-pager", "--lines", strconv.Itoa(logLinesMax))
	if err != nil {
		return nil, err
	}
	return strings.Split(strings.TrimRight(out, "\n"), "\n"), nil
}

// pickLines separates what niri said into the lines that decide the answer and
// the lines that colour it in.
//
// Newest first in both, because a session that failed, was restarted and failed
// again has the useful copy at the bottom of the log and the bound counts from
// the top.
func pickLines(log logReader) (fell, saw []string, err error) {
	lines, err := log()
	if err != nil {
		return nil, nil, err
	}
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		low := strings.ToLower(line)
		if matchesAny(low, fellSigns) {
			if len(fell) < fellMax {
				fell = append(fell, cut(line, lineMax))
			}
			continue
		}
		if matchesAny(low, sawSigns) && len(saw) < sawMax {
			saw = append(saw, cut(line, lineMax))
		}
	}
	return fell, saw, nil
}

func matchesAny(low string, signs []string) bool {
	for _, s := range signs {
		if strings.Contains(low, s) {
			return true
		}
	}
	return false
}

// Drew is the answer, as one of three states, and it is what the first line of
// the graphics section says.
//
// A pure method on the readings, so that every one of the three can be written
// down in a test without a GPU, a compositor or a journal - which is the only
// way the "never drew" answer is ever exercised, since the machine this is
// written on cannot be made to produce it.
type Drew int

const (
	// DrewNo is niri's own log saying it did not reach a renderer. It is the
	// only one of the three that is a verdict rather than an inference, because
	// it is the compositor's own word.
	DrewNo Drew = iota
	// DrewProbably is no such line, and a compositor answering with a screen.
	// Deliberately not "yes": nothing here can see a photon, and this file must
	// not be the thing that talks somebody out of looking further.
	DrewProbably
	// DrewUnknown is not enough to say either way - no log to read, or a log
	// with nothing in it and no compositor to ask.
	DrewUnknown
)

// Drew reads the four readings in the order of how much each is worth.
func (g Graphics) Drew() Drew {
	switch {
	case len(g.Fell) > 0:
		return DrewNo
	case g.LogErr != nil:
		// The one reading that could have said no could not be taken, so a
		// compositor answering proves only that it is alive - which is exactly
		// the state that looks like health and is not.
		return DrewUnknown
	case g.NiriErr == nil && len(g.Screens) > 0:
		return DrewProbably
	}
	return DrewUnknown
}

// Unsure is which reading was missing, for the answer that has to say so.
//
// A sentence and not a code, because the whole point of the verdict line is
// that it is read by somebody with no other context: "not known" on its own
// sends them looking through the rest of the section for the gap, which is the
// work this file exists to have already done.
func (g Graphics) Unsure() string {
	switch {
	case g.LogErr != nil:
		return "niri's own log is the only thing that can say it fell back, and it could not be read"
	case g.NiriErr != nil:
		return "nothing in niri's log this boot says it fell back, and niri is not answering, so it cannot be asked either"
	case len(g.Screens) == 0:
		return "niri is answering and has a screen for none of its outputs, so there is nothing it could have drawn on"
	}
	return "no reading here says either way"
}

// Device is which card the answer is about, in a few words, or empty when this
// machine cannot say.
//
// Only the cards, and never the render nodes: a renderD node beside a card is
// the same chip counted twice, and a report that named both would read as a
// machine with two GPUs.
func (g Graphics) Device() string {
	var named []string
	for _, c := range g.Cards {
		if !strings.HasPrefix(filepath.Base(c.Node), "card") {
			continue
		}
		if c.Driver == "" {
			// A card with nothing bound to it is the shape of the fault rather
			// than a device to name, and saying so is worth more than leaving
			// it out.
			named = append(named, c.Node+" (no driver bound)")
			continue
		}
		named = append(named, c.Node+" ("+c.Driver+")")
	}
	return strings.Join(named, ", ")
}

// probeVersions reads what this is a version of.
func probeVersions(root string, self Self) Versions {
	v := Versions{Zde: self.Zde, Zded: self.Zded}
	// niri's own, and by running it: niri has no IPC request for its version
	// that internal/niri models, and the point of this line is the build a
	// person could go and reproduce with. Bounded like every other subprocess
	// here.
	if out, err := run(probeTimeout, "niri", "--version"); err != nil {
		v.NiriErr = err
	} else {
		v.Niri = firstLine(out)
	}
	var uts unix.Utsname
	if err := unix.Uname(&uts); err == nil {
		v.Kernel = nulString(uts.Release[:])
	}
	// The NixOS generation's own version file first, because it is the exact
	// string `nixos-version` prints and the one an issue asks for; os-release is
	// the answer on a machine that is not NixOS at all (docs/delivery.md - the
	// portable path is layer 1 on somebody else's distro).
	v.OS = firstLine(readFile(filepath.Join(root, "run", "current-system", "nixos-version")))
	if v.OS == "" {
		v.OS = osRelease(filepath.Join(root, "etc", "os-release"))
	}
	v.Revision = firstLine(readFile(filepath.Join(root, "run", "current-system", "configuration-revision")))
	// Read as links rather than followed: the store path is the identity, and
	// the two being different strings is the whole reading.
	v.Booted, _ = os.Readlink(filepath.Join(root, "run", "booted-system"))
	v.Current, _ = os.Readlink(filepath.Join(root, "run", "current-system"))
	return v
}

// Skewed is a machine that has been rebuilt and not rebooted.
//
// It is a method rather than a line, because it is the one thing in this
// section that is a conclusion: the running kernel and the running mesa are the
// booted generation's, and everything started since is the current one's. That
// mismatch is the cause this project's own flake names for a black screen from
// a TTY, and it is invisible in any log.
//
// Both empty is not skew, it is a machine with no generations - which is every
// machine that is not NixOS.
func (v Versions) Skewed() bool {
	return v.Booted != "" && v.Current != "" && v.Booted != v.Current
}

// probeHardware is the inventory.
func probeHardware(root string) Hardware {
	h := Hardware{}
	dmi := filepath.Join(root, "sys", "class", "dmi", "id")
	h.Model = strings.TrimSpace(firstLine(readFile(filepath.Join(dmi, "sys_vendor"))) + " " +
		firstLine(readFile(filepath.Join(dmi, "product_name"))))
	h.Board = strings.TrimSpace(firstLine(readFile(filepath.Join(dmi, "board_vendor"))) + " " +
		firstLine(readFile(filepath.Join(dmi, "board_name"))))
	h.CPU, h.Cores = cpuInfo(filepath.Join(root, "proc", "cpuinfo"))
	h.Memory = memTotal(filepath.Join(root, "proc", "meminfo"))
	// The presence of the directory and nothing inside it: the kernel creates
	// /sys/firmware/efi only when it booted through EFI, which is the whole
	// question.
	h.Firmware = "BIOS"
	if fi, err := os.Stat(filepath.Join(root, "sys", "firmware", "efi")); err == nil && fi.IsDir() {
		h.Firmware = "UEFI"
	}
	h.Cmdline = cut(kernelCmdline(firstLine(readFile(filepath.Join(root, "proc", "cmdline")))), cmdlineMax)
	h.Inputs = inputNames(filepath.Join(root, "proc", "bus", "input", "devices"))
	return h
}

// diskParams are the kernel parameters whose value is the identity of a
// filesystem rather than an instruction to the kernel.
//
// root= and resume= are a UUID or a device path, rd.luks.* and cryptdevice= are
// the encrypted volume this machine unlocks at boot, and resume_offset= is
// where in it the swap file starts. Every one of them travels with the file and
// none of them can cause or explain a black screen: the parameters that do -
// nomodeset, nvidia_drm.modeset=0, a module blacklist, an i915 option - are on
// the same line and are kept, along with everything else, including parameters
// this list has never heard of.
var diskParams = []string{
	"root",
	"resume",
	"resume_offset",
	"cryptdevice",
	"rd.luks.uuid",
	"rd.luks.name",
	"rd.lvm.lv",
	"rd.md.uuid",
	"rd.dm.uuid",
}

// kernelCmdline is /proc/cmdline with the disk taken out of it.
//
// The parameter's name stays and only its value goes, which is the whole
// balance of it: "this machine resumes from something" is a fact about how it
// boots and may well matter, and which volume it resumes from is a serial
// number for the disk. A person reading the file sees the parameter was there.
//
// Prefix-matched on the name before the first "=", so root=UUID=..., root=/dev/-
// nvme0n1p2 and root=LABEL=nixos are all the same parameter and all go. A bare
// word with no "=" is not a value to remove and is left alone: `ro`, `quiet`
// and `nomodeset` are the shape of most of this line.
func kernelCmdline(s string) string {
	fields := strings.Fields(s)
	for i, f := range fields {
		name, _, ok := strings.Cut(f, "=")
		if !ok {
			continue
		}
		for _, p := range diskParams {
			if name == p {
				// One word, with no space in it, because the line it goes back
				// into is read as words: a placeholder with a sentence in it
				// would be four more parameters as far as anything reading this
				// is concerned. What it means is in the file's own header.
				fields[i] = name + "=<removed>"
				break
			}
		}
	}
	return strings.Join(fields, " ")
}

// cpuInfo is the processor's own name for itself and how many the kernel sees.
func cpuInfo(path string) (model string, cores int) {
	for _, line := range strings.Split(readFile(path), "\n") {
		name, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		switch strings.TrimSpace(name) {
		case "model name":
			// The first block's. Every block carries the same string on a
			// machine with one socket, and on a machine with two the first is
			// still the answer to "what is this".
			if model == "" {
				model = cut(strings.TrimSpace(value), lineMax)
			}
		case "processor":
			// One line per thing the kernel schedules on, which is what this
			// count means and is the field every architecture with this file
			// has - aarch64 has no model name at all.
			cores++
		}
	}
	return model, cores
}

// memTotal is MemTotal as the kernel spells it, kB and all. Not converted:
// this is a reading, and a reading a person can compare with `free` beats one
// they have to trust the arithmetic of.
func memTotal(path string) string {
	for _, line := range strings.Split(readFile(path), "\n") {
		if value, ok := strings.CutPrefix(line, "MemTotal:"); ok {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

// inputNames is what the kernel calls each input device.
//
// The N: lines of /proc/bus/input/devices, which are quoted names. Nothing else
// off that file: it also carries the vendor, product and the event node, and
// the question this answers is "which keyboard is this", not "what is its USB
// id".
func inputNames(path string) []string {
	var out []string
	for _, line := range strings.Split(readFile(path), "\n") {
		value, ok := strings.CutPrefix(line, "N: Name=")
		if !ok {
			continue
		}
		if len(out) >= inputsMax {
			break
		}
		out = append(out, cut(strings.Trim(strings.TrimSpace(value), `"`), lineMax))
	}
	return out
}

// osRelease is PRETTY_NAME out of an os-release file, which is the one line of
// it a person recognises.
func osRelease(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	s := bufio.NewScanner(f)
	for s.Scan() {
		if value, ok := strings.CutPrefix(s.Text(), "PRETTY_NAME="); ok {
			return strings.Trim(value, `"`)
		}
	}
	return ""
}

// readFile is a sysfs or procfs read that answers with nothing when it cannot.
//
// Absent is a state here rather than an error, which is the rule the whole
// package is written to: every one of these files is missing on some machine
// this has to run on - a VM with no DMI, a container with no /sys/firmware -
// and a snapshot that refused to be written because a laptop had no board name
// would be a snapshot nobody ever gets.
//
// Bounded, because /proc and /sys hold files that are not files: reading
// /proc/kcore into the report would be the machine's memory.
//
// io.ReadAll over a LimitReader and not one Read, which is a distinction procfs
// insists on: /proc/cpuinfo answers a single read with as much as it has ready
// and no more, so the first version of this counted two cores on an eight-core
// machine. Anything reading a /proc file a page at a time has the same bug.
//
// readFileMax is 256 KiB, which is /proc/cpuinfo on a machine with a couple of
// hundred threads and larger than every other file here by three orders of
// magnitude. What comes out is bounded again by whoever reads it: nothing off
// these files reaches the report without going through cut.
func readFile(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, readFileMax))
	if err != nil {
		return ""
	}
	return string(data)
}

const readFileMax = 256 << 10

// The one-line readings above go through the package's own firstLine (gather.go):
// a sysfs file that is one value and a program's first line of complaint are the
// same string operation, and having two of it is how they drift.

// nulString is a fixed-width C string out of a syscall struct.
func nulString(b []byte) string {
	if i := strings.IndexByte(string(b), 0); i >= 0 {
		return string(b[:i])
	}
	return string(b)
}

// cut is one reading as long as it is allowed to be, saying so when it was cut.
//
// A bound and not a filter, which is the distinction the first version of this
// got wrong: its comment said these are kernel and compositor strings, which
// are ASCII, and one of them is a USB product string - whatever a device's
// maker wrote in a descriptor, in whatever bytes they liked, changed by putting
// a different device in a port and needing no account on this machine at all.
// What a string is allowed to contain is decided where it is written down
// instead (report.go, reading).
//
// Counted in characters, so that the cut lands between two of them: a bound in
// bytes can stop halfway through one, and half a character is a run of bytes
// that is not text - which the filter downstream then has to turn into a
// replacement mark, on a reading that was perfectly good UTF-8 before this
// touched it.
func cut(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + fmt.Sprintf(" ... (%d more characters)", len(r)-max)
}

// exists says whether a probe found anything, which is the difference between a
// blank line and a line saying nothing was found.
func exists(s string) bool { return strings.TrimSpace(s) != "" }
