package doctor

// The state snapshot: one file, written where the session starts, for somebody
// who cannot ask this machine anything.
//
// The failure it is for is the one docs/verify.md opens with. The session does
// not come up, the screen is black, and there is no terminal to type into. The
// journal survives that - services.journald.storage is persistent on every
// config in this flake, so greetd, niri, zded and the shell all have their
// words on the disk after a power cut - and what the journal has never held is
// what the machine looked like: the card, the driver bound to it, the
// generation that was booted against the one that was current. This file is
// that, plus the whole of `zde doctor`, in a place a person can reach by taking
// the disk out.
//
// Everything it does is bounded and nothing it does can hang. It is written by
// a unit at session start on a machine that has asked for it (zde.debug), which
// means it runs on the machine that is already broken - so a probe that waited
// would take away the one thing that was going to explain the wait.

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/crispuscrew/zde/internal/attn"
)

// ReportDir is where the files go: the root filesystem, not the ESP.
//
// /var/log because that is where a Linux machine keeps what it wants to be able
// to read after the fact, and because the ESP is small, shared with the
// bootloader, and vfat - which has no ownership and no permission bits, so a
// 0600 file cannot exist on it at all. The whole privacy half of this file
// would be a comment there.
//
// A directory per account under it, created by layer 0 (nix/system.nix) and
// owned by that account. Per account rather than one shared directory because
// there is then nothing to configure and nothing to get wrong: a single
// directory has to be given an owner, and an owner set to the wrong name is a
// snapshot that is silently never written on the one machine somebody set the
// option for.
//
// A var and not a const, and only a test ever assigns it. It was a const, and
// the cost of that was the defect below it: WriteReport is the door a session
// start comes through and there was no way to run it anywhere but /var/log/zde,
// so every test of this file entered at renderReport or at gather instead - and
// the one line that decides who this file's readings are gathered for was
// covered by nothing at all. It could be changed from anyone to owner with the
// whole suite green, and that change puts private desk names, the apps on them,
// the paths of manifests that would not parse and the resolver's quoted app
// name into a file written to a disk and pasted into bug reports.
var ReportDir = "/var/log/zde"

// reportsMax is how many are kept, and reportBytesMax bounds one of them.
//
// The arithmetic, done the way internal/attn does its own, because this is a
// directory that grows with events and this repo has been bitten by one of
// those twice - a notification flood filled a 16 GB tmpfs and took every shell
// on the machine with it (internal/attn, SendersMax). A log directory nobody
// bounded is the same defect with a slower clock.
//
// One file is what the sections below add up to. The header and the four
// section headings are two pages, about 4 KB - it grew when the header stopped
// making three promises the file underneath it was not keeping, and the length
// is the cost of the file saying what is in it rather than gesturing at it. Graphics is a verdict, up
// to cardsMax device lines, fellMax + sawMax log lines at lineMax (32 rows x
// 300 characters, which is where the row filter bounds them: 10 KB), the
// environment allowlist, and screensMax names: under 14 KB. Versions is a dozen
// lines of store paths, which are long: 2 KB. Hardware is DMI, a CPU name, the
// kernel command line filled across a few rows, and inputsMax device names at a
// row's bound: 20 KB at its absolute limit and about 2 KB on a laptop. The
// doctor report is one line per check, and its own count is the one thing here
// that something else decides - a machine with two hundred desks each naming an
// app nothing can run gets a line each. So 64 KiB is roughly three times the
// widest ordinary file and is enforced rather than derived: what does not fit
// is cut, and the cut says so on its own line, because a file that stopped in
// the middle of a word is one nobody can tell from a disk that filled up.
//
// Eight of them, at 64 KiB, is 512 KiB: the boot that broke and the seven
// before it, for less than the space of one photograph. Eight because the
// question a person actually has is "what changed", and that takes the last
// good one as well as the bad one - one file could only ever answer half of it.
//
// Eight is what this writer keeps, at one taken out for each one put in (see
// rotate). It is not a ceiling on the directory, because it cannot be: a
// process running as this account can write files into a directory that belongs
// to it, and a rotation that answered that by deleting more would be an easier
// way to lose eight real snapshots than to gain one fake one.
const (
	reportsMax     = 8
	reportBytesMax = 64 << 10
)

// bootHex is the shape of a boot id in a snapshot's name: eight lower-case hex
// characters, which is what the kernel writes and journalctl prints.
//
// One expression, read by every place that decides the shape. The name a file is
// written under and the name rotation will match have to agree, or the bound on
// the directory is not a bound.
const bootHex = `[0-9a-f]{8}`

// reportName is what a file in that directory is called, and the only thing
// this package will ever delete.
//
// The timestamp first, in UTC and with no colons, so that the names sort
// lexicographically into the order they were written - which is what makes
// keeping the newest eight an `ls` and a slice rather than eight stats. Then
// the boot id, because that is the string `journalctl --boot=` takes: this file
// and the journal beside it are two halves of one answer, and the name is how a
// person pairs them without guessing at times.
var reportName = regexp.MustCompile(`^\d{8}T\d{6}Z-` + bootHex + `\.txt$`)

// bootShape is the same eight characters on their own, for deciding whether a
// string may be part of a filename at all.
var bootShape = regexp.MustCompile(`^` + bootHex + `$`)

// bootID is the kernel's own id for this boot, cut to its first eight
// characters. Empty when the kernel does not say, which is a container rather
// than a machine - and empty as well when what it said is not a boot id, for
// which see bootHex.
//
// Eight and not thirty-two because of what it is for. It is not an argument -
// journalctl's `-b` takes the whole id and refuses a prefix ("Failed to add
// match: Invalid argument"), so nothing pastes this into a command. It is a
// handle: the person reading the disk runs `journalctl --list-boots`, finds the
// row whose id starts with these eight, and takes the full one off that row.
// Eight hex characters is one collision in four billion boots, and the whole
// filename stays short enough to read in an `ls`.
//
// The dashes come out because that is how journalctl prints it: the kernel
// writes a UUID with them and --list-boots shows 32 bare hex characters, and
// two spellings of the same id is exactly the confusion this is meant to remove.
func bootID(root string) string {
	id := strings.ReplaceAll(firstLine(readFile(filepath.Join(root, "proc", "sys", "kernel", "random", "boot_id"))), "-", "")
	if len(id) < 8 || !bootShape.MatchString(id[:8]) {
		return ""
	}
	return id[:8]
}

// WriteReport gathers everything, renders the file, and writes it.
//
// It answers with all three: where it went, what it says, and why there is no
// file. The text comes back even when the write failed on purpose - a machine
// with nowhere to write is a machine where somebody typed this into a terminal
// and still wants the answer, and the caller is what decides between the two
// (cmd/zde, report).
//
// One clock reading for both the header and the filename, so that a file's name
// and the time inside it cannot disagree.
func WriteReport(self Self) (path, text string, err error) {
	now := time.Now().UTC()
	// Gathered for anyone, and that is the whole of the privacy rule: nothing a
	// desk declaring private named is in what comes back, because no probe put
	// it there (gather.go, audience). The alternative was a pass over the
	// finished struct, which has to know every field that could carry a desk
	// name - and the field somebody adds next will not tell it.
	s := gather(anyone)
	if s.Status != nil {
		self.Zded = s.Status.Version
	}
	text = renderReport(s, GatherMachine(self), now)
	path, err = writeReport(ReportDir, text, now, bootID("/"))
	return path, text, err
}

// The section headings. Square brackets and lowercase, so that a person reading
// this in a pager can jump between them with a search that cannot collide with
// anything a machine put in the file.
const (
	secGraphics = "[graphics]"
	secVersions = "[versions]"
	secHardware = "[hardware]"
	secDoctor   = "[doctor]"
)

// reportHeader is what the file says about itself.
//
// It is here rather than in a doc comment because of where this file ends up.
// Somebody is going to paste it into a bug report, and the two questions they
// will have - is this safe to send, and what is missing from it - have to be
// answerable from the file itself and not from this repository.
//
// So the "what is in it" half is a list rather than a sentence, and the list is
// the answer to the question the first draft of this file got wrong. That draft
// said "readings: device nodes, driver names, chip ids", which is true and is
// not what a person weighing whether to paste it needs: the same file carried
// two filesystem UUIDs, this machine's hostname inside a store path, the
// account name, and twenty-seven input devices by name - one of which said out
// loud that there is remote-access software on the machine. Every one of those
// is either the point of the file or the cost of the reading beside it, so what
// changed is not what is captured but what the file admits to carrying.
//
// Two readings are narrowed rather than admitted to, because admitting to them
// would cost the file the thing it is for. The kernel command line keeps every
// parameter's name and the value of the ones that decide what gets drawn
// (machine.go, keptParams), so nomodeset survives and the MAC a machine
// netboots from does not. And whatever is holding this session awake is counted
// rather than named (gather.go, Logind.Named), because logind takes an
// inhibitor's name from the command line of the program that set it - which is
// how a backup running out of somebody's home directory came to be written down
// here with the path it was copying.
//
// One reading is admitted to rather than narrowed, and it is the only one: the
// lines niri itself logged about a renderer. They are journal content, the
// paragraph below promised there was none, and the list above it said they were
// here - so one of the two had to give. They stay because they are the
// evidence the first line of the file was read off, and because a snapshot that
// answered "niri says it never drew, go and read the journal" would be one more
// thing to do on the machine that will not boot. What changed is that the
// sentence about them says what a person is being asked to skim before they
// paste, and that the lines picked up are held to the word the sentence uses
// (machine.go, sawSigns).
//
// Every other promise is kept by something above or beside it: the allowlist
// (graphicsEnv), the monitors-and-never-windows rule (askNiri), and the private
// desks that are counted and never named (gather.go, audience).
const reportHeader = `zde state snapshot
%s

What this is: what this machine looked like when the session started - the
graphics device and whether the compositor ever reached a renderer on it, the
versions of everything, the hardware under it, and every check ` + "`zde doctor`" + ` makes.
A session with zde.debug on writes one of these when it starts, and ` + "`zde report`" + `
writes one at any time. It is meant to be read from another machine, off a disk
that will not boot.

What is in it, exactly. Readings, and this is the whole list of them, because
somebody is going to paste this file into a bug report:

  - the graphics device nodes, the drivers bound to them and the chip ids
  - up to %d lines niri itself logged this boot, whole and in niri's own words:
    the ones saying it could not reach a renderer, and the ones naming a
    renderer, a GPU, DRM, EGL, GBM or /dev/dri. That is journal content, and it
    is the only journal content here - it is in the file because it is the
    evidence the answer at the top was read off. Read it before you send this
  - the make and model of this machine, its motherboard and its processor, the
    memory the kernel sees, and whether it booted through UEFI or BIOS
  - the kernel command line: every parameter's name, and the value of the ones
    that decide what can be drawn - a module blacklist, an i915 or nvidia_drm
    option, video=, console=, systemd.unit=. Every word carrying no value at
    all is kept, because nomodeset is one of those and it is the answer to half
    the black screens there are. Every other value reads <removed>, so you can
    see that root=, ip=, BOOTIF= or systemd.setenv= was on the line without the
    disk, the address, the MAC or the variable that came after it
  - every input device the kernel names. That is your keyboard, and it is
    equally a security key, a tablet, a games controller, or the virtual
    keyboard a remote-desktop program creates: whatever is attached is here
    under the name its maker gave it
  - the account this session belongs to, paths under its home directory, its
    user id inside the runtime paths, its session id and its seat
  - kernel, system and package versions; the system's store path, which on
    NixOS has this machine's hostname in it; and the revision the configuration
    was built from, where it records one
  - the names niri gives your monitors, and the names your desks and the apps
    they declare are written under
  - what ` + "`zde doctor`" + ` checks, which is a handful of names of programs on this
    machine: the units a login starts and whether they are running, the program
    holding the notification name and its process id, the program configured to
    lock the screen, and the id logind gives this session

What is never in it: no notification text, no clipboard content, nothing
waiting in the queue, and no window titles - nothing here asks the compositor
what is on a screen. Nothing about the network: no MAC address, no IP address,
no wifi name. No machine-id, no firmware serial numbers, no monitor serial
numbers. No command line but the kernel's own: nothing here says what any
program on this machine was started with, and whatever is holding this session
awake is counted and never named, because logind takes an inhibitor's name from
the command line of the program that set it. No journal but the niri lines
above - no other unit's, and nothing out of your niri config except where one
of those lines quotes it. The environment is an allowlist of the dozen
variables that change what a compositor renders with, and never the environment
itself, which is where an API key would be.

A desk that declares ` + "`private: true`" + ` is counted and never named, and neither are
the apps on it nor the manifest file that would have named it.

The journal is the other half and it is not in here. It survives a reboot on
this machine, and docs/verify.md, section 1 says how to read it off a disk that
is mounted somewhere else.
`

// renderReport assembles the file. A pure function of what was gathered and the
// clock, so that every branch below - a machine that never drew, a machine with
// no niri, a machine with no cards at all - can be written down in a test on a
// machine that is none of them.
func renderReport(s Session, m Machine, now time.Time) string {
	var b strings.Builder
	// The bound is printed from the bound itself, because the header is a
	// promise about how much of somebody's journal is in the file and a promise
	// with a number typed into it goes stale the first time the number moves.
	fmt.Fprintf(&b, reportHeader, now.Format(time.RFC3339), fellMax+sawMax)
	b.WriteByte('\n')

	// Graphics first, and that is deliberate: it is the question somebody
	// staring at a black screen has, and a file that made them scroll past a
	// hardware inventory to reach it would be a file written for the person who
	// wrote it.
	writeGraphics(&b, m.Graphics)
	writeVersions(&b, m.Versions)
	writeHardware(&b, m.Hardware)
	writeDoctor(&b, s)

	return capped(b.String())
}

// capped is the file at its ceiling, saying so where it was cut.
//
// Cut at a line boundary, because a report that stops mid-sentence reads as a
// disk that filled up rather than as a bound somebody chose - and the person
// reading it is already looking for a machine that broke.
func capped(s string) string {
	if len(s) <= reportBytesMax {
		return s
	}
	const note = "\n... cut here: this snapshot reached its %d byte limit (internal/doctor, reportBytesMax)\n"
	room := reportBytesMax - len(fmt.Sprintf(note, reportBytesMax))
	if i := strings.LastIndexByte(s[:room], '\n'); i > 0 {
		room = i
	}
	return s[:room] + fmt.Sprintf(note, reportBytesMax)
}

// writeGraphics is the answer: did niri reach a real renderer, and on which
// device.
//
// The verdict is the first line and it is a sentence rather than a word,
// because the reader has no other context - this file is opened on a different
// machine, days later, by somebody who has forgotten what the two log lines
// mean. Everything under it is the evidence the verdict was read off, in the
// order it was weighed.
func writeGraphics(b *strings.Builder, g Graphics) {
	b.WriteString(secGraphics + "\n")
	// Filtered as one reading rather than card by card: what it is cut to is
	// the tail of a list of sixteen graphics devices, and every one of them has
	// a line of its own below.
	device := attn.Line(g.Device())
	switch g.Drew() {
	case DrewNo:
		fmt.Fprintf(b, "  answer     NO - niri came up and never reached a renderer. It says so itself, below.\n")
		fmt.Fprintf(b, "             This is the failure that looks like a black screen with a healthy log:\n")
		fmt.Fprintf(b, "             the compositor is alive, its sockets are open, and nothing is drawn.\n")
	case DrewProbably:
		fmt.Fprintf(b, "  answer     probably yes - nothing in niri's log this boot says it fell back, and\n")
		// What niri said, and not the length of a list that is bounded: a
		// machine with more outputs than screensMax would otherwise be told it
		// has sixteen (see Graphics.ScreensMore).
		fmt.Fprintf(b, "             niri has %d screen(s) to draw on. Nothing here can see a photon, so\n",
			len(g.Screens)+g.ScreensMore)
		fmt.Fprintf(b, "             a black screen with this line is a fault after the renderer.\n")
	default:
		fmt.Fprintf(b, "  answer     not known - %s.\n", g.Unsure())
	}
	if device != "" {
		fmt.Fprintf(b, "  device     %s\n", device)
	} else {
		fmt.Fprintf(b, "  device     none: this machine has no DRM card node at all, which is a machine\n")
		fmt.Fprintf(b, "             with no graphics driver bound and never a compositor that can draw\n")
	}

	// niri, as it answered.
	switch {
	case g.NiriErr != nil:
		fmt.Fprintf(b, "  niri       not answering: %s\n", reading(g.NiriErr.Error()))
	case len(g.Screens) == 0:
		fmt.Fprintf(b, "  niri       answering on %s, and it has a screen for none of its outputs - a\n", reading(g.Socket))
		fmt.Fprintf(b, "             compositor with nowhere to draw is a black screen however well its\n")
		fmt.Fprintf(b, "             renderer started. niri switching a monitor off itself reads this way\n")
	default:
		fmt.Fprintf(b, "  niri       answering on %s\n", reading(g.Socket))
		// The monitors niri is drawing on and not every connector it lists, for
		// the reason askNiri gives. So a monitor that is plugged in and dark is
		// absent from this row, which is the reading rather than a gap in it.
		//
		// Each name and then the join, rather than the join and then the
		// filter: a monitor called "a b" and two monitors called "a" and "b"
		// are different machines, and one filter over the joined string cannot
		// keep them apart.
		names := reading(strings.Join(each(g.Screens), " "))
		if g.ScreensMore > 0 {
			// The mark every other cut reading in this file carries: a list
			// that stopped without saying so reads as all of them.
			names += fmt.Sprintf(" ... (%d more)", g.ScreensMore)
		}
		fmt.Fprintf(b, "  screens    %s\n", names)
	}

	// The cards, one line each. Written even when the verdict was decided
	// without them: "on which device" is half the question.
	switch {
	case g.CardsErr != nil:
		fmt.Fprintf(b, "  cards      could not be listed: %s\n", reading(g.CardsErr.Error()))
	case len(g.Cards) == 0:
		fmt.Fprintf(b, "  cards      none\n")
	}
	for _, c := range g.Cards {
		driver := c.Driver
		if driver == "" {
			driver = "no driver bound"
		}
		// Node, driver and chip id are three columns of one row, so each is
		// filtered on its own: a driver name carrying a newline would otherwise
		// take the pci id onto a line of its own and leave it reading as a row.
		line := fmt.Sprintf("  card       %-20s %-14s", reading(c.Node), reading(driver))
		if c.PCI != "" {
			line += " pci " + reading(c.PCI)
		}
		if c.OpenErr != nil {
			// The reading nothing else on the machine makes. A card that is
			// there, with a driver bound, that this account may not open is a
			// black screen with no complaint anywhere above it.
			line += fmt.Sprintf("\n             this session cannot open it: %s", reading(c.OpenErr.Error()))
		}
		b.WriteString(line + "\n")
	}

	// niri's own words, verbatim - which here means every word of them a
	// terminal will not act on, since this file is printed to one.
	switch {
	case g.LogErr != nil:
		fmt.Fprintf(b, "  log        could not be read: %s\n", reading(g.LogErr.Error()))
		fmt.Fprintf(b, "             (journalctl --user -b -u %s is the same question by hand)\n", niriUnit)
	case len(g.Fell) == 0 && len(g.Saw) == 0:
		fmt.Fprintf(b, "  log        %s said nothing about a renderer this boot\n", niriUnit)
	}
	for _, l := range g.Fell {
		fmt.Fprintf(b, "  FELL BACK  %s\n", reading(l))
	}
	for _, l := range g.Saw {
		fmt.Fprintf(b, "  log        %s\n", reading(l))
	}

	for _, e := range g.Env {
		if !e.Set {
			continue
		}
		// The name is this package's own (graphicsEnv) and the value is
		// whatever started the session, which is why only one of them is
		// filtered - and why the demonstration that opened this fix was a
		// variable with a newline in it.
		//
		// attn.Line and not reading: a variable set to nothing is a reading of
		// its own here, since LIBGL_ALWAYS_SOFTWARE= and no LIBGL_ALWAYS_-
		// SOFTWARE are different instructions to the stack that reads them
		// (machine.go, EnvVar), and "not known" would be the wrong word for it.
		fmt.Fprintf(b, "  env        %s=%s\n", e.Name, attn.Line(e.Value))
	}
	b.WriteByte('\n')
}

// each is a list of readings, every one of them filtered.
func each(all []string) []string {
	out := make([]string, 0, len(all))
	for _, s := range all {
		out = append(out, reading(s))
	}
	return out
}

// writeVersions is what this is a version of, and whether the running machine
// and the built machine are the same one.
func writeVersions(b *strings.Builder, v Versions) {
	b.WriteString(secVersions + "\n")
	fmt.Fprintf(b, "  zde        %s\n", orNone(v.Zde))
	if v.Zded == "" {
		fmt.Fprintf(b, "  zded       not answering, so its version is not known\n")
	} else {
		// A reading and not orNone, which is the difference this row got wrong.
		// zde's own version is set at link time and nothing else can touch it;
		// the daemon's arrives as JSON over a socket, and a socket in this
		// account's runtime directory is a thing any process running as this
		// account can answer on. Printed raw it was the one scalar in the three
		// machine sections that skipped the filter, and a crafted version string
		// opened a second [doctor] heading and put an escape into a file that is
		// printed straight to a terminal.
		fmt.Fprintf(b, "  zded       %s\n", reading(v.Zded))
		if v.Zde != "" && v.Zded != v.Zde {
			// Worth a line of its own: a rebuild replaces the binary and leaves
			// the running daemon where it was, so these two disagreeing means
			// the session has not been restarted since the switch.
			fmt.Fprintf(b, "             and this zde is %s: the daemon has not been restarted since the rebuild\n", v.Zde)
		}
	}
	if v.NiriErr != nil {
		if errors.Is(v.NiriErr, exec.ErrNotFound) {
			fmt.Fprintf(b, "  niri       not installed\n")
		} else {
			fmt.Fprintf(b, "  niri       could not be asked: %s\n", reading(v.NiriErr.Error()))
		}
	} else {
		fmt.Fprintf(b, "  niri       %s\n", reading(v.Niri))
	}
	fmt.Fprintf(b, "  kernel     %s\n", reading(v.Kernel))
	fmt.Fprintf(b, "  os         %s\n", reading(v.OS))
	if v.Revision != "" {
		fmt.Fprintf(b, "  revision   %s\n", reading(v.Revision))
	}
	if v.Booted != "" || v.Current != "" {
		fmt.Fprintf(b, "  booted     %s\n", reading(v.Booted))
		fmt.Fprintf(b, "  current    %s\n", reading(v.Current))
	}
	if v.Skewed() {
		// The named cause, said as what it costs. The running kernel and the
		// running mesa belong to the booted generation and everything started
		// since belongs to the current one, and that skew is what this project's
		// own flake blames for a black screen from a TTY (flake.nix, the niri
		// input; docs/update.md, boot rather than switch).
		fmt.Fprintf(b, "  SKEW       this machine was rebuilt and not rebooted: the kernel and the graphics\n")
		fmt.Fprintf(b, "             stack running are the booted generation's and everything started since\n")
		fmt.Fprintf(b, "             is the current one's. That mismatch is the ordinary cause of a black\n")
		fmt.Fprintf(b, "             screen after a switch (docs/update.md - boot, rather than switch, for a\n")
		fmt.Fprintf(b, "             kernel or a mesa change).\n")
	}
	b.WriteByte('\n')
}

// writeHardware is the inventory: what machine this report is about.
//
// Everything on it but the core count and the firmware word is a string some
// firmware, some kernel or some device's own descriptor supplied, so every one
// of them goes through the filter above. A motherboard's name is whatever the
// vendor wrote in the DMI table, and an input device's is whatever its maker
// put in a USB descriptor - which needs no account on this machine to change,
// only a hand and a port.
func writeHardware(b *strings.Builder, h Hardware) {
	b.WriteString(secHardware + "\n")
	fmt.Fprintf(b, "  model      %s\n", reading(h.Model))
	fmt.Fprintf(b, "  board      %s\n", reading(h.Board))
	fmt.Fprintf(b, "  cpu        %s\n", reading(h.CPU))
	if h.Cores > 0 {
		fmt.Fprintf(b, "  cores      %d\n", h.Cores)
	}
	fmt.Fprintf(b, "  memory     %s\n", reading(h.Memory))
	fmt.Fprintf(b, "  firmware   %s\n", h.Firmware)
	writeWords(b, "cmdline", strings.Fields(h.Cmdline))
	if len(h.Inputs) == 0 {
		fmt.Fprintf(b, "  input      none the kernel names\n")
	}
	for _, name := range h.Inputs {
		fmt.Fprintf(b, "  input      %s\n", reading(name))
	}
	b.WriteByte('\n')
}

// writeDoctor is `zde doctor` in full, judged off the same gather.
//
// In full and not summarised: it is the half of this file that says whether the
// session that did come up was well, and every line of it is already written to
// be read by somebody with nothing else to look anything up on (this package's
// own doc).
//
// Nothing is taken out here, and that is the fix rather than an omission. This
// used to be a pass over the gathered session that dropped the private desks
// out of one field, and it was wrong in the way a pass is always wrong: two
// other fields carried the same names - the resolver's error, which quotes
// whichever app was asked first, and zded's list of manifests it could not
// read, every entry of which begins with a path that is a desk's name - and the
// pass had no way to know. So the readings this section is judged off were
// gathered for a reader who is not the owner and never had those names in them
// (gather.go, audience), and the counts that survive are printed by the checks
// themselves (doctor.go, hiddenApps and manifests).
//
// What is left here is the one line that is about the file rather than about
// the machine.
func writeDoctor(b *strings.Builder, s Session) {
	b.WriteString(secDoctor + "\n")
	for _, c := range Judge(s) {
		// Every line of the check and not only its first, because a check is
		// more than one line where its detail is (doctor.go, String). Indenting
		// the first alone would leave a continuation two columns to the left of
		// the detail it continues, and this file's own two spaces are what keep
		// column one for a section heading.
		fmt.Fprintf(b, "  %s\n", strings.ReplaceAll(c.String(), "\n", "\n  "))
	}
	if s.Desks.Private > 0 {
		// Said even when nothing was left out, because "there is a private desk
		// on this machine and it had nothing to report" and "there is no
		// private desk" are the same blank otherwise - and somebody deciding
		// whether to paste this into a bug report is entitled to know which.
		fmt.Fprintf(b, "  %-*s %-*s %d desk(s) declare private, and nothing about them is in this file\n",
			levelAt, "note", nameAt, "desk apps", s.Desks.Private)
	}
}

// reading is one thing this machine said, on its way into one row of this file:
// filtered, and never a blank - a line ending in nothing reads as a bug in
// whatever wrote it.
//
// The one door every scalar in the three machine sections goes through, and it
// is a door because of what was on the other side of it. Almost nothing in this
// file was written by zde. A USB product string is whatever its maker put in
// the descriptor, and plugging a device in needs no account on this machine; an
// environment value is set by whatever started the session; a DMI string is
// firmware; a niri log line is a compositor's. All of it lands in a file that
// is read in a pager and often printed straight to a terminal (cmd/zde,
// runReport), where ESC is not a character but the start of an instruction.
//
// attn.Line, which is the filter for foreign text on its way into a row, and
// the choice between the three is the shape of what is being made. This section
// is a two-column list: a label, then one reading. A newline in a reading is
// therefore not shape, it is a row nobody made - and column one of this file is
// where a section heading goes, so a value carrying "\n[doctor]" opens a second
// doctor section and a value carrying "\nok    forged    ..." is a check that
// was never run, in the column an eye runs down. attn.Text would leave both
// where they are, because keeping shape is its whole job; attn.Block would push
// them one indent in, which stops a heading and does not stop a forged row,
// since every row in this file is already indented. Line is the only one of the
// three that makes the forgery impossible rather than harder, and reflowing a
// reading costs nothing: it was one line to begin with or it was lying.
//
// The doctor section under this one took the other answer, and the two are not
// in disagreement: the filter follows the shape. A check's detail is a sentence
// zde often wrote itself and has to arrive whole, on a row whose level is in
// column one, so it keeps its shape and every line after the first is indented
// to the detail column, where nothing else in either shape of this report sits
// (doctor.go, String). A reading is a scalar in a list where every row is
// already two spaces in, so no indent tells it apart from a row, and it was one
// line to begin with or it was lying.
//
// Its bound comes with it - a queue row's - and that is deliberate here too: an
// environment value has no length a machine can rely on, and the report's own
// ceiling should be reached by a machine with forty input devices rather than
// by one variable. Nothing in these three sections is near it on a real
// machine, which is the other half of why this one keeps a silent cut and the
// detail column could not: the readings that could be long are cut with a
// marker before they arrive (machine.go, lineMax and cmdlineMax), and the rest
// are a socket path, a driver name, a store path and a DMI string.
func reading(s string) string {
	out := attn.Line(s)
	if out == "" {
		return "not known"
	}
	return out
}

// The two columns every row in the three machine sections is made of: two
// spaces, a label eleven wide, and the reading. wrapAt is where a row that
// carries a list of words is filled onto the next one.
const (
	valueAt = 13
	wrapAt  = 79
)

// writeWords is one reading that is a list of words, filled across as many rows
// as it takes rather than cut to fit one.
//
// The kernel command line is the only reading shaped like this and it is why
// this exists. It is the one field in the file that a row's bound would ruin:
// an initrd and two store paths run past a queue row's three hundred characters
// long before the line reaches nomodeset or nvidia_drm.modeset=0, which are the
// words somebody is reading it for, and a filter that quietly dropped the tail
// would take exactly the part that explains the black screen.
//
// The other two filters were both wrong here, and the second one instructively
// so. attn.Text keeps newlines, which is the forgery itself. attn.Block indents
// what follows a newline by two - which stops a heading, since a heading is in
// column one, and does not stop anything else: every row in this file is
// already two spaces in, so a value carrying "\nok    forged   ..." lands
// exactly where a check result goes and reads as one. Block is for a message
// printed on its own in column one, which is what cmd/zde does with an error;
// inside a file whose shape is an indent it buys nothing.
//
// So the line is split at the word boundaries it already has, every word goes
// through the row filter on its own, and the wrap is this package's. What
// reaches any row is whole parameters, `grep nomodeset` still finds it, and no
// word can end a row because none of them can contain a newline any more.
func writeWords(b *strings.Builder, label string, words []string) {
	indent := strings.Repeat(" ", valueAt)
	line, empty := fmt.Sprintf("  %-11s", label), true
	for _, w := range words {
		w = reading(w)
		if !empty && len(line)+1+len(w) > wrapAt {
			b.WriteString(line + "\n")
			line, empty = indent, true
		}
		if !empty {
			line += " "
		}
		line, empty = line+w, false
	}
	if empty {
		line += "not known"
	}
	b.WriteString(line + "\n")
}

// orNone is a reading, or the fact that there was none. Never a blank: a line
// ending in nothing reads as a bug in whatever wrote it.
//
// zde's own strings, which is the difference between it and reading above: a
// version set at link time, the daemon's answer about itself. Nothing filters
// them because nothing else wrote them.
func orNone(s string) string {
	if !exists(s) {
		return "not known"
	}
	return strings.TrimSpace(s)
}

// writeReport puts the text in dir, under a name made of the time and the boot,
// and then keeps only the newest reportsMax.
//
// O_EXCL and O_NOFOLLOW rather than the temp-file-and-rename the notification
// snapshot uses, because the two files want different things. That one replaces
// itself and must never be found half written; this one has a name nothing else
// can have, so refusing to overwrite is free and is the stronger promise - and
// O_NOFOLLOW means a symlink planted at tomorrow's name cannot make this write a
// session's inventory into somebody else's file.
//
// 0600 from the open, and owned by whoever is running: this is written by the
// session, into a directory layer 0 made for that account (nix/system.nix).
//
// A write that failed leaves nothing. That is the other half of O_EXCL and it
// was missing: a disk that filled up halfway through, or an RLIMIT_FSIZE, used
// to leave the first half of a report on the disk under a name that looks like
// a whole one, with no "cut here" line in it and nothing anywhere saying it was
// cut - while this returned no path at all, so the person was told no file was
// written and the file was there. The two together are the worst shape this can
// have: a file nobody knows about that reads as complete and stops in the
// middle of the section somebody needed. Removed, and then a retry in the same
// second works too, which O_EXCL had otherwise made impossible.
func writeReport(dir, text string, now time.Time, boot string) (string, error) {
	// The name has to be one rotate will match, or this file is outside the
	// bounds this package keeps (see bootHex). Checked here as well as at the
	// read, because the boot id is the tail of a filepath.Join: a string with a
	// separator in it decides where the file goes and not only what it is
	// called. The zeroes are what a machine with no boot id gets, and mean the
	// same thing - this file cannot be paired with a boot.
	if !bootShape.MatchString(boot) {
		boot = "00000000"
	}
	dir = filepath.Join(dir, whoami())
	name := fmt.Sprintf("%s-%s.txt", now.Format("20060102T150405Z"), boot)
	path := filepath.Join(dir, name)
	// Deliberately not created here. The directory belongs to root and is made
	// by layer 0 when zde.debug is on, so a machine where it is missing is a
	// machine that did not ask for this - and a snapshot writer that quietly
	// made itself somewhere to write would be one that works on the machine
	// nobody configured and silently writes to the wrong place on the machine
	// somebody did.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return "", err
	}
	if err := writeWhole(f, text); err != nil {
		// Closed here whatever it was that failed. writeWhole closes it on its
		// own way out, so this is either the close that never happened or one
		// that answers "already closed" - and a descriptor left open on the
		// error path is the one thing that would outlive the failure.
		f.Close()
		if rm := os.Remove(path); rm != nil {
			// Both, because the second one changes what is on the disk and
			// therefore what the person has to do about it.
			return "", fmt.Errorf("%w - and the part of it already written could not be removed: %v", err, rm)
		}
		return "", err
	}
	// Best effort, and after the file is safely down: a directory that could not
	// be tidied is not a reason to lose the snapshot that was just written.
	rotate(dir, reportsMax, name)
	return path, nil
}

// writeWhole is the file on the disk or an error, with nothing in between: the
// bytes, the sync and the close, and the first of them that fails is the answer.
//
// The sync is here because the whole premise is a machine that is about to be
// powered off by somebody holding the button down, and the close is checked
// beside it because a close is where a write over a full filesystem is allowed
// to surface - a snapshot that reported success off an ignored close would be
// the same truncated file with a better story.
func writeWhole(f *os.File, text string) error {
	if _, err := f.WriteString(text); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	return f.Close()
}

// rotate makes room for the file just written, and takes out at most the one it
// replaced.
//
// Three rules, and each of them is here because leaving it out was a way of
// steering this into deleting the snapshots it exists to keep.
//
// Only files whose name this package generated, matched whole. That is the
// difference between a bound and a program that deletes things: the directory
// is on the root filesystem and a person may well have copied something into it
// while debugging, and a rotation that removed whatever was oldest would remove
// that.
//
// Nothing that sorts at or above the name just written. The timestamp is
// fixed-width UTC, so that is every name claiming a moment this write had not
// reached - which is a name this package cannot have written before now, and is
// therefore either something else's or this machine's own from a clock that has
// since been corrected. Neither is a reason to delete eight real snapshots.
// Without this rule one file called 29991231T235959Z-ffffffff.txt is enough to
// evict a genuine one on every write for ever, and eight of them evict all
// eight - while `zde report` goes on printing the path of a file it deleted a
// microsecond after writing it, because the file it had just written sorted
// below all of them. The file just written is inside this rule and so can never
// be the one that goes, which is what makes that line true.
//
// And one file per file written, however many are over the bound. A snapshot
// writer that removes eight things because of one write is a tool, and a
// process running as this account can plant names anywhere in the order this
// reads - it need only date them a second ago rather than a century on. It can
// also unlink these files outright, and nothing here can stop that; what this
// rule buys is that rotation is never the instrument, since one write can cost
// at most the one it replaced. The directory converges on keep at a snapshot a
// boot, which is the rate it fills up at.
func rotate(dir string, keep int, wrote string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	var ours []string
	for _, e := range entries {
		if e.IsDir() || !reportName.MatchString(e.Name()) || e.Name() > wrote {
			continue
		}
		ours = append(ours, e.Name())
	}
	if len(ours) <= keep {
		return
	}
	sort.Strings(ours)
	os.Remove(filepath.Join(dir, ours[0]))
}

// whoami is the account this session belongs to, which is the directory layer 0
// made for it.
//
// The name and not the uid, because a person mounting the disk on another
// machine reads the directory listing and has to recognise it - and the uid
// there is a number whose meaning left with the passwd file. The uid is the
// fallback for a session whose user cannot be looked up at all, which is a
// container rather than a login.
func whoami() string {
	if u, err := user.Current(); err == nil && u.Username != "" {
		return u.Username
	}
	return strconv.Itoa(os.Getuid())
}
