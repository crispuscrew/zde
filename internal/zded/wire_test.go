package zded

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/crispuscrew/zde/internal/link"
)

// The names on the wire, in the two files that have to agree about them.
//
// Every name in this protocol is dotted - desk.switch, attn.mode, and a kind
// like ask.panel - so every pattern here has to be able to see a dot. One that
// could not was worse than no pattern at all: the scan that looks for it went
// blind while the scan that demands it did not, and the test failed on a tree
// where nothing was wrong.
var (
	// Both ways the QML writes a request: as JSON text, {"method":"queue.list"},
	// and as an object literal handed to root.send, where the key is bare and
	// the newline after the brace is where gofmt's QML cousin puts it. The
	// second form is how the picker sends desk.switch, and requiring the quotes
	// meant this could not see the one method it was most likely to miss.
	// Deliberately not a JSON parse: these are string literals and object
	// literals in a QML file, and the point is to find the names wherever they
	// are written.
	//
	// Anchored on the brace, so what is read is the first key of an object and
	// not any property in the file that happens to be called method. That is
	// the same mistake the kind pattern below made, found by looking for it
	// rather than by a merge finding it: a shell that grows an httpMethod or a
	// paymentMethod would otherwise have had this test demanding that zded
	// implement "GET". The cost is a request whose method is not written first,
	// which nothing here does and which would fail silently.
	methodInQML = regexp.MustCompile(`\{\s*"?method"?\s*:\s*"([a-zA-Z0-9.-]+)"`)
	// The third way, and the one that hid the method most worth watching: a name
	// handed to something else that will put it in the request. The shell asks
	// for the link with netLink.ask("net.status", []), and a picker row is sent
	// with root.pickMethod, set where the rows are known and read where the
	// choice is made - so the shell sends window.jump-to without ever writing it
	// inside a request. The scan above could not see either, which is why this
	// file's own test dispatched everything the bar asks for against a daemon
	// with no compositor for as long as it did: the methods that talk to niri
	// were exactly the ones it could not see.
	//
	// Anchored on the two shapes rather than on any name that looks like a
	// method, for the same reason the pattern above is anchored on a brace. A
	// scan that read every dotted lowercase literal in a QML file would have
	// this test demanding that zded implement an icon called wifi.svg. So: a
	// call whose name is one of the verbs a shell hands a request to, or a
	// property whose name ends in Method, which is what a method kept in one
	// gets called.
	//
	// The dot is required here where the request form does not require it.
	// `httpMethod: "GET"` is the false alarm the anchor above was written for
	// and it cannot survive a dot; the three undotted methods zded has - status,
	// shown and events - are each spelled inside a request today, so one of them
	// hidden behind a helper would go unseen rather than wrongly reported. That
	// is the direction this file errs in on purpose: a test failing on a tree
	// where nothing is wrong is worse than one that misses a name.
	methodHandedOnInQML = regexp.MustCompile(`(?:\w+Method\s*[:=]|\b(?:ask|send|call|command)\()\s*"([a-z][a-z0-9]*\.[a-z0-9.-]+)"`)
	// How the QML asks which event it is: msg.event.kind === "picker", and
	// msg.event.kind !== "ask.text" for a shell that reads one by ruling it
	// out. Both halves of this pattern are load-bearing and only work together.
	//
	// The anchor, because "kind" is an ordinary word: the network widget holds
	// a link status in netState.kind, and an unanchored pattern read "wifi" as
	// an event and demanded zded send it. Deriving the kinds from the Go
	// constants cannot help with that - wifi will never be an Event constant.
	//
	// The operators, because a kind read as !== is a kind the shell draws, and
	// matching only === reported it as one nothing draws. Widening the
	// operators without the anchor is strictly worse than either: it starts
	// reading netState.kind !== "absent" too. The loose forms are here because
	// they cost nothing behind the anchor, and their absence would be the same
	// false alarm one style choice later.
	kindInQML = regexp.MustCompile(`\bevent\.kind\s*(?:===|!==|==|!=)\s*"([a-zA-Z0-9.-]+)"`)
	// How zded declares one. The convention is the contract: a kind zded can
	// broadcast is a constant named Event<Something> in this package, whether it
	// stands alone or sits in a const block. One declared under another name is
	// not found here, and this file will call it undrawn.
	kindInGo = regexp.MustCompile(`\bEvent[A-Za-z0-9]*\s*=\s*"([a-zA-Z0-9.-]+)"`)
)

// The bar talks to zded directly, in QML, over the same socket `zde` uses. That
// makes this protocol something with two consumers and only one of them
// compiled: the method name in shell/shell.qml is a string in a file nothing
// else reads, so renaming a method here leaves zded answering "unknown method",
// the bar showing an empty queue for ever, and every test in the repo green.
//
// So this is the test that fails instead. It reads the method names out of the
// QML and asks the real dispatcher about each one.
func TestTheBarAsksForMethodsThatExist(t *testing.T) {
	src := readShell(t)
	asked := kindsIn(methodInQML, src)
	// And the ones it hands to a helper, which is where the methods that reach
	// for niri were hiding.
	for method := range kindsIn(methodHandedOnInQML, src) {
		asked[method] = true
	}
	if len(asked) == 0 {
		t.Fatalf("no method name found in the shell, so this test is checking nothing: "+
			"either the bar stopped speaking the protocol directly, and this test "+
			"should go, or it spells the request differently now, and %s or %s "+
			"should follow it", methodInQML, methodHandedOnInQML)
	}

	srv := wireServer(t)
	// Sorted, so that a run that finds something wrong finds it in the same
	// order twice: a map's order is not one, and the first failure is what
	// somebody reads.
	methods := make([]string, 0, len(asked))
	for method := range asked {
		methods = append(methods, method)
	}
	sort.Strings(methods)
	for _, method := range methods {
		got := srv.Dispatch(Request{Method: method})
		// A method can legitimately refuse this call - queue.list needs a
		// journal, and there is none here. What it must not do is not exist.
		if strings.HasPrefix(got.Error, "unknown method") {
			t.Errorf("the bar asks for %q and zded does not have it: %s", method, got.Error)
		}
	}
}

// wireServer is a daemon with enough behind it to answer, which is what the
// scan above dispatches against.
//
// A compositor, and not the nil this passed until now. Every method that opens
// a surface asks niri for something before it can answer - the switcher for the
// desks, the window picker for the windows, the palette for the screen being
// looked at - so a nil there did not refuse, it panicked, and the test that
// exists to prove the shell and the daemon agree about the wire was one QML
// edit away from failing for a reason that had nothing to do with the edit. It
// stayed green by luck: the shell sends window.jump-to through a helper, so the
// scan never found the one method that would have shown it.
//
// Furnished rather than empty, because a method that refuses early proves
// nothing about the path behind it: with no windows open, window.jump-to
// answers "nothing is open to jump to" and never reaches the broadcast that
// used to be the point of dispatching it.
//
// Still no journal. A method may refuse for want of one - queue.list does - and
// the question here is whether zded has the method, not whether this machine
// can serve it.
//
// A network side that says there is no NetworkManager, because the scan now
// sees net.status: the shell asks for it every five seconds through the same
// helper that hid window.jump-to, and a unit test must not open the system bus
// of the machine it happens to be running on.
func wireServer(t *testing.T) *Server {
	t.Helper()
	s := New("test", nil, &fakeCompositor{
		m:       twoDesks(),
		focused: "vshop.DP-1.code",
		output:  "DP-1",
		windows: []Window{{ID: 1, AppID: "term", Title: "a shell", Workspace: "vshop.DP-1.code"}},
	}, nil)
	withLink(s, nil, link.ErrNoManager)
	return s
}

// The same hole, one door along: an event kind is a string in the Go source and
// a string in the QML, and nothing compiles either against the other. Renaming
// EventCenter would leave zded broadcasting a kind the shell ignores, so Mod+n
// would open nothing and print nothing - and every test in the repo would stay
// green, because the daemon's half still works perfectly.
//
// The kinds are read out of the Go source rather than written down here. A list
// in a test is a list somebody has to remember to update, and this one sits
// where several branches land at once: each of them adds a surface, each adds a
// kind, and a written list would have turned this red on every one of those
// merges while the tree was fine.
func TestTheBarListensForEventsThatExist(t *testing.T) {
	found := kindInQML.FindAllStringSubmatch(string(readShell(t)), -1)
	if len(found) == 0 {
		t.Fatalf("no event kind found in the shell, so this test is checking nothing: "+
			"either the shell stopped reading events, and this test should go, "+
			"or it spells the comparison differently now and %s should follow it", kindInQML)
	}
	kinds := eventKinds(t)
	for _, m := range found {
		if !kinds[m[1]] {
			t.Errorf("the shell listens for the event kind %q and no Event constant in this package has that value "+
				"(a kind zded can send is a constant named Event<Something>, which is what this test scans for)", m[1])
		}
		delete(kinds, m[1])
	}
	for kind := range kinds {
		t.Errorf("zded sends the event kind %q and nothing in the shell draws it", kind)
	}
}

// Both scanners, handed source of their own. This is the test for the test: the
// thing that broke here was not the wiring but the reading of it, and a pattern
// that cannot see a dotted name fails in two different directions at once -
// silently in the scan that looks for one, loudly in the scan that demands it.
func TestTheWireScannersSeeADottedName(t *testing.T) {
	kinds := kindsIn(kindInGo, []byte(`
const EventPicker = "picker"

const (
	// A kind with a dot in it, which is the shape a branch adding a surface
	// with more than one of them reaches for.
	EventAskPanel = "ask.panel"
	EventNet      = "connections"
)
`))
	for _, want := range []string{"picker", "ask.panel", "connections"} {
		if !kinds[want] {
			t.Errorf("the Go scan did not find the kind %q: %v", want, kinds)
		}
	}

	drawn := kindsIn(kindInQML, []byte(`if (msg.event.kind === "ask.panel") root.openAsk(msg.event);`))
	if !drawn["ask.panel"] {
		t.Errorf("the QML scan did not find a dotted kind: %v", drawn)
	}

	// And the methods, which have been dotted since the first one: every verb
	// in this protocol is group.verb.
	methods := kindsIn(methodInQML, []byte(`stream.write('{"method":"attn.mode"}\n');`))
	if !methods["attn.mode"] {
		t.Errorf("the method scan did not find a dotted method: %v", methods)
	}
}

// eventKinds is every kind this package can broadcast, read out of its own
// source. Every .go file rather than events.go alone: a branch that adds a
// surface declares its kind next to the code that sends it, and a scan that
// looked in one file would call that kind undeclared.
func eventKinds(t *testing.T) map[string]bool {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read the package directory: %v", err)
	}
	kinds := map[string]bool{}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		for kind := range kindsIn(kindInGo, src) {
			kinds[kind] = true
		}
	}
	if len(kinds) == 0 {
		t.Fatal("no Event constant found in this package, so this test is checking nothing: " +
			"either the events went away, or they are declared under a name this scan does not know")
	}
	return kinds
}

// kindsIn is the first capture of every match, as a set.
func kindsIn(re *regexp.Regexp, src []byte) map[string]bool {
	out := map[string]bool{}
	for _, m := range re.FindAllSubmatch(src, -1) {
		out[string(m[1])] = true
	}
	return out
}

// readShell is the shell's own file, and only that one. Every other .qml in
// there is a surface: it is handed rows and it emits what was chosen, and this
// file is the one that holds the sockets and turns a choice into a request. So
// this is where the names on the wire are, and a scan of the whole directory
// would be reading files that send nothing - and dispatching what it found in
// them, which for the bluetooth surface means dialling the radio of whatever
// machine is running the tests.
func readShell(t *testing.T) []byte {
	t.Helper()
	path := filepath.Join("..", "..", "shell", "shell.qml")
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return src
}

// "kind" is an ordinary word, and only one of them is an event kind. The
// network widget holds a link status in netState.kind, so a pattern that reads
// any .kind comparison demands that zded start sending "wifi" - a test failing
// on a tree where nothing is wrong, which is the failure this whole file exists
// to avoid causing.
func TestTheKindScannerReadsOnlyEventKinds(t *testing.T) {
	drawn := kindsIn(kindInQML, []byte(`
        if (msg.event.kind === "picker")
            root.openPicker(msg.event);
        text: netState.kind === "wifi" ? "wifi" : "wired"
        visible: root.linkKind !== "absent"
`))
	if !drawn["picker"] {
		t.Errorf("the event kind was not found: %v", drawn)
	}
	for _, notAnEvent := range []string{"wifi", "wired", "absent"} {
		if drawn[notAnEvent] {
			t.Errorf("%q is a property called kind and this read it as an event zded must send: %v", notAnEvent, drawn)
		}
	}
}

// A kind the shell reads by ruling it out is a kind the shell reads. The ask
// panel does exactly that, and matching only === reported a kind it draws as
// one nothing draws - the same false alarm from the other direction.
func TestTheKindScannerSeesBothWaysOfAsking(t *testing.T) {
	for _, line := range []string{
		`if (msg.event.kind === "ask.text") root.openAsk(msg.event);`,
		`if (msg.event.kind !== "ask.text") return;`,
		`if (msg.event.kind == "ask.text") root.openAsk(msg.event);`,
		`if (msg.event.kind != "ask.text") return;`,
	} {
		if drawn := kindsIn(kindInQML, []byte(line)); !drawn["ask.text"] {
			t.Errorf("a kind the shell reads was invisible: %s", line)
		}
	}
}

// The scan reads a method the shell hands to a helper, in each of the shapes it
// hands one over in.
//
// The three named here are the three that made this worth writing. Every one of
// them asks the compositor something before it can answer, so a scan that could
// not see one was a scan that never dispatched it - and never dispatching them
// was what kept a daemon with no compositor looking like a daemon that worked.
// window.jump-to is the one the picker sets and the choice sends, which is the
// hole this closes. desk.switcher and palette.list reach zded from a key
// through `zde` today and are not in the shell at all: they are here because
// the shape a surface would send them in is the shape that used to be
// invisible, and this is where that gets settled rather than in the merge that
// adds one.
//
// The negatives are the false alarms this pattern would cause if it were any
// looser: a property called httpMethod is not a request, and a file named after
// a surface is not a method. Both are things a shell can grow, and this test
// fails here rather than on the tree that grows one.
func TestTheMethodScannerSeesAMethodHandedToAHelper(t *testing.T) {
	found := kindsIn(methodHandedOnInQML, []byte(`
        property string pickMethod: "desk.switcher"
        root.pickMethod = "window.jump-to";
        netLink.ask("net.status", []);
        surface.send("palette.list", []);
        root.command("bluetooth.pair", [d.address]);
        readonly property string httpMethod: "GET"
        Qt.createComponent("picker.qml");
`))
	for _, want := range []string{"desk.switcher", "window.jump-to", "net.status", "palette.list", "bluetooth.pair"} {
		if !found[want] {
			t.Errorf("the shell sends %q and the scan did not see it: %v", want, found)
		}
	}
	for _, notAMethod := range []string{"GET", "picker.qml"} {
		if found[notAMethod] {
			t.Errorf("%q is not a method and this read it as one: %v", notAMethod, found)
		}
	}
}

// The same audit on the other pattern, before a merge does it. A request is an
// object whose first key is the method; a property somewhere else in the file
// that is also called method is not one, and reading it would leave this test
// asking the dispatcher for whatever string it found.
func TestTheMethodScannerReadsOnlyRequests(t *testing.T) {
	found := kindsIn(methodInQML, []byte(`
        stream.write('{"method":"queue.list"}\n');
        root.send({
            method: "attn.invoke",
            args: [which, key]
        });
        readonly property string method: "GET"
        function fetchIt() {
            return http.send({
                url: "/v1/ask",
                method: "POST"
            });
        }
`))
	for _, want := range []string{"queue.list", "attn.invoke"} {
		if !found[want] {
			t.Errorf("the request for %q was not found: %v", want, found)
		}
	}
	// A property that is called method, and a method key that is not the first
	// of its object: neither is a request to zded, and reading either would
	// leave this test asking the dispatcher to implement "POST".
	for _, notARequest := range []string{"GET", "POST"} {
		if found[notARequest] {
			t.Errorf("%q is not a request and this read it as one: %v", notARequest, found)
		}
	}
}
