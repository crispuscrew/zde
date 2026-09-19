package zded

import (
	"encoding/json"
	"strings"
	"testing"
)

// Delivery alone cannot claim display; the event must name its monitor and acknowledgement token.
func TestAskOneshotTellsAListenerAndSaysWhetherItDrew(t *testing.T) {
	f := &fakeCompositor{m: twoDesks(), focused: "vshop.DP-1.code", output: "DP-1"}
	s := New("test", nil, f, nil)

	var shown bool
	if err := json.Unmarshal(s.Dispatch(Request{Method: "ask.oneshot"}).Ok, &shown); err != nil {
		t.Fatal(err)
	}
	if shown {
		t.Error("nothing is listening and the answer claims a window was drawn")
	}

	rec := &recorder{}
	s.listen(&sink{w: rec})
	resp := s.Dispatch(Request{Method: "ask.panel"})
	if resp.Error != "" {
		t.Fatalf("ask.panel: %s", resp.Error)
	}
	if err := json.Unmarshal(resp.Ok, &shown); err != nil {
		t.Fatal(err)
	}
	if shown {
		t.Error("the listener acknowledged nothing and the answer claims a window was drawn")
	}
	var got struct{ Event Event }
	line := strings.TrimSpace(rec.String())
	if err := json.Unmarshal([]byte(line), &got); err != nil {
		t.Fatalf("the event is not one line of json: %q", line)
	}
	if got.Event.Kind != EventAskPanel {
		t.Errorf("kind = %q, want %q", got.Event.Kind, EventAskPanel)
	}
	if got.Event.Output != "DP-1" {
		t.Errorf("output = %q, want the screen being looked at", got.Event.Output)
	}
	if got.Event.Token == "" {
		t.Error("the event carries no token, so nothing can say it drew the window")
	}
}

// The panel-opening event must carry the trimmed question to the surface.
func TestAPanelAskedForWithAQuestionOpensWithThatQuestionInIt(t *testing.T) {
	f := &fakeCompositor{m: twoDesks(), focused: "vshop.DP-1.code", output: "DP-1"}
	s := New("test", nil, f, nil)
	rec := &recorder{}
	s.listen(&sink{w: rec})

	resp := s.Dispatch(Request{Method: "ask.panel", Args: []string{"  what is the capital of peru\n"}})
	if resp.Error != "" {
		t.Fatalf("ask.panel with a question: %s", resp.Error)
	}
	var got struct{ Event Event }
	line := strings.TrimSpace(rec.String())
	if err := json.Unmarshal([]byte(line), &got); err != nil {
		t.Fatalf("the event is not one line of json: %q", line)
	}
	if got.Event.Kind != EventAskPanel {
		t.Errorf("kind = %q, want %q", got.Event.Kind, EventAskPanel)
	}
	if got.Event.Question != "what is the capital of peru" {
		t.Errorf("question = %q: the panel opens with what was typed, trimmed the way a tier gets it", got.Event.Question)
	}
}

// Terminal questions belong to terminal replies, never to a newly opened popup.
func TestTheOneshotPopupIsNeverHandedAQuestion(t *testing.T) {
	s := New("test", nil, &fakeCompositor{m: twoDesks()}, nil)
	rec := &recorder{}
	s.listen(&sink{w: rec})

	resp := s.Dispatch(Request{Method: "ask.oneshot", Args: []string{"what is the capital of peru"}})
	if resp.Error == "" {
		t.Fatal("ask.oneshot took a question, so a question typed in a terminal has two places to go")
	}
	if rec.String() != "" {
		t.Errorf("it was refused and a surface was asked for anyway: %q", rec.String())
	}
}

func TestAPanelQuestionOfNothingButSpaceOpensNoPanel(t *testing.T) {
	s := New("test", nil, &fakeCompositor{m: twoDesks()}, nil)
	rec := &recorder{}
	s.listen(&sink{w: rec})

	resp := s.Dispatch(Request{Method: "ask.panel", Args: []string{"   \n"}})
	if resp.Error == "" {
		t.Fatal("a question of nothing but space was accepted")
	}
	if rec.String() != "" {
		t.Errorf("nothing was asked and a panel was opened anyway: %q", rec.String())
	}
}
