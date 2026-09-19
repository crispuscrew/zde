package zded

import "fmt"

func (peer *channelPeer) legacyAcknowledgements() error {
	if _, err := peer.request("clip.history"); err != nil {
		return err
	}
	if err := peer.reply("", "legacy clip"); err != nil {
		return err
	}
	// Queued public tokens take priority over an ordinary call from that callback.
	for _, token := range []string{"first-surface", "second-surface"} {
		request, err := peer.request("shown")
		if err != nil {
			return err
		}
		if len(request.Args) != 1 || request.Args[0] != token {
			return fmt.Errorf("queued acknowledgement = %+v, want token %q", request, token)
		}
		if err := peer.reply("", "thanks"); err != nil {
			return err
		}
	}
	if err := peer.event("acks-drained"); err != nil {
		return err
	}
	if _, err := peer.request("attn.mode"); err != nil {
		return err
	}
	return peer.reply("", map[string]string{"mode": "work"})
}
