package desk

import "testing"

func TestMoveToKeepsTheLabelAndTheMonitor(t *testing.T) {
	m := Rebuild([]Workspace{
		{Name: "vshop.DP-1.comms", Output: "DP-1"},
		{Name: "haven.DP-1.db", Output: "DP-1"},
	}, []string{"DP-1"})

	from, err := ParseName("vshop.DP-1.comms")
	if err != nil {
		t.Fatal(err)
	}
	got, ok := MoveTo(m, from, Regulars)
	if !ok {
		t.Fatal("MoveTo refused a move it can make")
	}
	if got.String() != "regulars.DP-1.comms" {
		t.Errorf("MoveTo = %s, want regulars.DP-1.comms", got)
	}
}

// The label is what a person reads, so a second one has to be told apart from
// the first rather than replacing it - the same rule adoption uses for a second
// firefox.
func TestMoveToNumbersALabelAlreadyInTheBand(t *testing.T) {
	m := Rebuild([]Workspace{
		{Name: "vshop.DP-1.comms", Output: "DP-1"},
		{Name: "regulars.DP-1.comms", Output: "DP-1"},
	}, []string{"DP-1"})

	from, _ := ParseName("vshop.DP-1.comms")
	got, ok := MoveTo(m, from, Regulars)
	if !ok {
		t.Fatal("MoveTo refused")
	}
	if got.String() != "regulars.DP-1.comms-2" {
		t.Errorf("MoveTo = %s, want regulars.DP-1.comms-2", got)
	}
}

// Out of the band as well as into it. A band that can only be added to is a
// band that fills up (docs/roadmap.md), and the verb is the same rename either
// way round.
func TestMoveToBringsWorkOutOfTheRegulars(t *testing.T) {
	m := Rebuild([]Workspace{
		{Name: "regulars.DP-1.comms", Output: "DP-1"},
		{Name: "vshop.DP-1.code", Output: "DP-1"},
	}, []string{"DP-1"})

	from, _ := ParseName("regulars.DP-1.comms")
	got, ok := MoveTo(m, from, "vshop")
	if !ok {
		t.Fatal("MoveTo refused")
	}
	if got.String() != "vshop.DP-1.comms" {
		t.Errorf("MoveTo = %s, want vshop.DP-1.comms", got)
	}
}

// A band nobody owns anything on yet is the case that makes the regulars
// possible at all: the first workspace named into it is how it comes into
// being, so an empty target must not be an obstacle.
func TestMoveToIntoAnEmptyBand(t *testing.T) {
	m := Rebuild([]Workspace{
		{Name: "vshop.DP-1.comms", Output: "DP-1"},
	}, []string{"DP-1"})

	from, _ := ParseName("vshop.DP-1.comms")
	got, ok := MoveTo(m, from, Regulars)
	if !ok || got.String() != "regulars.DP-1.comms" {
		t.Errorf("MoveTo = %s, %v; want regulars.DP-1.comms", got, ok)
	}
}

// A band name a workspace name cannot hold has to be refused rather than
// mangled into one that cannot be parsed back.
func TestMoveToRefusesAnImpossibleName(t *testing.T) {
	from, _ := ParseName("vshop.DP-1.comms")
	if got, ok := MoveTo(nil, from, "Not A Desk"); ok {
		t.Errorf("MoveTo built %s out of an invalid band name", got)
	}
}
