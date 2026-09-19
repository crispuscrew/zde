pragma ComponentBehavior: Bound

import QtQuick

QtObject {
    id: scenario
    required property var harness
    property int refused: 0
    property int completed: 0

    function start() {
        const test = scenario.harness;
        const channel = test.channel;
        test.check(channel.legacy, "ID-less probe did not select legacy mode");
        test.check(channel.call({method: "clip.history", args: ["7"]}, response => {
            test.check(response.ok === "legacy clip", "legacy clip reply misrouted");
            scenario.completed += 1;
            test.check(scenario.refused === 1, "overlapping legacy call was not refused");
            test.check(channel.call({method: "attn.mode"}, result => {
                test.check(result.ok !== undefined && result.ok.mode === "work", "second legacy reply misrouted");
                scenario.completed += 1;
                test.check(scenario.completed === 2, "legacy callback ran more than once");
                test.finish();
            }), "legacy slot was not released before its callback");
        }), "first legacy request refused");
        test.check(!channel.call({method: "shown", args: ["surface"]}, response => {
            test.check(typeof response.error === "string" && response.error.indexOf("busy") >= 0, "legacy overlap did not explain its refusal");
            scenario.refused += 1;
        }), "legacy peer allowed concurrent requests");
        test.check(scenario.refused === 1 && Object.keys(channel.pending).length === 1, "busy refusal retained a callback");
    }
}
