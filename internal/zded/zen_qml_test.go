package zded

import (
	"regexp"
	"strings"
	"testing"
)

var (
	zenRequests = regexp.MustCompile(`\{[^{}]*(?:"method"|'method'|\bmethod)\s*:\s*(?:"desk\.zen"|'desk\.zen')[^{}]*\}`)
	zenReadOnly = regexp.MustCompile(`^\{\s*(?:"method"|'method'|method)\s*:\s*(?:"desk\.zen"|'desk\.zen')\s*\}$`)
)

// Unknown state and a live microphone must both override zen's hidden-bar state.
func TestTheBarGoesInZenAndComesBackForTheMicrophone(t *testing.T) {
	source := string(readQML(t, "StatusBar.qml"))
	visible := regexp.MustCompile(`(?m)^\s+visible:\s*(.*)$`)
	var found string
	for _, match := range visible.FindAllStringSubmatch(source, -1) {
		if strings.Contains(match[1], "zen") {
			found = match[1]
		}
	}
	want := regexp.MustCompile(`^!bar\.state\.zen\s*\|\|\s*!bar\.state\.zenKnown\s*\|\|\s*bar\.hardware\.mic\.live\s*$`)
	if !want.MatchString(found) {
		t.Errorf("bar visibility = %q; want zen hidden only when known and the microphone is idle", found)
	}
}

// Every shell request reads the daemon's zen state; only RequestChannel adds IDs.
func TestTheBarAsksAboutZenAndNeverSetsIt(t *testing.T) {
	requests := zenRequests.FindAllString(string(readShell(t)), -1)
	if len(requests) == 0 {
		t.Fatal("the active shell never asks about zen, so a restart cannot recover state")
	}
	for _, request := range requests {
		if !zenReadOnly.MatchString(request) {
			t.Errorf("zen request must contain only method, with no args or other properties: %s", request)
		}
	}
}

func TestZenRequestScannerAllowsOnlyMethodLiterals(t *testing.T) {
	for _, source := range []string{
		`stream.write('{"method":"desk.zen"}\n');`,
		`channel.call({method: "desk.zen"}, reply => state.zen = reply.ok.zen);`,
		"channel.call({\n  \"method\" : \"desk.zen\"\n});",
		`channel.call({'method': 'desk.zen'});`,
	} {
		requests := zenRequests.FindAllString(source, -1)
		if len(requests) != 1 || !zenReadOnly.MatchString(requests[0]) {
			t.Errorf("read-only zen request was not accepted: %s", source)
		}
	}
	for _, source := range []string{
		`channel.call({method: "desk.zen", args: ["on"]});`,
		`channel.call({args: ["toggle"], method: "desk.zen"});`,
		`channel.call({method: "desk.zen", args: []});`,
		`channel.call({method: "desk.zen", id: "poll"});`,
		`channel.call({method: "desk.zen", method: "desk.zen"});`,
	} {
		requests := zenRequests.FindAllString(source, -1)
		if len(requests) != 1 || zenReadOnly.MatchString(requests[0]) {
			t.Errorf("request with extra properties escaped the zen check: %s", source)
		}
	}
}
