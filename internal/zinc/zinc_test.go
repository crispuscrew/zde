package zinc

import "testing"

func TestAddress(t *testing.T) {
	if got := Address("browser", "work"); got != "browser@work" {
		t.Fatalf("Address(browser, work) = %q", got)
	}
	// The app that has always existed keeps the name it has always had. A
	// "browser@" here would be a new name for a running container, which is
	// the one thing instance addressing promised not to do.
	if got := Address("browser", ""); got != "browser" {
		t.Fatalf("Address(browser, no instance) = %q", got)
	}
}

func TestParseWhere(t *testing.T) {
	// The shape zinc documents as the contract (zinc CHANGELOG, 0.8.1).
	out := "state: /home/u/.local/state/zinc/firefox/work\ncontainer: firefox.work\n"
	loc, err := parseWhere(out)
	if err != nil {
		t.Fatal(err)
	}
	if loc.State != "/home/u/.local/state/zinc/firefox/work" {
		t.Fatalf("state = %q", loc.State)
	}
	// The runtime name is the other half and is not derivable from the address
	// by anyone who does not already know the `@` becomes a `.`.
	if loc.Container != "firefox.work" {
		t.Fatalf("container = %q", loc.Container)
	}
}

func TestParseWhereRefusesHalfAnAnswer(t *testing.T) {
	// A path with a colon in it is ordinary, so the value is everything after
	// the first one rather than everything before the second.
	loc, err := parseWhere("state: /srv/odd:name/zinc/a/b\ncontainer: a.b\n")
	if err != nil {
		t.Fatal(err)
	}
	if loc.State != "/srv/odd:name/zinc/a/b" {
		t.Fatalf("state = %q", loc.State)
	}
	// Missing either line is a zcr that has changed its mind about the format.
	// Filling in the gap is how a confident wrong path gets printed, so this
	// says it cannot rather than guessing.
	for _, out := range []string{
		"state: /home/u/.local/state/zinc/firefox/work\n",
		"container: firefox.work\n",
		"firefox\n",
		"",
	} {
		if _, err := parseWhere(out); err == nil {
			t.Fatalf("parseWhere(%q) accepted half an answer", out)
		}
	}
}
