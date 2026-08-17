package zded

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// Why a Text without a textFormat is refused.
//
// Every string an application chooses ends up in a Qt Text element in shell/,
// and that element is what decides whether the string is displayed or
// interpreted. Qt Quick's default is textFormat: Text.AutoText, and AutoText is
// not "plain unless somebody asks for more". QQuickText::setText runs the
// string through Qt::mightBeRichText(), and when that says yes the whole string
// goes to the StyledText parser instead of to a plain layout - qtdeclarative
// 6.11.1, qquicktext.cpp:
//
//	d->styledText = d->format == StyledText || (d->format == AutoText && Qt::mightBeRichText(n));
//
// The heuristic (qtbase, qtextdocument.cpp, mightBeRichTextImpl) scans as far as
// the first newline for the first '<', reads the word after it, and returns true
// for any name in Qt's HTML element table. So the markup does not have to be at
// the front of the string: "build failed <img src=...>" is enough, and img is in
// the table.
//
// StyledText draws <img src="...">, and the URL is not held to a local file. The
// parser preloads only local ones (qquickstyledtext.cpp, parseImageAttributes,
// guarded by url.isLocalFile()), but that is an optimisation and not the load.
// The load happens at layout time in QQuickTextPrivate::setLineGeometry, which
// builds a QQuickPixmap for whatever URL it was handed, unguarded; an http URL
// reaches QQuickPixmapReader::processJob and QNetworkAccessManager::get. Qt's own
// documentation for the property says the same thing in one sentence: "when
// displaying user-controlled, untrusted content, the textFormat should either be
// explicitly set to Text.PlainText, or the contents should be stripped of
// unwanted tags."
//
// Measured, not inferred, against the Qt this flake pins (6.11.1): a Text with
// no textFormat, text "Build failed <img src=\"http://127.0.0.1:8123/exfil.png\">",
// run under QT_QPA_PLATFORM=offscreen against a listener on loopback. The GET
// arrived. Nothing was ever on a screen. The same string in a Text with
// textFormat: Text.PlainText produced no connection.
//
// That is worth more here than a formatting bug. This is the layer-shell
// process: it holds an exclusive keyboard grab on every surface that takes one,
// and it is the one thing talking to zded. A notification body, a window title,
// an ssid, a clipboard preview or a model's answer that can make it open a
// socket to a host of the sender's choosing is a read receipt, a presence beacon
// and a side channel out of a process with no business on the network at all.
// And nothing upstream of the surface strips these strings: internal/attn's
// clean keeps every printable rune, so '<', '>' and '&' arrive intact. That is
// the right thing to do to a message - a notification that said "5 < 10" should
// say it - which is precisely why the decision belongs here, at the point of
// drawing.
//
// Hence: every Text in shell/ declares textFormat: Text.PlainText. Every one,
// including the ones that only ever draw a literal out of this repo's own
// source. Uniform rather than judged element by element, because "this one is
// only ever a literal" is a claim about today's binding that tomorrow's edit
// breaks in silence, and because a rule with exceptions cannot be checked by
// reading the file.
//
// The scan is three rules over every .qml under shell/, and they are three
// because each of the other two is a way past the first:
//
//  1. Every declaration of a text-drawing element declares
//     textFormat: Text.PlainText.
//  2. Every import is one this file has an opinion about, which means one whose
//     text-drawing types rule 1 knows to look for (see qmlModules).
//  3. Every mention of textFormat anywhere, bound with ':' or assigned with
//     '=', names PlainText.
//
// Rule 2 is the one that was doing nothing and looked like something. Until it
// was written, what kept a QtQuick.Controls Label out of this tree was that
// nobody had imported QtQuick.Controls - not the scan, which had never heard of
// Label and would not have looked at one. A Label's textFormat defaults to
// AutoText exactly as a Text's does, so the day somebody imports the module for
// a button, the guard says everything is fine about a file it cannot read.
//
// What this deliberately does not scan, and why:
//
//   - TextInput and TextField are not scanned, and could not be fixed if they
//     were. QQuickTextInput has no textFormat property at all and never
//     interprets markup, so the password field in Links.qml and the filter
//     fields in the palette and the clip history are safe by construction.
//   - TextEdit is not required to declare one, because its default is already
//     PlainText rather than AutoText. It is still covered by rule 3, which
//     refuses any textFormat that is not PlainText whatever element it is on.
//   - The scan is line-based and knows nothing about block comments. These files
//     use // exclusively, so a commented-out Text would be a false failure and
//     not a false pass, which is the direction to be wrong in.
//
// And a rule about the rules: a shape this cannot parse fails rather than being
// skipped. A scan that quietly cannot read a form is a scan that passes on the
// thing it exists to catch, which is how all four of the blind spots this
// version closes came to be there.
const whyPlainText = "Every string on these surfaces was chosen by something other than the person " +
	"at the machine, and Text.AutoText hands anything that looks like markup to Qt's StyledText " +
	"parser, which fetches <img src=\"http://...\"> over the network from inside the process " +
	"holding the keyboard. Write textFormat: Text.PlainText, and read the comment above " +
	"whyPlainText in this file before deciding this element is the exception."

// qmlModules is every import shell/ may use, and the text-drawing types each one
// brings that rule 1 then looks for.
//
// An allowlist and not a denylist, because the failure being closed is a module
// whose types this file has never heard of. A denylist of the ones known to
// default to rich text - QtQuick.Controls and its styles, QtQuick.Templates -
// would be the same confusable-table mistake made about module names: it is only
// ever as current as the last person to update it, and what it lets through is
// silence.
//
// So the price of a new import is one line here saying which of its types draw
// text that Qt might interpret. Nil is a claim, not a blank: it says this module
// brings no such type. QtQuick brings the one this tree uses everywhere.
//
// QtQuick.Controls is the module to think about before adding it. Label, Button,
// ItemDelegate, CheckBox, MenuItem, ToolTip and the rest all put their `text`
// through a Label, whose textFormat defaults to AutoText, so every one of them
// is a Text with no format by another name. Adding it here means naming all of
// them, and the honest answer is usually that a layer-shell surface drawing
// somebody else's strings does not need Controls at all.
var qmlModules = map[string][]string{
	"QtQuick":                      {"Text"},
	"Quickshell":                   nil,
	"Quickshell.Io":                nil,
	"Quickshell.Wayland":           nil,
	"Quickshell.Services.Pipewire": nil,
	"Quickshell.Services.UPower":   nil,
}

// qmlAlwaysText is looked for in every file whatever it imports.
//
// Two locks on one door, and the second one costs a word. Rule 2 refuses the
// import that brings a Label, so a Label cannot be created in this tree at all;
// but rule 2 reads import lines, and the shape of an import line is a thing this
// file has already been wrong about once. A Label is a Text by inheritance -
// QQuickLabel derives from QQuickText and its textFormat defaults to AutoText
// exactly as a Text's does - so if one ever does appear, the rule that would
// have caught a Text catches it too, without waiting for anybody to notice which
// module it came from.
var qmlAlwaysText = []string{"Label", "Text"}

// An import line, as this tree writes them: the module, and whatever follows it
// (a version, an `as` alias). The alias is deliberately not read - the module is
// what decides which types the file can name, however the file spells them.
var qmlImport = regexp.MustCompile(`^[ \t]*import[ \t]+([A-Za-z0-9_.]+)[ \t]*(.*)$`)

// A declaration whose opening brace is on the same line, which is how every
// element in this tree is written.
//
// Three things it allows in front of the type, each of them a way an element is
// written that used to be invisible:
//
//   - A property, as in `delegate: Text {`. The old scan was anchored at the
//     type, so an element assigned to a property was not an element as far as it
//     was concerned - and `delegate: Rectangle {` is already how three of these
//     surfaces write their rows (shell/NotifCenter.qml, shell/ClipHistory.qml,
//     shell/ActionPalette.qml), so this was one word away from being a hole
//     rather than a shape nobody uses.
//   - A namespace, as in `QQC.Label {`, which is how an aliased import is
//     spelled.
//   - `property Component x: Text {` and the like, which is the property form
//     with a type in front of the name.
//
// The indentation captured is the line's, which is where the element's closing
// brace has to be whichever of these it is.
func qmlDeclaration(typeName string) *regexp.Regexp {
	return regexp.MustCompile(`^([ \t]*)` + qmlBeforeType + typeName + `[ \t]*\{(.*)$`)
}

// The same declaration with its brace on the next line, which is legal QML and
// which the scan cannot follow. Matched only so that it can be refused by name:
// left unmatched it is an element that silently declares nothing.
func qmlLooseDeclaration(typeName string) *regexp.Regexp {
	return regexp.MustCompile(`^[ \t]*` + qmlBeforeType + typeName + `[ \t]*$`)
}

// What may stand between the indentation and the type name: an optional
// `property Type name:` or `name:`, then an optional namespace.
const qmlBeforeType = `(?:(?:property[ \t]+[A-Za-z0-9_.]+[ \t]+)?[A-Za-z0-9_]+[ \t]*:[ \t]*)?(?:[A-Za-z0-9_]+\.)*`

// Any textFormat, bound or assigned, and everything after the operator. The
// value is taken to the end of the line rather than as the first identifier on
// it, so that a value this cannot read - a ternary, a function call, a
// comparison - is refused instead of being read as its first word.
var anyTextFormat = regexp.MustCompile(`textFormat[ \t]*(:|=+)[ \t]*(.*)$`)

// A trailing line comment, to be cut off a value before it is read.
var trailingComment = regexp.MustCompile(`[ \t]*//.*$`)

// qmlScan is what one file's source came to: what was looked at, and what was
// found wrong. The counts are how the scan proves it is still looking at
// something - a regex that stops matching is a test that passes for ever.
type qmlScan struct {
	declarations int
	formats      int
	problems     []string
}

func (s *qmlScan) fail(where, why string) { s.problems = append(s.problems, where+": "+why) }

// checkQML runs the three rules over one file.
//
// A function over a name and a string rather than over a path, so that the guard
// can be shown to catch each of the shapes it is for without a broken .qml being
// committed to shell/ to prove it (see TestTheShellsPlainTextGuardCatchesWhatItIsFor).
func checkQML(where, src string) qmlScan {
	var s qmlScan
	lines := strings.Split(src, "\n")

	// Rule 2 first, because it decides what rule 1 is looking for.
	types := map[string]bool{}
	for i, line := range lines {
		if isQMLComment(line) || !strings.HasPrefix(strings.TrimSpace(line), "import") {
			continue
		}
		at := where + ":" + strconv.Itoa(i+1)
		m := qmlImport.FindStringSubmatch(line)
		if m == nil {
			s.fail(at, "this test cannot read that import, so it cannot say which types the file "+
				"may name. Write it as `import Module` and teach qmlModules what the module brings.")
			continue
		}
		brought, known := qmlModules[m[1]]
		if !known {
			s.fail(at, "nothing in this test knows what "+m[1]+" brings. A module whose text-drawing "+
				"types are not listed in qmlModules is a module whose Labels, Buttons and delegates "+
				"this scan would walk straight past, and Qt's default for every one of them is "+
				"Text.AutoText. Add it to qmlModules with the types it brings, or do without it. "+
				whyPlainText)
			continue
		}
		for _, t := range brought {
			types[t] = true
		}
	}

	// Rule 1, over the types every file is checked for and whatever else its
	// imports bring.
	for _, t := range qmlAlwaysText {
		types[t] = true
	}
	for _, typeName := range sortedKeys(types) {
		decl, loose := qmlDeclaration(typeName), qmlLooseDeclaration(typeName)
		for i, line := range lines {
			if isQMLComment(line) {
				continue
			}
			at := where + ":" + strconv.Itoa(i+1)
			if loose.MatchString(line) {
				s.declarations++
				s.fail(at, "this "+typeName+" is declared with its opening brace on another line, and "+
					"this scan reads one element per line. Put the brace here, or teach this test the "+
					"new shape - an element it cannot read is an element it cannot check.")
				continue
			}
			m := decl.FindStringSubmatch(line)
			if m == nil {
				continue
			}
			s.declarations++
			indent, rest := m[1], m[2]
			if strings.TrimSpace(rest) != "" {
				s.fail(at, "this "+typeName+" is declared with "+strconv.Quote(strings.TrimSpace(rest))+
					" after the brace, and this scan reads one element per line. Either put the "+
					"properties on their own lines, or teach this test the new shape.")
				continue
			}
			end, why := elementBody(lines, i, indent)
			if why != "" {
				s.fail(at, "this "+typeName+" "+why)
				continue
			}
			own := ownProperties(lines, i, end)
			if len(own) == 0 {
				s.fail(at, "this "+typeName+" declares nothing at all, so it declares no textFormat. "+whyPlainText)
				continue
			}
			name := at
			format := ""
			for _, j := range own {
				if id, ok := strings.CutPrefix(strings.TrimSpace(lines[j]), "id: "); ok {
					name = at + " (" + strings.TrimSpace(id) + ")"
				}
				if f := anyTextFormat.FindStringSubmatch(lines[j]); f != nil {
					format = qmlValue(f[2])
				}
			}
			switch {
			case format == "Text.PlainText":
			case format == "":
				s.fail(name, "no textFormat, so Qt draws it as Text.AutoText. "+whyPlainText)
			default:
				s.fail(name, "textFormat is "+format+", which interprets what it is given. "+whyPlainText)
			}
		}
	}

	// Rule 3, over every line, whatever element it belongs to and whether it is a
	// binding or an assignment made at run time.
	for i, line := range lines {
		// Comments talk about the formats by name - this file's own rule is
		// written into every header in shell/ - and a scan that read those would
		// fail on the sentence explaining why it exists.
		if isQMLComment(line) {
			continue
		}
		m := anyTextFormat.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		s.formats++
		at := where + ":" + strconv.Itoa(i+1)
		if m[1] != ":" && m[1] != "=" {
			s.fail(at, "this test cannot read `textFormat "+m[1]+"`, and a shape it cannot read is a "+
				"format it cannot check. "+whyPlainText)
			continue
		}
		value := qmlValue(m[2])
		// The type in front of the dot is not checked, only the value after it.
		// QML scopes an enum to its type, so a TextEdit spells the same constant
		// TextEdit.PlainText, and a test that demanded "Text.PlainText"
		// everywhere would refuse the correct spelling on every element but a
		// Text.
		//
		// The second half is what keeps that from being fooled by its own
		// suffix: `rich ? Text.RichText : Text.PlainText` ends in the right
		// letters and is a rich format half the time. Anything that is not one
		// bare identifier is refused rather than parsed, which is this file's
		// rule about shapes it cannot read applied to the value as well.
		if !strings.HasSuffix(value, ".PlainText") || strings.ContainsAny(value, " \t?(") {
			s.fail(at, "textFormat is "+strconv.Quote(value)+". "+whyPlainText)
		}
	}
	return s
}

// qmlValue is the right-hand side of a binding or an assignment, without the
// trailing comment, the semicolon or the space around it. Anything left that is
// not a bare identifier is left as it is, so that rule 3 refuses it.
func qmlValue(rest string) string {
	rest = trailingComment.ReplaceAllString(rest, "")
	return strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(rest), ";"))
}

func isQMLComment(line string) bool { return strings.HasPrefix(strings.TrimSpace(line), "//") }

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// The guard itself, over the surfaces as they are.
func TestEveryTextInTheShellIsDrawnAsPlainText(t *testing.T) {
	files := qmlSurfaces(t)
	declarations, formats := 0, 0
	for _, file := range files {
		got := checkQML(shortQMLName(file), readQMLPath(t, file))
		declarations += got.declarations
		formats += got.formats
		for _, p := range got.problems {
			t.Error(p)
		}
	}
	// Three ways this test can go blind, and each of them is a way it would keep
	// passing while checking nothing: no files, no elements, no formats.
	if declarations == 0 {
		t.Fatal("this scan found no text-drawing element in the whole of shell/, so it is checking " +
			"nothing: either every surface went away, or elements are written in a shape this scan " +
			"no longer matches")
	}
	if formats == 0 {
		t.Fatal("no textFormat anywhere in shell/, which cannot be true while the check above " +
			"passes: this scan has gone blind")
	}
}

// The guard against itself.
//
// Every rule above exists because of a shape that used to pass, and a rule that
// has stopped catching its shape looks exactly like a tree with nothing wrong in
// it. So each one is shown catching what it is for, on source held here rather
// than on a broken surface committed to shell/ - which would be a real hole in
// the shell for as long as the file existed.
func TestTheShellsPlainTextGuardCatchesWhatItIsFor(t *testing.T) {
	// The shape every rule is measured against: what shell/ actually writes.
	const good = "import QtQuick\n\nItem {\n    Text {\n        text: \"hello\"\n        textFormat: Text.PlainText\n    }\n}\n"
	if got := checkQML("good.qml", good); len(got.problems) > 0 || got.declarations != 1 || got.formats != 1 {
		t.Fatalf("the scan does not pass the shape this tree is written in, so nothing below means "+
			"anything: %v (declarations %d, formats %d)", got.problems, got.declarations, got.formats)
	}

	for _, c := range []struct {
		what string
		src  string
		want string
	}{{
		what: "a Text with no textFormat at all, which is the whole point",
		src:  "import QtQuick\nItem {\n    Text {\n        text: body\n    }\n}\n",
		want: "no textFormat",
	}, {
		what: "a Text whose opening brace is on the next line, which the scan cannot follow",
		src:  "import QtQuick\nItem {\n    Text\n    {\n        text: body\n    }\n}\n",
		want: "opening brace on another line",
	}, {
		what: "a Text with a property after the brace, which the scan cannot follow either",
		src:  "import QtQuick\nItem {\n    Text { text: body }\n}\n",
		want: "after the brace",
	}, {
		what: "a Text assigned to a property, which is how three of these surfaces write their rows",
		src:  "import QtQuick\nItem {\n    ListView {\n        delegate: Text {\n            text: body\n        }\n    }\n}\n",
		want: "no textFormat",
	}, {
		what: "a Label, which is a Text by inheritance and defaults to AutoText like one",
		src:  "import QtQuick\nItem {\n    Label {\n        text: body\n    }\n}\n",
		want: "no textFormat",
	}, {
		what: "a comment inside an element that spells the property out, which is not a property",
		src:  "import QtQuick\nItem {\n    Text {\n        // textFormat: Text.PlainText is what this wants\n        text: body\n    }\n}\n",
		want: "no textFormat",
	}, {
		what: "an import that brings types this scan has never heard of, Label among them",
		src:  "import QtQuick\nimport QtQuick.Controls\nItem {\n    Label {\n        text: body\n    }\n}\n",
		want: "nothing in this test knows what QtQuick.Controls brings",
	}, {
		what: "an import written in a shape this scan cannot read",
		src:  "import QtQuick\nimport \"helpers.js\" as Helpers\nItem {\n    Text {\n        textFormat: Text.PlainText\n    }\n}\n",
		want: "cannot read that import",
	}, {
		what: "a rich format assigned at run time, which no declaration scan would see",
		src:  "import QtQuick\nItem {\n    Component.onCompleted: label.textFormat = Text.RichText\n    Text {\n        id: label\n        textFormat: Text.PlainText\n    }\n}\n",
		want: "textFormat is \"Text.RichText\"",
	}, {
		what: "a rich format bound on an element the declaration scan does not know",
		src:  "import QtQuick\nItem {\n    TextEdit {\n        textFormat: Text.StyledText\n    }\n    Text {\n        textFormat: Text.PlainText\n    }\n}\n",
		want: "textFormat is \"Text.StyledText\"",
	}, {
		what: "a format this scan cannot read, which must fail rather than be skipped",
		src:  "import QtQuick\nItem {\n    Text {\n        textFormat: rich ? Text.RichText : Text.PlainText\n    }\n}\n",
		want: "textFormat is",
	}} {
		got := checkQML("case.qml", c.src)
		if !anyContains(got.problems, c.want) {
			t.Errorf("the guard said nothing about %s.\nwanted a complaint containing %q, got %v",
				c.what, c.want, got.problems)
		}
	}

	// The one that was not a blind spot but a lie: an element whose closing brace
	// carries a comment.
	//
	// The old walk wanted a line that was exactly indentation and a brace, so
	// `}  // the row` ended nothing, and the scan ran on and read the *next*
	// element's textFormat as this one's. A Text with no format at all came back
	// green because its sibling had one, which is the one failure mode worse than
	// not looking: there is nothing left to notice.
	//
	// So this asks for two things at once. The element with nothing on it is
	// named, and the sibling that does declare a format is not mistaken for it -
	// which is why the count matters as much as the message.
	const sibling = "import QtQuick\nItem {\n    Text {\n        text: body\n    }  // the row\n" +
		"    Text {\n        text: \"literal\"\n        textFormat: Text.PlainText\n    }\n}\n"
	switch got := checkQML("sibling.qml", sibling); {
	case len(got.problems) != 1:
		t.Errorf("a Text with no format beside one that has a format produced %d complaints, want the "+
			"one about the first: %v", len(got.problems), got.problems)
	case !strings.HasPrefix(got.problems[0], "sibling.qml:3: ") || !strings.Contains(got.problems[0], "no textFormat"):
		t.Errorf("the complaint is %q, want the element on line 3 named as the one with no textFormat",
			got.problems[0])
	}

	// And the fourth blind spot, which is not about source at all: a surface in a
	// subdirectory of shell/ used to be invisible, because the walk was one
	// os.ReadDir.
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "widgets"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"Top.qml", filepath.Join("widgets", "Buried.qml")} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(good), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	found, err := qmlFiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 2 {
		t.Errorf("the walk found %v, and a surface in a subdirectory of shell/ draws on the same "+
			"screen with the same keyboard grab as one at the top of it", found)
	}
}

func anyContains(problems []string, want string) bool {
	for _, p := range problems {
		if strings.Contains(p, want) {
			return true
		}
	}
	return false
}

// qmlFiles is every .qml under dir, however deep. The whole tree rather than one
// directory: a shell/widgets/ full of Text elements is a shell/widgets/ nothing
// was checking, and the day somebody makes one is exactly the day a scan of the
// top level would say the surfaces are fine.
func qmlFiles(dir string) ([]string, error) {
	var names []string
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(d.Name(), ".qml") {
			names = append(names, path)
		}
		return nil
	})
	sort.Strings(names)
	return names, err
}

// qmlSurfaces is every .qml in shell/, by path.
func qmlSurfaces(t *testing.T) []string {
	t.Helper()
	dir := filepath.Join("..", "..", "shell")
	names, err := qmlFiles(dir)
	if err != nil {
		t.Fatalf("walk %s: %v", dir, err)
	}
	if len(names) == 0 {
		t.Fatalf("no .qml under %s, so this test is checking nothing", dir)
	}
	return names
}

// readQMLPath is one surface's source, by the path the walk found it at.
// readQML in wire_test.go takes a bare name under shell/, which a walk that
// recurses cannot give it.
func readQMLPath(t *testing.T, path string) string {
	t.Helper()
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(src)
}

// shortQMLName is the path as the repo refers to it, so a failure names a file
// somebody can open rather than a walk's ../../ prefix.
func shortQMLName(path string) string {
	return filepath.ToSlash(strings.TrimPrefix(filepath.ToSlash(path), "../../"))
}

// elementBody is where the element opened at start ends, or the sentence saying
// why this scan will not guess.
//
// By indentation rather than by counting braces. A brace count would have to
// know which braces are inside a string literal, and these surfaces write JSON
// into one; the indentation in this tree is uniform, and a file where it is not
// has to fail here rather than be scanned wrongly.
//
// Which is the thing this had wrong, and it was worse than a blind spot. It
// looked for a line that was exactly the indentation and a brace, so a closing
// brace with a comment after it - `}  // the row` - was not the end of anything,
// and the walk ran on into the next element and read the next element's
// textFormat as this one's. An element with no format at all came back green
// because its sibling had one. A scan that mis-parses and reports success is
// worse than one that cannot see, because there is nothing left to notice.
//
// So the walk now ends at the first line at the element's own indentation, and
// that line has to be its closing brace. Anything else there - a sibling, a
// property, a stray dedent - is a shape this cannot read, and it says so instead
// of carrying on into somebody else's properties.
func elementBody(lines []string, start int, indent string) (end int, why string) {
	for j := start + 1; j < len(lines); j++ {
		if strings.TrimSpace(lines[j]) == "" {
			continue
		}
		prefix := lines[j][:len(lines[j])-len(strings.TrimLeft(lines[j], " \t"))]
		if strings.HasPrefix(prefix, indent) && prefix != indent {
			continue // inside the element
		}
		// At the element's own indentation, or further out. Either it is the
		// closing brace or this scan has lost the element.
		if prefix == indent && qmlValue(strings.TrimSpace(lines[j])) == "}" {
			return j, ""
		}
		return -1, "ends at line " + strconv.Itoa(j+1) + " with " +
			strconv.Quote(strings.TrimSpace(lines[j])) + " where its closing brace should be, so this " +
			"test cannot tell where it stops - and an element it reads the wrong end of is one whose " +
			"neighbour's properties it would read as its own. Indent the body one step further than " +
			"the declaration, or teach this test the shape."
	}
	return -1, "has no closing brace at its own indentation, so this test cannot tell where the " +
		"element stops and would be reading the next one."
}

// ownProperties is the lines that belong to this element rather than to a child
// of it: the ones indented exactly one step further than the declaration. A
// textFormat on a nested Text inside a Rectangle inside this one must not count
// as this element's answer, which is the mistake that would let a whole surface
// pass on one property.
//
// Comments are not properties, and that is a second way a green answer could be
// wrong rather than a tidiness. Every surface in this tree carries a paragraph
// about why the format is what it is, and the words `textFormat: Text.PlainText`
// appear in several of them; read as a property, one of those sentences would
// answer for an element that declares nothing at all.
func ownProperties(lines []string, start, end int) []int {
	step := ""
	for j := start + 1; j < end; j++ {
		if strings.TrimSpace(lines[j]) == "" || isQMLComment(lines[j]) {
			continue
		}
		step = lines[j][:len(lines[j])-len(strings.TrimLeft(lines[j], " \t"))]
		break
	}
	if step == "" {
		return nil
	}
	var own []int
	for j := start + 1; j < end; j++ {
		if strings.TrimSpace(lines[j]) == "" || isQMLComment(lines[j]) {
			continue
		}
		got := lines[j][:len(lines[j])-len(strings.TrimLeft(lines[j], " \t"))]
		if got == step {
			own = append(own, j)
		}
	}
	return own
}
