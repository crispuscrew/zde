pragma ComponentBehavior: Bound

import QtQuick

QtObject {
    id: scenario
    required property var harness
    property int refused: 0
    property int completed: 0

    function ordinaryCallIsBusy() {
        const test = scenario.harness;
        test.check(!test.channel.call({method: "status"}, response => {
            test.check(typeof response.error === "string" && response.error.indexOf("busy") >= 0, "ordinary overlap was not refused as busy");
            scenario.refused += 1;
        }), "ordinary call bypassed legacy single-flight");
    }

    function start() {
        const test = scenario.harness;
        const channel = test.channel;
        test.check(channel.legacy, "ID-less probe did not select legacy mode");
        test.check(channel.call({method: "clip.history", args: ["7"]}, response => {
            test.check(response.ok === "legacy clip", "shown consumed the clipboard reply");
            scenario.completed += 1;
            // Flushing before the callback reserves the next slot for shown.
            scenario.ordinaryCallIsBusy();
            test.check(Object.keys(channel.pending).length === 1, "shown did not get the next slot");
        }), "clipboard request refused");
        test.check(channel.acknowledge("first-surface"), "first public token was dropped");
        test.check(channel.acknowledge("second-surface"), "second public token was dropped");
        test.check(channel.acknowledgements.length === 2, "public tokens did not wait for the clipboard reply");
        scenario.ordinaryCallIsBusy();
    }

    function receive(message) {
        const test = scenario.harness;
        const channel = test.channel;
        if (!test.check(message.event.text === "acks-drained", "unexpected peer event"))
            return;
        test.check(scenario.completed === 1 && scenario.refused === 2, "callbacks were lost or repeated");
        test.check(channel.acknowledgements.length === 0 && Object.keys(channel.pending).length === 0, "shown did not finish before the next call");
        test.check(channel.call({method: "attn.mode"}, response => {
            test.check(response.ok !== undefined && response.ok.mode === "work", "ID-less reply ownership drifted after shown");
            test.finish();
        }), "ordinary call stayed busy after acknowledgements completed");
    }
}
