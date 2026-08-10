package zded

import (
	"os"
	"path/filepath"
	"regexp"
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
// What this scans, and what it deliberately does not:
//
//   - Every *.qml under shell/, not a named list. The day somebody adds a
//     surface is exactly the day a list of names would go stale.
//   - Declarations written as a line whose last character is the opening brace,
//     which is how every element in this tree is written. A Text declared with
//     anything after that brace fails rather than being skipped: a scan that
//     quietly cannot read a form is a scan that passes on the thing it exists
//     to catch.
//   - TextInput and TextField are not scanned, and could not be fixed if they
//     were. QQuickTextInput has no textFormat property at all and never
//     interprets markup, so the password field in Links.qml and the filter
//     fields in the palette and the clip history are safe by construction.
//   - TextEdit is not required to declare one, because its default is already
//     PlainText rather than AutoText. It is still covered, by the second test
//     below: that one refuses any textFormat other than PlainText anywhere in
//     shell/, which is what catches a TextEdit, a Label, or anything else that
//     grows the property later.
//   - The scan is line-based and knows nothing about block comments. These files
//     use // exclusively, so a commented-out Text would be a false failure and
//     not a false pass, which is the direction to be wrong in.
var textDeclaration = regexp.MustCompile(`^([ \t]*)Text[ \t]*\{(.*)$`)

// Any textFormat, and what it was set to. Deliberately not anchored to Text:
// the point of the second test is to see one on an element this file does not
// know about yet.
var anyTextFormat = regexp.MustCompile(`textFormat[ \t]*:[ \t]*([A-Za-z0-9_.]+)`)

func TestEveryTextInTheShellIsDrawnAsPlainText(t *testing.T) {
	found := 0
	for _, file := range qmlSurfaces(t) {
		lines := strings.Split(string(readQML(t, file)), "\n")
		for i, line := range lines {
			m := textDeclaration.FindStringSubmatch(line)
			if m == nil {
				continue
			}
			found++
			where := file + ":" + strconv.Itoa(i+1)
			indent, rest := m[1], m[2]
			if strings.TrimSpace(rest) != "" {
				t.Errorf("%s: this Text is declared with %q after the brace, and this scan reads "+
					"one element per line. Either put the properties on their own lines, or teach "+
					"this test the new shape - a Text it cannot read is a Text it cannot check.",
					where, strings.TrimSpace(rest))
				continue
			}

			end := closingBrace(lines, i, indent)
			if end < 0 {
				t.Errorf("%s: this Text has no closing brace at its own indentation, so this test "+
					"cannot tell where the element stops and would be reading the next one.", where)
				continue
			}
			own := ownProperties(lines, i, end)
			if len(own) == 0 {
				t.Errorf("%s: this Text declares nothing at all, so it declares no textFormat. %s",
					where, whyPlainText)
				continue
			}

			name := where
			for _, j := range own {
				if id, ok := strings.CutPrefix(strings.TrimSpace(lines[j]), "id: "); ok {
					name = where + " (" + strings.TrimSpace(id) + ")"
				}
			}

			format := ""
			for _, j := range own {
				if f := anyTextFormat.FindStringSubmatch(lines[j]); f != nil {
					format = f[1]
				}
			}
			switch format {
			case "Text.PlainText":
			case "":
				t.Errorf("%s: no textFormat, so Qt draws it as Text.AutoText. %s", name, whyPlainText)
			default:
				t.Errorf("%s: textFormat is %s, which interprets what it is given. %s",
					name, format, whyPlainText)
			}
		}
	}
	if found == 0 {
		t.Fatal("this scan found no Text element in the whole of shell/, so it is checking nothing: " +
			"either every surface went away, or elements are written in a shape textDeclaration " +
			"no longer matches and this test has gone blind")
	}
}

// The same rule from the other side, for everything this file does not know how
// to find. A Text is one way to interpret a string a sender wrote; a TextEdit, a
// Label from QtQuick.Controls, or a Text declared in a shape the scan above
// cannot read are others, and all of them take the same property. So: nowhere in
// shell/ does a textFormat name anything except PlainText.
//
// The type in front of the dot is not checked, only the value after it. QML
// scopes an enum to its type, so a TextEdit spells the same constant
// TextEdit.PlainText, and a test that demanded the string "Text.PlainText"
// everywhere would refuse the correct spelling on every element except a Text.
//
// This is what would catch the change that turns one label into rich text to get
// a bold word into it, which is how a shell acquires this bug in the first place.
func TestNoSurfaceAsksQtToInterpretWhatItIsGiven(t *testing.T) {
	seen := 0
	for _, file := range qmlSurfaces(t) {
		for i, line := range strings.Split(string(readQML(t, file)), "\n") {
			// Comments talk about the formats by name - this file's own rule is
			// written into every header in shell/ - and a scan that read those
			// would fail on the sentence explaining why it exists.
			if strings.HasPrefix(strings.TrimSpace(line), "//") {
				continue
			}
			m := anyTextFormat.FindStringSubmatch(line)
			if m == nil {
				continue
			}
			seen++
			if !strings.HasSuffix(m[1], ".PlainText") {
				t.Errorf("%s:%d: textFormat is %s. %s", file, i+1, m[1], whyPlainText)
			}
		}
	}
	if seen == 0 {
		t.Fatal("no textFormat anywhere in shell/, which cannot be true while the test above " +
			"passes: this scan has gone blind")
	}
}

const whyPlainText = "Every string on these surfaces was chosen by something other than the person " +
	"at the machine, and Text.AutoText hands anything that looks like markup to Qt's StyledText " +
	"parser, which fetches <img src=\"http://...\"> over the network from inside the process " +
	"holding the keyboard. Write textFormat: Text.PlainText, and read the comment above " +
	"textDeclaration in this file before deciding this element is the exception."

// qmlSurfaces is every .qml file in shell/, by name. Read fresh each time rather
// than cached: these are two tests and the second must not depend on the first
// having run.
func qmlSurfaces(t *testing.T) []string {
	t.Helper()
	dir := filepath.Join("..", "..", "shell")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".qml") {
			names = append(names, e.Name())
		}
	}
	if len(names) == 0 {
		t.Fatalf("no .qml in %s, so both tests below are checking nothing", dir)
	}
	return names
}

// closingBrace is the line that ends the element opened at start, found by
// indentation rather than by counting braces. A brace count would have to know
// which braces are inside a string literal, and these surfaces write JSON into
// one; the indentation in this tree is uniform, and a file where it is not
// fails loudly here rather than being scanned wrongly.
func closingBrace(lines []string, start int, indent string) int {
	want := indent + "}"
	for j := start + 1; j < len(lines); j++ {
		if strings.TrimRight(lines[j], " \t") == want {
			return j
		}
	}
	return -1
}

// ownProperties is the lines that belong to this element rather than to a child
// of it: the ones indented exactly one step further than the declaration. A
// textFormat on a nested Text inside a Rectangle inside this one must not count
// as this element's answer, which is the mistake that would let a whole surface
// pass on one property.
func ownProperties(lines []string, start, end int) []int {
	step := ""
	for j := start + 1; j < end; j++ {
		if strings.TrimSpace(lines[j]) == "" {
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
		if strings.TrimSpace(lines[j]) == "" {
			continue
		}
		got := lines[j][:len(lines[j])-len(strings.TrimLeft(lines[j], " \t"))]
		if got == step {
			own = append(own, j)
		}
	}
	return own
}
