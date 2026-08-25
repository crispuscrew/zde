package niri

import (
	"bufio"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Every field, spelled out, because niri supplies none of them over the socket.
//
// This is the test that says why zde asking for a screenshot is not the same as
// the bind asking for one. niri-ipc's Action derives plain Serialize/Deserialize
// with no serde defaults; the defaults live in the KDL bind parser
// (niri-config, binds.rs). So a bare {"Screenshot":{}} is "error parsing
// request" while Print works, which is the asymmetry the palette used to have to
// warn about (internal/keymap, performs). A field dropped here is invisible in
// Go and is a key that stops taking screenshots.
//
// show_pointer is checked as well as present, because the two do not agree:
// ScreenshotWindow's bind default is false and ScreenshotScreen's is true. A key
// that used to be a native and now spawns zde has to take the same picture.
func TestTheScreenshotRequestsCarryEveryFieldNiriNeeds(t *testing.T) {
	sent := &asked{}
	path := fakeNiriAsked(t, sent, `{"Ok":"Handled"}`, `{"Ok":"Handled"}`)
	s := &Shots{act: dial(t, path)}

	if err := s.ScreenshotWindow("/home/you/Pictures/Screenshots/b.png"); err != nil {
		t.Fatal(err)
	}
	if err := s.ScreenshotScreen("/home/you/Pictures/Screenshots/c.png"); err != nil {
		t.Fatal(err)
	}

	lines := sent.all()
	if len(lines) != 2 {
		t.Fatalf("niri was sent %d requests: %v", len(lines), lines)
	}
	for i, want := range []string{
		`{"Action":{"ScreenshotWindow":{"id":null,"path":"/home/you/Pictures/Screenshots/b.png","show_pointer":false,"write_to_disk":true}}}`,
		`{"Action":{"ScreenshotScreen":{"path":"/home/you/Pictures/Screenshots/c.png","show_pointer":true,"write_to_disk":true}}}`,
	} {
		if lines[i] != want {
			t.Errorf("request %d was\n %s\nwant\n %s", i, lines[i], want)
		}
	}
}

// The event that says where the file went, which is the only thing that does:
// niri saves on a thread and save_screenshot returns Ok before it starts, so
// "Handled" is a request that parsed and nothing more.
func TestCapturedReportsWhereTheFileWent(t *testing.T) {
	path := fakeNiriStream(t,
		`{"WindowsChanged":{"windows":[]}}`,
		`{"ScreenshotCaptured":{"path":"/home/you/Pictures/Screenshots/a.png"}}`,
		`{"ScreenshotCaptured":{"path":null}}`,
	)
	events, err := dial(t, path).captured()
	if err != nil {
		t.Fatal(err)
	}
	// The workspace event in the middle is there so that a reader keying on
	// nothing but "a line arrived" fails: it must be the screenshot events and
	// only those, in the order niri sent them.
	for i, want := range []string{"/home/you/Pictures/Screenshots/a.png", ""} {
		select {
		case got := <-events:
			if got != want {
				t.Errorf("event %d = %q, want %q", i, got, want)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("nothing arrived for event %d", i)
		}
	}
}

// A compositor that will not stream is answered before anything is captured,
// rather than after a picture has been taken with nowhere to hear about it.
func TestCapturedCarriesNirisRefusal(t *testing.T) {
	path := fakeNiri(t, `{"Err":"unknown request"}`)
	if _, err := dial(t, path).captured(); err == nil {
		t.Fatal("a compositor that refused the stream reported one")
	} else if !strings.Contains(err.Error(), "unknown request") {
		t.Errorf("niri's own refusal did not travel: %v", err)
	}
}

// fakeNiriStream is a niri that answers the subscription and then pushes events
// nobody asked for, which is what an event stream is. The fake beside it
// (client_test.go) writes one reply per request read, so a stream would stop
// after the request that opened it.
func fakeNiriStream(t *testing.T, events ...string) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "niri")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "s")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	held := make(chan struct{})
	t.Cleanup(func() { close(held); ln.Close(); os.RemoveAll(dir) })

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		if _, err := bufio.NewReader(conn).ReadBytes('\n'); err != nil {
			return
		}
		lines := append([]string{`{"Ok":{"Handled":{}}}`}, events...)
		for _, line := range lines {
			if _, err := conn.Write(append(oneLine(line), '\n')); err != nil {
				return
			}
		}
		// Held open until the test is done, because a stream that closes is a
		// different fact about niri from a stream with nothing more to say.
		<-held
	}()
	return path
}
