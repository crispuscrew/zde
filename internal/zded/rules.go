package zded

import (
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
// What goes in it today is placement. A desk manifest pins an app to a
// workspace, and until now nothing read that: a desk brought up three apps and
// all three landed on whichever workspace was in front of you. niri places a
// window at map time from a window rule, which is the one moment a move cannot
// reach - a window moved after it appears has already been drawn somewhere
// else, and on a desktop whose rule is that nothing rearranges under you, that
// is the wrong kind of correct.
//
// Written at startup and on reconcile rather than on a switch: niri reloads its
// whole config when this file is written, and a reload re-evaluates the rules
// for every window already open. Doing that on a keypress somebody presses all
// day is a lot of asking for something that only changes when a file is
// edited.
const rulesHeader = "// Written by zded. Do not edit: the source is the desk manifests\n" +
	"// (~/.config/zde/desks), and this file is rewritten from them.\n"

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

// placementRules renders every pinned app on every desk as a niri window rule,
// in a stable order so that writing it twice from the same manifests produces
// the same bytes and niri is not asked to reload for nothing.
//
// Every desk at once rather than the one being entered: the file is the whole
// of niri's dynamic config, so writing one desk's rules would drop the rest.
func placementRules(desks map[string]*manifest.Desk) string {
	var b strings.Builder
	b.WriteString(rulesHeader)
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
			// Anchored and quoted: the manifest gives a literal app id and niri
			// reads a regex, so an unescaped one would match more windows than
			// the desk asked for - `.` alone matches every app there is.
			b.WriteString("    match app-id=\"^" + regexp.QuoteMeta(app.AppID) + "$\"\n")
			b.WriteString("    open-on-output \"" + app.Monitor + "\"\n")
			b.WriteString("    open-on-workspace \"" + n.String() + "\"\n")
			b.WriteString("}\n")
		}
	}
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
