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
const ReportDir = "/var/log/zde"

// reportsMax is how many are kept, and reportBytesMax bounds one of them.
//
// The arithmetic, done the way internal/attn does its own, because this is a
// directory that grows with events and this repo has been bitten by one of
// those twice - a notification flood filled a 16 GB tmpfs and took every shell
// on the machine with it (internal/attn, SendersMax). A log directory nobody
// bounded is the same defect with a slower clock.
//
// One file is what the sections below add up to. The header and the four
// section headings are a page, about 2 KB. Graphics is a verdict, up to
// cardsMax device lines, fellMax + sawMax log lines cut to lineMax (32 lines x
// 400 bytes = 13 KB), the environment allowlist, and outputsMax names: under 16
// KB. Versions is a dozen lines of store paths, which are long: 2 KB. Hardware
// is DMI, a CPU name, a kernel command line and inputsMax device names at
// lineMax: 26 KB at its absolute limit and about 2 KB on a laptop. The doctor
// report is one line per check, and its own count is the one thing here that
// something else decides - a machine with two hundred desks each naming an app
// nothing can run gets a line each. So 64 KiB is roughly four times the widest
// ordinary file and is enforced rather than derived: what does not fit is cut,
// and the cut says so on its own line, because a file that stopped in the
// middle of a word is one nobody can tell from a disk that filled up.
//
// Eight of them, at 64 KiB, is 512 KiB: the boot that broke and the seven
// before it, for less than the space of one photograph. Eight because the
// question a person actually has is "what changed", and that takes the last
// good one as well as the bad one - one file could only ever answer half of it.
const (
	reportsMax     = 8
	reportBytesMax = 64 << 10
)

// reportName is what a file in that directory is called, and the only thing
// this package will ever delete.
//
// The timestamp first, in UTC and with no colons, so that the names sort
// lexicographically into the order they were written - which is what makes
// keeping the newest eight an `ls` and a slice rather than eight stats. Then
// the boot id, because that is the string `journalctl --boot=` takes: this file
// and the journal beside it are two halves of one answer, and the name is how a
// person pairs them without guessing at times.
var reportName = regexp.MustCompile(`^\d{8}T\d{6}Z-[0-9a-f]{8}\.txt$`)

// bootID is the kernel's own id for this boot, cut to its first eight
// characters. Empty when the kernel does not say, which is a container rather
// than a machine.
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
	if len(id) < 8 {
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
	s := Gather()
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
// answerable from the file itself and not from this repository. So it says both
// out loud, and every promise in it is kept by something above: the allowlist
// (graphicsEnv), the outputs-and-never-windows rule (askNiri), and the private
// desks that are counted and never named (redactPrivate).
const reportHeader = `zde state snapshot
%s

What this is: what this machine looked like when the session started - the
graphics device and whether the compositor ever reached a renderer on it, the
versions of everything, the hardware under it, and every check ` + "`zde doctor`" + ` makes.
A session with zde.debug on writes one of these when it starts, and ` + "`zde report`" + `
writes one at any time. It is meant to be read from another machine, off a disk
that will not boot.

What is in it, exactly: readings. Device nodes, driver names, chip ids, kernel
and system versions, store paths, the kernel command line, the names the kernel
gives your input devices and the names niri gives your monitors, and the lines
niri itself logged about renderers this boot.

What is never in it: no notification text, no clipboard content, nothing
waiting in the queue, and no window titles - nothing here asks the compositor
what is on a screen. A desk that declares ` + "`private: true`" + ` is counted and never
named, and neither are the apps on it. The environment is an allowlist of the
dozen variables that change what a compositor renders with, and never the
environment itself, which is where an API key would be.

Desk names, app names and monitor names are configuration and are here. What
you were sent and what you wrote is not.

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
	fmt.Fprintf(&b, reportHeader, now.Format(time.RFC3339))
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
	device := g.Device()
	switch g.Drew() {
	case DrewNo:
		fmt.Fprintf(b, "  answer     NO - niri came up and never reached a renderer. It says so itself, below.\n")
		fmt.Fprintf(b, "             This is the failure that looks like a black screen with a healthy log:\n")
		fmt.Fprintf(b, "             the compositor is alive, its sockets are open, and nothing is drawn.\n")
	case DrewProbably:
		fmt.Fprintf(b, "  answer     probably yes - nothing in niri's log this boot says it fell back, and\n")
		fmt.Fprintf(b, "             niri is answering with %d output(s). Nothing here can see a photon, so\n", len(g.Outputs))
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
		fmt.Fprintf(b, "  niri       not answering: %s\n", g.NiriErr)
	case len(g.Outputs) == 0:
		fmt.Fprintf(b, "  niri       answering on %s, and listing no outputs at all - a compositor with\n", g.Socket)
		fmt.Fprintf(b, "             nowhere to draw is a black screen however well its renderer started\n")
	default:
		fmt.Fprintf(b, "  niri       answering on %s\n", g.Socket)
		fmt.Fprintf(b, "  outputs    %s\n", strings.Join(g.Outputs, " "))
	}

	// The cards, one line each. Written even when the verdict was decided
	// without them: "on which device" is half the question.
	switch {
	case g.CardsErr != nil:
		fmt.Fprintf(b, "  cards      could not be listed: %s\n", g.CardsErr)
	case len(g.Cards) == 0:
		fmt.Fprintf(b, "  cards      none\n")
	}
	for _, c := range g.Cards {
		driver := c.Driver
		if driver == "" {
			driver = "no driver bound"
		}
		line := fmt.Sprintf("  card       %-20s %-14s", c.Node, driver)
		if c.PCI != "" {
			line += " pci " + c.PCI
		}
		if c.OpenErr != nil {
			// The reading nothing else on the machine makes. A card that is
			// there, with a driver bound, that this account may not open is a
			// black screen with no complaint anywhere above it.
			line += fmt.Sprintf("\n             this session cannot open it: %s", c.OpenErr)
		}
		b.WriteString(line + "\n")
	}

	// niri's own words, verbatim.
	switch {
	case g.LogErr != nil:
		fmt.Fprintf(b, "  log        could not be read: %s\n", g.LogErr)
		fmt.Fprintf(b, "             (journalctl --user -b -u %s is the same question by hand)\n", niriUnit)
	case len(g.Fell) == 0 && len(g.Saw) == 0:
		fmt.Fprintf(b, "  log        %s said nothing about a renderer this boot\n", niriUnit)
	}
	for _, l := range g.Fell {
		fmt.Fprintf(b, "  FELL BACK  %s\n", l)
	}
	for _, l := range g.Saw {
		fmt.Fprintf(b, "  log        %s\n", l)
	}

	for _, e := range g.Env {
		if !e.Set {
			continue
		}
		fmt.Fprintf(b, "  env        %s=%s\n", e.Name, cut(e.Value, lineMax))
	}
	b.WriteByte('\n')
}

// writeVersions is what this is a version of, and whether the running machine
// and the built machine are the same one.
func writeVersions(b *strings.Builder, v Versions) {
	b.WriteString(secVersions + "\n")
	fmt.Fprintf(b, "  zde        %s\n", orNone(v.Zde))
	if v.Zded == "" {
		fmt.Fprintf(b, "  zded       not answering, so its version is not known\n")
	} else {
		fmt.Fprintf(b, "  zded       %s\n", v.Zded)
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
			fmt.Fprintf(b, "  niri       could not be asked: %s\n", v.NiriErr)
		}
	} else {
		fmt.Fprintf(b, "  niri       %s\n", orNone(v.Niri))
	}
	fmt.Fprintf(b, "  kernel     %s\n", orNone(v.Kernel))
	fmt.Fprintf(b, "  os         %s\n", orNone(v.OS))
	if v.Revision != "" {
		fmt.Fprintf(b, "  revision   %s\n", v.Revision)
	}
	if v.Booted != "" || v.Current != "" {
		fmt.Fprintf(b, "  booted     %s\n", orNone(v.Booted))
		fmt.Fprintf(b, "  current    %s\n", orNone(v.Current))
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
func writeHardware(b *strings.Builder, h Hardware) {
	b.WriteString(secHardware + "\n")
	fmt.Fprintf(b, "  model      %s\n", orNone(h.Model))
	fmt.Fprintf(b, "  board      %s\n", orNone(h.Board))
	fmt.Fprintf(b, "  cpu        %s\n", orNone(h.CPU))
	if h.Cores > 0 {
		fmt.Fprintf(b, "  cores      %d\n", h.Cores)
	}
	fmt.Fprintf(b, "  memory     %s\n", orNone(h.Memory))
	fmt.Fprintf(b, "  firmware   %s\n", h.Firmware)
	fmt.Fprintf(b, "  cmdline    %s\n", orNone(h.Cmdline))
	if len(h.Inputs) == 0 {
		fmt.Fprintf(b, "  input      none the kernel names\n")
	}
	for _, name := range h.Inputs {
		fmt.Fprintf(b, "  input      %s\n", name)
	}
	b.WriteByte('\n')
}

// writeDoctor is `zde doctor` in full, judged off the same gather.
//
// In full and not summarised: it is the half of this file that says whether the
// session that did come up was well, and every line of it is already written to
// be read by somebody with nothing else to look anything up on (this package's
// own doc). What it is not allowed to carry here is the one thing a terminal
// may show its owner and a file may not - see redactPrivate.
func writeDoctor(b *strings.Builder, s Session) {
	s, hidden := redactPrivate(s)
	b.WriteString(secDoctor + "\n")
	for _, c := range Judge(s) {
		fmt.Fprintf(b, "  %s\n", c)
	}
	if hidden > 0 {
		fmt.Fprintf(b, "  %-5s %-13s %d app(s) on desks that declare private are left out of the count\n",
			"note", "desk apps", hidden)
		fmt.Fprintf(b, "  %-5s %-13s above and are not named here. `zde doctor` in a terminal shows them.\n", "", "")
	}
	if hidden == 0 && s.Desks.Private > 0 {
		// Said even when it changes nothing, because "there is a private desk on
		// this machine and it had nothing to report" and "there is no private
		// desk" are the same blank otherwise - and somebody deciding whether to
		// paste this into a bug report is entitled to know which.
		fmt.Fprintf(b, "  %-5s %-13s %d desk(s) declare private, and nothing about them is in this file\n",
			"note", "desk apps", s.Desks.Private)
	}
}

// redactPrivate takes the desks that declare private out of what is about to be
// written down, and says how many entries went.
//
// This is the rule the whole file is written around, and it is stricter here
// than anywhere else in zde for one reason: this is a file on a disk that is
// meant to be carried to another machine and pasted into a bug report. A
// private desk is history only (docs/vision.md, section 3) - the notification
// snapshot already leaves its arrivals out entirely (internal/attn, Snapshot) -
// and a report that named the desk and the three apps on it would say what that
// desk is for to everybody who ever reads it.
//
// Both halves go: the desk's name and the app's. Naming the app and hiding the
// desk would be the same disclosure with an extra step.
//
// A count survives, and that is the deliberate limit of it. A file that
// silently dropped lines would be a file whose all-clear cannot be trusted, and
// "three apps on a desk you declared private could not be started" is a real
// fault somebody has to be told about without being told which.
func redactPrivate(s Session) (Session, int) {
	kept := make([]DeskApp, 0, len(s.Desks.Unrunnable))
	hidden := 0
	for _, a := range s.Desks.Unrunnable {
		if a.Private {
			hidden++
			continue
		}
		kept = append(kept, a)
	}
	s.Desks.Unrunnable = kept
	return s, hidden
}

// orNone is a reading, or the fact that there was none. Never a blank: a line
// ending in nothing reads as a bug in whatever wrote it.
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
func writeReport(dir, text string, now time.Time, boot string) (string, error) {
	if boot == "" {
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
	defer f.Close()
	if _, err := f.WriteString(text); err != nil {
		return "", err
	}
	// Synced, because the whole premise is a machine that is about to be
	// powered off by somebody holding the button down.
	if err := f.Sync(); err != nil {
		return "", err
	}
	// Best effort, and after the file is safely down: a directory that could not
	// be tidied is not a reason to lose the snapshot that was just written.
	rotate(dir, reportsMax)
	return path, nil
}

// rotate keeps the newest keep files and removes the rest.
//
// Only files whose name this package generated, matched whole. That is the
// difference between a bound and a program that deletes things: the directory
// is on the root filesystem and a person may well have copied something into it
// while debugging, and a rotation that removed whatever was oldest would remove
// that.
//
// By name and not by mtime, which is what the name is shaped for: the timestamp
// is fixed-width UTC, so lexicographic order is chronological order, and a
// clock that jumped backwards between two boots cannot make this delete the
// newest file.
func rotate(dir string, keep int) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	var ours []string
	for _, e := range entries {
		if !e.IsDir() && reportName.MatchString(e.Name()) {
			ours = append(ours, e.Name())
		}
	}
	if len(ours) <= keep {
		return
	}
	sort.Strings(ours)
	for _, name := range ours[:len(ours)-keep] {
		os.Remove(filepath.Join(dir, name))
	}
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
