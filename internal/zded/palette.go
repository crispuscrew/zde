package zded

import (
	"os"
	"os/exec"
	"strconv"
	"time"

	"github.com/crispuscrew/zde/internal/keymap"
)

// Action is one row of the palette: an action a person can ask for by name,
// what it does, the key that would have done it, and whether asking for it here
// does anything at all.
//
// The last part is the point of the thing at this stage. Most of the keymap is
// bound to commands nobody has written yet, and a bind whose command is not
// written is a key that does nothing, silently (README, Missing). The rows are
// shown anyway rather than left out: somebody who has forgotten a key is better
// served by "that one is not written yet, and here is the key it will be" than
// by a list that pretends the action does not exist - and the cheatsheet on
// Mod+slash lists them too, so a palette that hid them would be a second,
// shorter answer to the same question.
type Action struct {
	Name  string `json:"name"`
	Group string `json:"group"`
	Desc  string `json:"desc"`
	Key   string `json:"key,omitempty"`
	// Live says picking it does something on this machine. Why says why not,
	// in the same words the refusal uses when somebody picks one anyway.
	Live bool   `json:"live"`
	Why  string `json:"why,omitempty"`
}

// Palette is what palette.list answers: the actions, and whether a surface took
// the job of showing them. Shown means here exactly what it means for the desk
// switcher, and for the same reason (see Switcher).
type Palette struct {
	Shown   bool     `json:"shown"`
	Actions []Action `json:"actions"`
}

// palette opens the palette, or says that nothing could open it.
//
// It asks niri only which screen is being looked at, and does not mind not
// being told. The list itself is a file and a compiled-in registry, so this is
// the one surface that still works on a session whose compositor has stopped
// answering - which is also the session where somebody most wants to run
// `zde doctor` by name.
func (s *Server) palette() Response {
	list := s.actions()
	_, output, err := s.niri.FocusedPlace()
	if err != nil {
		output = ""
	}
	token := s.nextToken()
	acked := s.await(token)
	defer s.stopAwaiting(token)

	sent := s.broadcast(Event{
		Kind:    EventPalette,
		Actions: list,
		Output:  output,
		Token:   token,
	})
	if sent == 0 {
		return ok(Palette{Shown: false, Actions: list})
	}
	select {
	case <-acked:
		return ok(Palette{Shown: true, Actions: list})
	case <-time.After(ackWait):
		return ok(Palette{Shown: false, Actions: list})
	}
}

// actions is the list as it goes over the wire.
func (s *Server) actions() []Action {
	known := known()
	out := make([]Action, 0, len(known))
	for _, a := range known {
		why := whyNot(a)
		out = append(out, Action{
			Name:  a.Name,
			Group: a.Group,
			Desc:  a.Desc,
			Key:   a.Key,
			Live:  why == "",
			Why:   why,
		})
	}
	return out
}

// known is every action, with the keys this machine has bound to them.
//
// Read from disk each time rather than at startup: the file is written by a
// home-manager switch, zded is restarted by one too but not always before
// somebody presses the key, and a list of keys from the last build is exactly
// the kind of confident wrong answer `zde keys` exists to avoid. It is one
// small file per keypress.
//
// A missing file is not an error. It means layer 1 has never been activated, or
// this is a zde built by hand; the actions are still every action zde has, and
// what is not known is which keys reach them.
//
// Nor is anything else the read can say. Every failure lands in the same place -
// the keys are not known and the actions still are - and that is what makes the
// read through keymap.ReadText load-bearing rather than tidy: a FIFO at
// $XDG_CONFIG_HOME/zde/keymap.txt used to leave this parked in the kernel with
// no writer coming, on the goroutine serving palette.list or palette.run, so the
// palette key never opened, `zde keys` hung, and each call leaked a goroutine
// and a descriptor that closing the socket could not take back.
func known() []keymap.Action {
	data, err := keymap.ReadText()
	if err != nil {
		data = nil
	}
	return keymap.Actions(data)
}

// whyNot is why picking a row would do nothing, and empty when it would do
// something.
//
// One function, because the list says it on the row and the refusal says it
// back to whoever picked one anyway - and two spellings of the same judgement
// would eventually disagree about which rows work.
func whyNot(a keymap.Action) string {
	if a.Native != "" {
		// niri's own, and one it will not take from us: an argument in niri's
		// own types, or a field the bind does not carry and the socket has no
		// default for (internal/keymap, performs). The key does it, which is
		// what the row goes on saying.
		//
		// Read off the registry rather than worked out from the action line.
		// Deriving it called the three screenshots runnable, because their line
		// looks like every other bare action, and niri refuses all three - the
		// palette claiming a key that works does not, which is the one mistake
		// this surface must not make.
		if !a.Performs {
			return "niri's own, and the key is the only way to ask for it"
		}
		return ""
	}
	if !a.Live || len(a.Spawn) == 0 {
		return "nothing is written behind it yet"
	}
	// The binary, not the verb. `zde` is always here and half its verbs are not,
	// which is what Live already answered; this is the other half - wpctl,
	// brightnessctl, zlg - and it is a fact about this machine rather than about
	// this build, so it is asked every time the list is built.
	//
	// zded's PATH, and the key spawns from niri's. The two are the same on a
	// machine where both came up from one login, and not on one where somebody
	// restarted the daemon by hand from a shell with less on its path - and then
	// the row says a program is missing about a key that works. Answering it the
	// way the key would means asking niri what it would spawn with, which niri
	// does not offer.
	if _, err := exec.LookPath(a.Spawn[0]); err != nil {
		return a.Spawn[0] + " is not installed"
	}
	return ""
}

// runAction does what the bind would have done: the argv it spawns, or the niri
// action it performs. It is the whole of `palette.run NAME`, and of the choice a
// palette surface sends back.
func (s *Server) runAction(name string) Response {
	for _, a := range known() {
		if a.Name != name {
			continue
		}
		if why := whyNot(a); why != "" {
			// The half that makes marking a row worth more than hiding it: the
			// key itself would have done nothing and said nothing, and this
			// says what nothing means.
			return Response{Error: name + ": " + why}
		}
		if a.Native != "" {
			if err := s.niri.Perform(a.Native); err != nil {
				return Response{Error: err.Error()}
			}
			return ok([]string{})
		}
		if err := s.spawn(a.Spawn); err != nil {
			return Response{Error: err.Error()}
		}
		return ok([]string{})
	}
	return Response{Error: "no action called " + strconv.Quote(name) + ": `zde palette` lists them"}
}

// spawnDetached starts what a key would have started.
//
// Reaped in the background rather than waited for: the answer to `palette.run`
// is that it started, and a terminal started this way outlives the keypress by
// hours. Its output goes where zded's does, which is the journal - the same
// nowhere a key's would go, since niri gives a spawned command its own log and
// no screen either.
//
// What is not the same as a key: this is zded's child, where the key's belongs
// to niri. A zded restarted by a home-manager switch takes what the palette
// started with it, and niri surviving that means the key's child survives it
// too. Nothing worth keeping is startable from here yet - the palette starts
// terminals and lockers - and the fix is a scope of its own, which is a thing to
// do when something wants one.
func spawnDetached(argv []string) error {
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait()
	return nil
}
