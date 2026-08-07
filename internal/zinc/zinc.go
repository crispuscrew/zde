// Package zinc is zde's side of the layer 2 contract (docs/delivery.md): how
// an app is addressed, and where the thing that runs it says its state lives.
//
// The rule this package exists to keep is zinc's own: nothing outside zinc
// hardcodes the layout. `zcr where` prints it, so zde asks. Joining
// ~/.local/state/zinc/<app>/<instance> here would work today and be a second
// copy of a rule, which drifts the first time either side moves a directory -
// and the side that moves it would not be this one.
//
// Addressing is the half zde does hold, because a manifest carries the app and
// the instance in two fields and something has to put them together: `@` is the
// human form, one instance per app, and an app with no instance is its bare
// name (zinc 0.8.1).
package zinc

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// Runner is the binary that answers. Not a path: which zcr is on PATH is the
// session's business, and a zde that pinned one would be pinning it past an
// update of the flake that installed it.
const Runner = "zcr"

// Address is how a person writes one instance of an app. An empty instance is
// the app itself, which is the app that has always existed - zinc renames
// nothing that was running before instances.
func Address(app, instance string) string {
	if instance == "" {
		return app
	}
	return app + "@" + instance
}

// Location is what zinc says about an address.
type Location struct {
	// State is the directory that instance keeps its state in, honouring
	// XDG_STATE_HOME. It is answered whether or not anything is running.
	State string
	// Container is the name that instance takes at runtime - the human `@`
	// becomes a `.`, because podman refuses an `@`. This is the name to look
	// for in `podman ps`, which is why it is worth carrying.
	Container string
}

// Where asks zcr where an address keeps its state.
//
// The error when zcr is absent says so plainly: on a zde machine that is one
// missing option away (programs.zinc.enable), and "exec: zcr: not found" sends
// people looking for a package instead.
func Where(address string) (Location, error) {
	out, err := exec.Command(Runner, "where", address).Output()
	if errors.Is(err, exec.ErrNotFound) {
		return Location{}, fmt.Errorf("%s is not on PATH: where %q keeps its state is zinc's to answer, "+
			"and nothing on this machine is there to ask (programs.zinc.enable)", Runner, address)
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		// zcr's refusals are the useful half of this - an address it will not
		// parse is a manifest to fix - and they go to stderr, which Output
		// hands back rather than printing.
		if msg := strings.TrimSpace(string(exit.Stderr)); msg != "" {
			return Location{}, fmt.Errorf("%s where %s: %s", Runner, address, msg)
		}
	}
	if err != nil {
		return Location{}, fmt.Errorf("%s where %s: %w", Runner, address, err)
	}
	return parseWhere(string(out))
}

// parseWhere reads the two labelled lines zinc documents as the contract. Both
// are required: half an answer is one this cannot tell from a zcr that changed
// its mind about the format, and guessing the missing half is how a wrong path
// gets printed with confidence.
func parseWhere(out string) (Location, error) {
	var loc Location
	for _, line := range strings.Split(out, "\n") {
		label, value, found := strings.Cut(line, ":")
		if !found {
			continue
		}
		switch strings.TrimSpace(label) {
		case "state":
			loc.State = strings.TrimSpace(value)
		case "container":
			loc.Container = strings.TrimSpace(value)
		}
	}
	if loc.State == "" || loc.Container == "" {
		return Location{}, fmt.Errorf("%s where: expected a state line and a container line, got %q", Runner, strings.TrimSpace(out))
	}
	return loc, nil
}

// ErrAlreadyRunning is zinc refusing to start a second copy of an app that is
// already up. Every caller of Run wants it apart from the failures: the app the
// caller asked for is running, which is the state it was asking for.
//
// A sentinel, because the only caller that could tell one from the other is
// this one. zcr says it in prose and exits 1, exactly as it does for an image
// that would not build, so a caller holding the error has nothing to key on -
// see alreadyRunning for why the words are all there is.
var ErrAlreadyRunning = errors.New("already running")

// Run starts one app instance, detached, the way a person would from a shell.
//
// --exec because without it zcr prints the launch plan and exits, which from a
// keypress looks exactly like nothing happening.
//
// Nothing here asks first whether it is already running. zinc 0.9.1 made a
// second launch refuse before it prepares anything - the release before it,
// that second launch tore down the first - so asking would be a podman round
// trip per app per desk switch to learn what the launch is about to tell us
// anyway. What it tells us is ErrAlreadyRunning, classified here because here
// is where zcr's own words still exist: the sentence is the whole of the
// evidence, and by the time an error reaches a caller it has been through a
// %s and there is nothing left to read.
func Run(address string) error {
	out, err := exec.Command(Runner, "run", address, "--exec").CombinedOutput()
	if errors.Is(err, exec.ErrNotFound) {
		return fmt.Errorf("%s is not on PATH, so %q cannot be started (programs.zinc.enable)", Runner, address)
	}
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if alreadyRunning(msg) {
			// zcr's own sentence goes no further than here, and it is the one
			// error in this package that does not travel with it. What it adds
			// is advice for a person at a shell - stop it first, or use another
			// instance - and nobody is at a shell: this arrives on the goroutine
			// behind a desk switch, where the app being up is the good news.
			return fmt.Errorf("%s run %s: %w", Runner, address, ErrAlreadyRunning)
		}
		if msg != "" {
			return fmt.Errorf("%s run %s: %s", Runner, address, msg)
		}
		return fmt.Errorf("%s run %s: %w", Runner, address, err)
	}
	return nil
}

// alreadyRunning reads zcr's refusal to start a second copy out of what it
// printed.
//
// Text, because zcr has no code to key on: at v0.9.1 its main prints
// "zcr: " and the error and exits 1 for every one of them, and the only other
// status the binary has is `recheck`'s 2 for a tag that has moved
// (container/runner/main.go). A status that says "one of the many things that
// can go wrong went wrong" cannot separate the one that did.
//
// The match is the phrase both of zinc's runners share - "<name> is already
// running", the container runner's in app/service.go and the VM runner's in
// its own - rather than either whole sentence, because what follows the
// semicolon is advice to a person and advice gets reworded. Anywhere in the
// output rather than at the front of it, because CombinedOutput has merged
// whatever else the run printed on its way to refusing, so the refusal is not
// reliably the first line or the last.
//
// Wrong in the safe direction if zinc rewords the phrase itself: a refusal this
// stops recognising is reported as a failed launch, which is what every one of
// them was before this existed. The direction to fear is the other one - a real
// failure read as a refusal is a window that never appeared and nothing said
// about it - and it is why this matches a phrase with a subject in front of it
// rather than the word "already", which podman says about a container name in
// use and zinc says about a derived image that is already built.
func alreadyRunning(out string) bool {
	return strings.Contains(out, "is already running")
}
