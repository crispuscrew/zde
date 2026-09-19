package zded

import "testing"

func TestTheWireScannersSeeADottedName(t *testing.T) {
	kinds := kindsIn(kindInGo, []byte(`
const EventPicker = "picker"
const (
    EventAskPanel = "ask.panel"
    EventNet = "connections"
)
`))
	for _, want := range []string{"picker", "ask.panel", "connections"} {
		if !kinds[want] {
			t.Errorf("Go scanner missed %q: %v", want, kinds)
		}
	}
	drawn := kindsIn(kindInQML, []byte(`if (msg.event.kind === "ask.panel") root.openAsk(msg.event);`))
	if !drawn["ask.panel"] {
		t.Errorf("QML scanner missed dotted kind: %v", drawn)
	}
	methods := kindsIn(methodInQML, []byte(`stream.write('{"method":"attn.mode"}\n');`))
	if !methods["attn.mode"] {
		t.Errorf("method scanner missed dotted name: %v", methods)
	}
}

func TestTheKindScannerReadsOnlyEventKinds(t *testing.T) {
	drawn := kindsIn(kindInQML, []byte(`
    if (message.event.kind === "picker") root.openPicker(message.event);
    text: netState.kind === "wifi" ? "wifi" : "wired"
    visible: root.linkKind !== "absent"
`))
	if !drawn["picker"] {
		t.Errorf("event kind was not found: %v", drawn)
	}
	for _, notAnEvent := range []string{"wifi", "wired", "absent"} {
		if drawn[notAnEvent] {
			t.Errorf("ordinary property %q was read as an event", notAnEvent)
		}
	}
}

func TestTheKindScannerSeesBothWaysOfAsking(t *testing.T) {
	for _, source := range []string{
		`if (msg.event.kind === "ask.text") root.openAsk(msg.event);`,
		`if (msg.event.kind !== "ask.text") return;`,
		`if (event.kind == "ask.text") root.openAsk(event);`,
		`if (event.kind != "ask.text") return;`,
	} {
		if drawn := kindsIn(kindInQML, []byte(source)); !drawn["ask.text"] {
			t.Errorf("event comparison was invisible: %s", source)
		}
	}
}

func TestTheMethodScannerSeesAMethodHandedToAHelper(t *testing.T) {
	found := kindsIn(methodHandedOnInQML, []byte(`
    property string pickMethod: "desk.switcher"
    targets.pickMethod = "window.jump-to";
    network.ask("net.status", []);
    surface.send("palette.list", []);
    channel.call ("desk.zen", []);
    root.command("bluetooth.pair", [device.address]);
    readonly property string httpMethod: "GET"
    Qt.createComponent("picker.qml");
`))
	for _, want := range []string{"desk.switcher", "window.jump-to", "net.status", "palette.list", "desk.zen", "bluetooth.pair"} {
		if !found[want] {
			t.Errorf("helper scanner missed %q: %v", want, found)
		}
	}
	for _, notAMethod := range []string{"GET", "picker.qml"} {
		if found[notAMethod] {
			t.Errorf("%q was wrongly read as a method", notAMethod)
		}
	}
}

func TestTheMethodScannerReadsOnlyRequests(t *testing.T) {
	found := kindsIn(methodInQML, []byte(`
    stream.write('{"method":"queue.list"}\n');
    channel.call({
        method: "attn.invoke",
        args: [which, key]
    });
    channel.call({ "method" : "desk.zen" });
    readonly property string method: "GET"
    function fetchIt() {
        return http.send({url: "/v1/ask", method: "POST"});
    }
`))
	for _, want := range []string{"queue.list", "attn.invoke", "desk.zen"} {
		if !found[want] {
			t.Errorf("request scanner missed %q: %v", want, found)
		}
	}
	for _, notARequest := range []string{"GET", "POST"} {
		if found[notARequest] {
			t.Errorf("%q was wrongly read as a request", notARequest)
		}
	}
}
