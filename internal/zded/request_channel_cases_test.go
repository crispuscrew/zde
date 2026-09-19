package zded

import "fmt"

func (peer *channelPeer) modern() error {
	clip, err := peer.request("clip.history")
	if err != nil {
		return err
	}
	shown, err := peer.request("shown")
	if err != nil {
		return err
	}
	mode, err := peer.request("attn.mode")
	if err != nil {
		return err
	}
	for _, send := range []func() error{
		func() error { return peer.reply("unknown-id", "must be ignored") },
		func() error { return peer.event("one") },
		func() error { return peer.reply(shown.ID, "thanks") },
		func() error { return peer.reply(shown.ID, "duplicate must be ignored") },
		func() error { return peer.reply(mode.ID, map[string]string{"mode": "quiet"}) },
		func() error { return peer.event("two") },
		func() error { return peer.reply(clip.ID, []string{"copied"}) },
	} {
		if err := send(); err != nil {
			return err
		}
	}
	return nil
}

func (peer *channelPeer) legacy() error {
	if _, err := peer.request("clip.history"); err != nil {
		return err
	}
	if err := peer.reply("", "legacy clip"); err != nil {
		return err
	}
	// A busy shown request must have been refused locally, never put on this socket.
	if _, err := peer.request("attn.mode"); err != nil {
		return err
	}
	return peer.reply("", map[string]string{"mode": "work"})
}

func (peer *channelPeer) disconnect() error {
	for index := range 128 {
		request, err := peer.request("status")
		if err != nil {
			return err
		}
		if len(request.Args) != 1 || request.Args[0] != fmt.Sprint(index) {
			return fmt.Errorf("pending request %d = %+v", index, request)
		}
	}
	return peer.conn.Close()
}

func (peer *channelPeer) timeout() error {
	old, err := peer.request("clip.history")
	if err != nil {
		return err
	}
	// Wait for the real five-second Qt timer to expire and its callback to ask again.
	current, err := peer.request("attn.mode")
	if err != nil {
		return err
	}
	if err := peer.reply(old.ID, "late clipboard reply"); err != nil {
		return err
	}
	return peer.reply(current.ID, map[string]string{"mode": "work"})
}
