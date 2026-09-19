pragma ComponentBehavior: Bound

import QtQuick

QtObject {
    id: scenario
    required property var harness
    property int connections: 0
    property int failures: 0
    property int expired: 0
    property bool retiring: false
    property bool accepted: false
    property string text: ""

    function failed(response) {
        const test = scenario.harness;
        test.check(scenario.retiring && response.error === "retired request channel", "restart did not fail pending work synchronously");
        test.check(Object.keys(test.channel.pending).length === 0, "restart retained pending callbacks");
        scenario.failures += 1;
    }

    function retire() {
        const test = scenario.harness;
        scenario.retiring = true;
        test.channel.restart("retired request channel");
        test.check(!test.channel.connected, "restart left the retired channel ready");
        const expected = test.scenario === "restart" ? 1 : 2;
        test.check(scenario.failures === expected, "restart missed or repeated pending callbacks");
    }

    function connected() {
        const test = scenario.harness;
        const channel = test.channel;
        if (!channel.connected)
            return;
        scenario.connections += 1;
        if (scenario.connections === 1) {
            test.check(channel.call({method: "ask.run", args: ["provider", "old"]}, response => {
                if (response.timeout) {
                    test.check(test.scenario === "restart", "buffered restart event was not delivered");
                    scenario.expired += 1;
                    scenario.retire();
                } else {
                    scenario.failed(response);
                }
            }), "old ask refused");
            test.check(channel.call({method: "status"}, response => scenario.failed(response)), "pending status refused");
        } else if (scenario.connections === 2) {
            test.check(scenario.retiring, "channel reconnected without the requested retirement");
            test.check(scenario.expired === (test.scenario === "restart" ? 1 : 0), "timeout callback count changed");
            test.check(channel.call({method: "ask.run", args: ["provider", "new"]}, response => {
                test.check(response.ok === "asking", "replacement socket did not accept the new ask");
                scenario.accepted = true;
            }), "replacement socket refused a new request");
        } else {
            test.check(false, "unexpected additional reconnect");
        }
    }

    function receive(message) {
        const test = scenario.harness;
        const event = message.event;
        if (test.scenario === "retired-stream" && event.text === "retire" && !scenario.retiring) {
            scenario.retire();
            return;
        }
        if (!test.check(scenario.connections === 2 && scenario.accepted, "old stream reached the replacement channel"))
            return;
        test.check(event.kind === "ask.text" && event.error === undefined, "replacement stream failed");
        if (event.text)
            scenario.text += event.text;
        if (event.done) {
            test.check(scenario.text === "fresh", "retired stream contaminated the new answer");
            test.finish();
        }
    }
}
