pragma ComponentBehavior: Bound

import QtQuick

QtObject {
    id: scenario
    required property var harness
    property var replies: []
    property string events: ""

    function receive(message) {
        scenario.harness.check(message.event.kind === "ask.text", "event kind changed");
        scenario.events += message.event.text;
    }

    function answered(name, response, expected) {
        const test = scenario.harness;
        if (!test.check(response.error === undefined && JSON.stringify(response.ok) === JSON.stringify(expected), name + " got another request's reply"))
            return;
        if (!test.check(scenario.replies.indexOf(name) === -1, name + " completed twice"))
            return;
        scenario.replies.push(name);
        if (scenario.replies.length === 3) {
            test.check(scenario.replies.join(",") === "shown,mode,clip", "replies were not completed out of order");
            test.check(scenario.events === "onetwo", "interleaved events were lost or consumed as replies");
            test.finish();
        }
    }

    function start() {
        const test = scenario.harness;
        const channel = test.channel;
        test.check(!channel.legacy, "ID-capable peer was treated as legacy");
        test.check(channel.call({method: "clip.history", args: ["7"]}, response => scenario.answered("clip", response, ["copied"])), "clip refused");
        test.check(channel.call({method: "shown", args: ["surface"]}, response => scenario.answered("shown", response, "thanks")), "shown refused behind clip");
        test.check(channel.call({method: "attn.mode"}, response => scenario.answered("mode", response, {mode: "quiet"})), "mode refused behind clip");
        test.check(Object.keys(channel.pending).length === 3, "modern requests did not overlap");
    }
}
