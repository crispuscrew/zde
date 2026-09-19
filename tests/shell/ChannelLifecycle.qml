pragma ComponentBehavior: Bound

import QtQuick

QtObject {
    id: scenario
    required property var harness
    property int failures: 0
    property int overflow: 0
    property int expired: 0
    property var failedRequests: ({})

    function disconnect() {
        const test = scenario.harness;
        const channel = test.channel;
        test.check(channel.pendingMax === 128, "pending bound changed; update both sides of the test");
        for (let index = 0; index < channel.pendingMax; ++index) {
            test.check(channel.call({method: "status", args: [String(index)]}, response => {
                test.check(!scenario.failedRequests[index], "disconnect repeated a callback");
                scenario.failedRequests[index] = true;
                test.check(typeof response.error === "string" && response.error.indexOf("lost") >= 0, "disconnect did not fail pending callback");
                test.check(Object.keys(channel.pending).length === 0, "disconnect retained the pending map during callbacks");
                test.check(channel.acknowledgements.length === 0, "disconnect retained queued public tokens");
                scenario.failures += 1;
                if (scenario.failures === channel.pendingMax) {
                    test.check(Object.keys(scenario.failedRequests).length === channel.pendingMax, "disconnect missed a callback");
                    test.check(scenario.overflow === 1, "pending limit did not refuse exactly once");
                    Qt.callLater(() => {
                        test.check(!channel.connected, "disconnect left channel ready");
                        test.finish();
                    });
                }
            }), "request below the pending limit was refused");
        }
        test.check(Object.keys(channel.pending).length === channel.pendingMax, "pending map exceeded or missed its limit");
        for (let index = 0; index < channel.pendingMax; ++index)
            test.check(channel.acknowledge("surface-" + index), "public token below the queue limit was refused");
        test.check(channel.acknowledgements.length === channel.pendingMax, "public tokens were not queued behind pending work");
        test.check(!channel.acknowledge("overflow"), "acknowledgement queue exceeded its bound");
        test.check(!channel.call({method: "status"}, response => {
            test.check(typeof response.error === "string" && response.error.indexOf("busy") >= 0, "overflow did not report busy");
            scenario.overflow += 1;
        }), "request above the pending limit was accepted");
    }

    function timeout() {
        const test = scenario.harness;
        const channel = test.channel;
        test.check(channel.call({method: "clip.history", args: ["old"]}, response => {
            test.check(response.timeout === true, "local timeout was not tagged for stream retirement");
            test.check(typeof response.error === "string" && response.error.indexOf("in time") >= 0, "request did not expire on the real timer");
            scenario.expired += 1;
            test.check(scenario.expired === 1 && Object.keys(channel.pending).length === 0, "timeout retained or repeated its callback");
            test.check(channel.call({method: "attn.mode"}, current => {
                test.check(current.ok !== undefined && current.ok.mode === "work", "late reply completed the newer request");
                test.check(scenario.expired === 1, "late reply completed the expired request twice");
                test.finish();
            }), "timeout prevented a new request");
        }), "timeout request refused");
    }
}
