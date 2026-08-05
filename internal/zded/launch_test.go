package zded

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crispuscrew/zde/internal/attn"
	"github.com/crispuscrew/zde/internal/journal"
	"github.com/crispuscrew/zde/internal/manifest"
)

// deskThatDeclares is a server on a desk nobody is standing on, whose manifest
// names these apps. Entering it is what these tests do.
func deskThatDeclares(t *testing.T, apps ...string) (*Server, *fakeCompositor) {
	t.Helper()
	dir := t.TempDir()
	yaml := "name: vshop\nmonitors: { DP-1: { workspaces: [code] } }\napps:\n"
	for _, app := range apps {
		yaml += "  - { app: " + app + ", instance: vshop }\n"
	}
	if err := os.WriteFile(filepath.Join(dir, "vshop.yaml"), []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	f := &fakeCompositor{m: twoDesks(), focused: "haven.DP-1.db", output: "DP-1"}
	// With a journal: an arrival is recorded against it, and a notification
	// nothing can keep is not a notification (see Arrived).
	jrn, err := journal.Open(filepath.Join(t.TempDir(), "j.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { jrn.Close() })
	return New("test", jrn, f, manifest.Dir(dir)), f
}

// arrivals waits for what the switch had to say, since the launches happen on
// the goroutine behind it. Everything the history has, newest first.
func arrivals(t *testing.T, s *Server, want int) []attn.Record {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		got := s.history.Recent()
		if len(got) >= want {
			return got
		}
		if time.Now().After(deadline) {
			t.Fatalf("waited for %d notifications and %d arrived: %+v", want, len(got), got)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// The failure this exists to stop being silent: a person switches to a desk,
// gets fewer windows than the manifest declares, and the only record of which
// one and why was a line in a daemon's log.
func TestADeskThatCannotStartAnAppSaysWhichOneAndWhy(t *testing.T) {
	s, _ := deskThatDeclares(t, "nvim")
	s.launch = func(address string) error {
		return fmt.Errorf("zcr is not on PATH, so %q cannot be started (programs.zinc.enable)", address)
	}

	if resp := s.Dispatch(Request{Method: "desk.switch", Args: []string{"vshop"}}); resp.Error != "" {
		t.Fatal(resp.Error)
	}
	got := arrivals(t, s, 1)[0]
	if !strings.Contains(got.Text, "vshop") || !strings.Contains(got.Text, "nvim@vshop") {
		t.Errorf("notification says %q, want the desk and the app that did not start", got.Text)
	}
	if !strings.Contains(got.Body, "zcr is not on PATH") {
		t.Errorf("notification body is %q, want what the runner said about it", got.Body)
	}
	// Not urgent: urgency is what crosses focus and quiet modes, and a missing
	// window is not worth breaking somebody's concentration for.
	if got.Urgent {
		t.Error("a desk that came up short interrupted a focus mode")
	}
}

// The noise bound. A desk that declares eight apps and can run none of them is
// every desk on a machine with no layer 2, and eight arrivals for one keypress
// would make the whole mechanism something to turn off.
func TestADeskThatCanStartNoneOfItsAppsIsOneNotification(t *testing.T) {
	var apps []string
	for i := range 8 {
		apps = append(apps, fmt.Sprintf("app-%d", i))
	}
	s, _ := deskThatDeclares(t, apps...)
	s.launch = func(address string) error { return errors.New("no such app") }

	if resp := s.Dispatch(Request{Method: "desk.switch", Args: []string{"vshop"}}); resp.Error != "" {
		t.Fatal(resp.Error)
	}
	got := arrivals(t, s, 1)
	// Long enough for a second one to have shown up if the count were per app.
	time.Sleep(200 * time.Millisecond)
	if got = s.history.Recent(); len(got) != 1 {
		t.Fatalf("eight apps that would not start produced %d notifications: %+v", len(got), got)
	}
	if !strings.Contains(got[0].Text, "8 apps") {
		t.Errorf("notification says %q, want the count", got[0].Text)
	}
	// And the body names the first few and counts the rest, rather than being a
	// wall of eight complaints or a number with nothing to act on.
	lines := strings.Split(got[0].Body, "\n")
	if len(lines) != launchesNamed+1 {
		t.Fatalf("body is %d lines, want %d named and one counting the rest:\n%s", len(lines), launchesNamed, got[0].Body)
	}
	if !strings.Contains(lines[0], "app-0@vshop") || !strings.Contains(lines[len(lines)-1], "4 more") {
		t.Errorf("body is\n%s\nwant the first few named and the rest counted", got[0].Body)
	}
}

// One failure in four is the case somebody actually meets: three windows
// arrived and one did not, and the notification is about the one.
func TestADeskSaysNothingAboutTheAppsThatStarted(t *testing.T) {
	s, _ := deskThatDeclares(t, "browser", "nvim", "term")
	s.launch = func(address string) error {
		if address == "nvim@vshop" {
			return errors.New("no app \"nvim\" defined")
		}
		return nil
	}

	if resp := s.Dispatch(Request{Method: "desk.switch", Args: []string{"vshop"}}); resp.Error != "" {
		t.Fatal(resp.Error)
	}
	got := arrivals(t, s, 1)[0]
	if strings.Contains(got.Body, "browser") || strings.Contains(got.Body, "term") {
		t.Errorf("notification mentions an app that started: %q", got.Body)
	}
	if !strings.Contains(got.Text, "nvim@vshop") {
		t.Errorf("notification says %q, want the one that did not start", got.Text)
	}
}

// A desk that came up whole has nothing to say, and saying it anyway would make
// every switch cost a queue item.
func TestADeskWhoseAppsAllStartNotifiesNobody(t *testing.T) {
	s, _ := deskThatDeclares(t, "browser", "nvim")
	started := make(chan string, 4)
	s.launch = func(address string) error {
		started <- address
		return nil
	}

	if resp := s.Dispatch(Request{Method: "desk.switch", Args: []string{"vshop"}}); resp.Error != "" {
		t.Fatal(resp.Error)
	}
	for range 2 {
		select {
		case <-started:
		case <-time.After(2 * time.Second):
			t.Fatal("the desk declares two apps and did not start them")
		}
	}
	if got := s.history.Recent(); len(got) != 0 {
		t.Errorf("a desk that started everything it declares said %+v", got)
	}
}

// The notification path must never be able to cost somebody the switch. Without
// a journal nothing can be kept, which is the way this fails that is not made
// up: the daemon still switches the desk and still starts what it can.
func TestASwitchSurvivesANotificationThatCannotBeKept(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "vshop.yaml"), []byte(
		"name: vshop\nmonitors: { DP-1: { workspaces: [code] } }\napps:\n"+
			"  - { app: nvim, instance: vshop }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f := &fakeCompositor{m: twoDesks(), focused: "haven.DP-1.db", output: "DP-1"}
	s := New("test", nil, f, manifest.Dir(dir)) // no journal: Arrived refuses
	tried := make(chan string, 1)
	s.launch = func(address string) error {
		tried <- address
		return errors.New("no such app")
	}

	if resp := s.Dispatch(Request{Method: "desk.switch", Args: []string{"vshop"}}); resp.Error != "" {
		t.Fatalf("a launch that could not be reported took the switch with it: %s", resp.Error)
	}
	select {
	case <-tried:
	case <-time.After(2 * time.Second):
		t.Fatal("the switch never tried to start what the desk declares")
	}
}

// zcr hands back everything the runner printed, and a failed image build is a
// screen of it. One line per failure, bounded, because the alternative is a
// notification body made of one app's stack trace.
func TestALaunchFailureIsCutToALineInTheNotification(t *testing.T) {
	long := strings.Repeat("x", reasonMax*2)
	body := launchBody([]launchFailure{
		{Address: "nvim@vshop", Err: errors.New("Error: short line\nError: preparing container\nmore\nand more")},
		{Address: "browser@vshop", Err: errors.New(long)},
	})
	lines := strings.Split(body, "\n")
	if len(lines) != 2 {
		t.Fatalf("body is %d lines for two failures:\n%s", len(lines), body)
	}
	if !strings.HasPrefix(lines[0], "nvim@vshop: Error: short line") || !strings.HasSuffix(lines[0], "...") {
		t.Errorf("line = %q, want the first line of the complaint and a mark that there was more", lines[0])
	}
	if n := len([]rune(lines[1])); n > len("browser@vshop: ")+reasonMax+len(" ...") {
		t.Errorf("one failure took %d characters of the body", n)
	}
}
