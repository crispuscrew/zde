package zded

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/crispuscrew/zde/internal/desk"
	"github.com/crispuscrew/zde/internal/manifest"
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
	if old, err := os.ReadFile(path); err == nil && string(old) == content {
		return nil
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
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
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
