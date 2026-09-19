package zded

import (
	"context"
	"fmt"
	"io"
	"net"
)

func (peer *channelPeer) restart(ctx context.Context, listener net.Listener, scenario string) error {
	if _, err := peer.request("ask.run"); err != nil {
		return err
	}
	if _, err := peer.request("status"); err != nil {
		return err
	}
	if scenario == "retired-stream" {
		// One write leaves another line in the old parser when the first retires it.
		const batch = "{\"event\":{\"kind\":\"ask.text\",\"text\":\"retire\"}}\n" +
			"{\"event\":{\"kind\":\"ask.text\",\"text\":\"stale\"}}\n"
		if _, err := io.WriteString(peer.conn, batch); err != nil {
			return err
		}
	}
	// In the timeout case, Qt's real timer must trigger the restart and this EOF.
	var extra channelRequest
	if err := peer.decoder.Decode(&extra); err != io.EOF {
		return fmt.Errorf("restart did not close the old socket: %+v, %v", extra, err)
	}
	// Reuse the actual accept/probe path to prove the replacement socket is usable.
	return runChannelPeer(ctx, listener, "recovered")
}

func (peer *channelPeer) recovered() error {
	request, err := peer.request("ask.run")
	if err != nil {
		return err
	}
	if err := peer.reply(request.ID, "asking"); err != nil {
		return err
	}
	if err := peer.event("fresh"); err != nil {
		return err
	}
	return peer.encoder.Encode(map[string]any{"event": map[string]any{"kind": "ask.text", "done": true}})
}
