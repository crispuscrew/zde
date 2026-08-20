package zded

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/crispuscrew/zde/internal/desk"
	"github.com/crispuscrew/zde/internal/manifest"
	"github.com/crispuscrew/zde/internal/plainfile"
)

// dynamic.kdl is the seam a running zded writes through (niri/config.kdl): the
// generated config is a read-only store path, so anything that changes while
// the session runs lives here, and niri reloads the lot when it is written.
//
// Three things go in it. Zen's half of the chrome is one - nothing in niri
// 26.04's IPC touches the layout section, so a file it already includes is the
// only way to change gaps or borders without a restart (zen.go). The capture
// blocks are the second, for the same reason: block-out-from is a window rule
// and niri's socket has no action for it (capture.go).
//
// The third is placement. A desk manifest pins an app to a
// workspace, and until now nothing read that: a desk brought up three apps and
// all three landed on whichever workspace was in front of you. niri places a
// window at map time from a window rule, which is the one moment a move cannot
// reach - a window moved after it appears has already been drawn somewhere
// else, and on a desktop whose rule is that nothing rearranges under you, that
// is the wrong kind of correct.
//
// Written at startup, on reconcile, and on a zen or capture-block toggle - not
// on a switch. niri reloads its whole config when this file is written, and a
// reload re-evaluates the rules for every window already open. Doing that on a
// keypress somebody presses all day is a lot of asking for something that only
// changes when a file is edited; the two toggles are on the list because a
// reload is the only way either happens at all, and both are pressed when
// somebody wants the screen to change now.
const rulesHeader = "// Written by zded. Do not edit: it is rewritten from the desk manifests\n" +
	"// (~/.config/zde/desks), from whether zen is on (`zde desk zen`), and from\n" +
	"// what is blocked out of capture (`zde window capture-block`).\n"

// dynamicPath is where niri's config includes it from.
func dynamicPath() string {
	if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
		return filepath.Join(dir, "niri", "dynamic.kdl")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config", "niri", "dynamic.kdl")
}

// dynamicKDL is the whole of that file: the header, what zen is doing to niri's
// chrome (zen.go), what is blocked out of capture (capture.go), and where the
// manifests say each pinned app opens. One function because there is one file,
// and a writer that knew about only its own part would drop the others on every
// write.
func dynamicKDL(zen bool, blocked []string, desks map[string]*manifest.Desk) string {
	return rulesHeader + zenLayout(zen) + captureRules(blocked) + placementRules(desks)
}

// placementRules renders every pinned app on every desk as a niri window rule,
// in a stable order so that writing it twice from the same manifests produces
// the same bytes and niri is not asked to reload for nothing.
//
// Every desk at once rather than the one being entered: the file is the whole
// of niri's dynamic config, so writing one desk's rules would drop the rest.
func placementRules(desks map[string]*manifest.Desk) string {
	var b strings.Builder
	for _, name := range sortedNames(desks) {
		for _, app := range desks[name].Apps {
			if app.AppID == "" || app.Monitor == "" || app.Workspace == "" {
				// Nothing to match on, or nowhere to put it. Adoption places
				// it, which is what an unpinned app has always got.
				continue
			}
			n, err := desk.NewName(name, app.Monitor, app.Workspace)
			if err != nil {
				continue // check() proved these parse; a new one cannot appear here
			}
			b.WriteString("\nwindow-rule {\n")
			// Two escapings, inner first, and they are not the same escaping.
			//
			// niri reads this value as a regex, so the literal app id from the
			// manifest is quoted for that: without it the desk's rule matches
			// more windows than it asked for, and `.` alone matches every app
			// there is. That is what QuoteMeta does, and it is why QuoteMeta
			// alone reads as obviously right.
			//
			// It is not, because niri never sees those bytes directly. They
			// arrive as a KDL quoted string, and KDL's escape set is nothing
			// like a regex's - so the `\.` QuoteMeta produces for the dot in
			// org.mozilla.firefox is an invalid KDL escape, niri refuses the
			// whole config over it, and dynamic.kdl is included by the config
			// that carries the binds. One pinned app with a dot in its id and
			// the machine has no keyboard (kdlString, and the test that hands
			// this to a real niri).
			//
			// So: quote for the regex, then quote that for KDL. The monitor
			// and the workspace go through the same door although NewName
			// above has already proved they are letters, digits and dashes -
			// one function owns "a value becoming a KDL string" here, so a
			// field added to this rule later is not a fresh thing to reason
			// about.
			b.WriteString("    match app-id=" + kdlString("^"+regexp.QuoteMeta(app.AppID)+"$") + "\n")
			b.WriteString("    open-on-output " + kdlString(app.Monitor) + "\n")
			b.WriteString("    open-on-workspace " + kdlString(n.String()) + "\n")
			b.WriteString("}\n")
		}
	}
	return b.String()
}

// kdlString renders s as a KDL quoted string, quotes included, for niri's
// parser (KDL 1.0, which is what niri 26.04 reads).
//
// What KDL actually requires is small, and was read off niri's own parser
// rather than guessed: inside a quoted string only `"` and `\` may not stand
// for themselves, and the escapes it accepts after a backslash are exactly
// `"`, `/`, `\`, `b`, `f`, `n`, `r`, `t` and `u{...}`. Anything else after a
// backslash - `\.`, `\s`, `\-` - is "invalid escape char" and takes the file
// down. Note what is *not* on that list: the four-hex-digit `u` form without
// braces is refused, and the `\x41`, `\a` and `\v` that Go's strconv.Quote
// would happily emit are not KDL escapes at all. So this is written out rather
// than borrowed from strconv.
//
// Raw tabs, raw newlines and raw control bytes are all accepted by KDL inside
// a quoted string, so escaping them is this function going past what it must.
// It does it anyway: dynamic.kdl is a file somebody reads when they are asking
// why a window went where it went, and one rule per line is the difference
// between reading it and not.
//
// Ranged by rune, so a byte that is not valid UTF-8 becomes U+FFFD rather than
// being copied through. KDL is UTF-8, and a file niri cannot decode is a file
// niri refuses - the same failure this whole function exists to prevent.
func kdlString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '\\':
			b.WriteString(`\\`)
		case '"':
			b.WriteString(`\"`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		default:
			// The rest of C0 and DEL, which have no letter of their own. Braced
			// hex is the only form KDL 1.0 takes.
			if r < 0x20 || r == 0x7f {
				fmt.Fprintf(&b, `\u{%x}`, r)
				continue
			}
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// writeRules puts them where niri reads them, and only when they changed.
//
// niri reloads its config on every write to an included file, and a reload is
// visible: it re-evaluates the rules for existing windows. Rewriting identical
// bytes on every desk switch would make that a thing that happens all day.
func writeRules(path, content string) error {
	if path == "" {
		return nil
	}
	// Bounded, and through internal/plainfile: this read runs at startup with
	// the socket already bound and nothing yet answering it, so a FIFO at
	// dynamic.kdl - which is a path in the person's own config directory, seeded
	// by an activation script that leaves it alone afterwards - used to stop the
	// daemon between binding and serving, which is the worst moment there is.
	// Anything that is not the file this wrote falls through to the rewrite
	// below, which replaces it, and that is the right answer for a path whose
	// header says zde owns every byte of it.
	//
	// The ceiling is generous rather than tight: this file is a window rule per
	// pinned app, a few hundred bytes each, so a megabyte is thousands of them.
	if old, err := plainfile.Read(path, 1<<20); err == nil && string(old) == content {
		// The bytes are right and the mode may not be. A machine that has been
		// running zde since before this was written has a 0644 dynamic.kdl
		// holding exactly what would be written now, so this early return is the
		// only path it ever takes and a tightening below it never arrives.
		//
		// Zde's own file, so its mode is zde's to set: the header says not to
		// edit it and every byte in it is rewritten from the manifests. That is
		// the difference between this and a desk manifest, which is somebody's
		// own file and keeps whatever mode they gave it (internal/manifest,
		// Save).
		//
		// On the path and not on a descriptor, which means it chmods through a
		// symlink - the read above deliberately followed one to get here. Left
		// that way, with the reasoning written down rather than the fix, because
		// the reach of it is nothing. os.Chmod refuses a file this account does
		// not own, so the far end can only ever be one of ours; the mode only
		// ever narrows, 0644 to 0600; and getting there at all means arranging a
		// file whose bytes are already byte-for-byte the rules zde would write,
		// which is a thing only somebody who can write this path could do, and
		// they can write this path. Every other route through this function
		// replaces the link by rename rather than following it, so this is the
		// one case and it is a tightening of your own file.
		return os.Chmod(path, 0o600)
	}
	// The directory as well, because nothing else makes it. niri's config
	// directory exists on a machine somebody has configured niri on by hand, and
	// zded is started on machines where nobody has: layer 1 generates niri's
	// config into the store and includes this path from it, so the first startup
	// on a fresh account found no ~/.config/niri to write into, said so once on
	// stderr, and every pinned app then opened wherever niri felt like putting
	// it. 0755 is what a directory gets from a plain mkdir, before the umask
	// takes its share, and it is used only when zde is the one creating the
	// directory: MkdirAll leaves one that is already there exactly as it found
	// it, and nothing here narrows it afterwards. That restraint is the point.
	// This is niri's config directory and not zde's - what else the person
	// keeps under it is theirs - so the privacy of the rules is carried by the
	// file's own 0600 below, which travels with the file into places a
	// directory's mode does not reach.
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	// Renamed over rather than truncated in place: a config half-written when
	// niri reads it is a config niri refuses, and it refuses the whole file -
	// the binds go with it.
	tmp, err := os.CreateTemp(filepath.Dir(path), ".dynamic.kdl.*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(content); err != nil {
		tmp.Close()
		return err
	}
	// On the disk before the rename, not merely in the page cache. Without it
	// the rename can land while the content has not, and what niri reads after
	// a power cut is a file of the right name and zero length - which it
	// refuses, and it refuses the whole config with it. The same fsync
	// internal/manifest's writeNew and internal/journal's compaction do, so the
	// three places zde renames a file into place now agree.
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	// 0600, which os.CreateTemp already gives it: only niri reads this, and niri
	// is the person's own compositor. What is in it is a line per pinned app
	// naming the desk it opens on, and one of those desks can be a private one -
	// which zde keeps out of the picker, so it should not be publishing the name
	// in a file next door either (docs/vision.md, section 3).
	if err := os.Chmod(tmp.Name(), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func sortedNames(desks map[string]*manifest.Desk) []string {
	out := make([]string, 0, len(desks))
	for name := range desks {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}
