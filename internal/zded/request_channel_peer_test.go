package zded

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"strings"
	"time"
)

type channelRequest struct {
	ID     string   `json:"id"`
	Method string   `json:"method"`
	Args   []string `json:"args"`
}

type channelPeer struct {
	conn    net.Conn
	decoder *json.Decoder
	encoder *json.Encoder
	seen    map[string]bool
}

func runChannelPeer(ctx context.Context, listener net.Listener, scenario string) error {
	conn, err := listener.Accept()
	if err != nil {
		return err
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	if err := conn.SetDeadline(time.Now().Add(18 * time.Second)); err != nil {
		return err
	}
	peer := &channelPeer{conn, json.NewDecoder(conn), json.NewEncoder(conn), map[string]bool{}}
	probe, err := peer.request("shown")
	if err != nil {
		return err
	}
	if probe.ID != "probe" || len(probe.Args) != 1 || probe.Args[0] != "" {
		return fmt.Errorf("unexpected protocol probe: %+v", probe)
	}
	identity := probe.ID
	if strings.HasPrefix(scenario, "legacy") {
		identity = ""
	}
	if err := peer.reply(identity, "thanks"); err != nil {
		return err
	}
	switch scenario {
	case "modern":
		err = peer.modern()
	case "legacy":
		err = peer.legacy()
	case "legacy-ack":
		err = peer.legacyAcknowledgements()
	case "disconnect":
		return peer.disconnect()
	case "timeout":
		err = peer.timeout()
	case "restart", "retired-stream":
		return peer.restart(ctx, listener, scenario)
	case "recovered":
		err = peer.recovered()
	default:
		return fmt.Errorf("unknown scenario %q", scenario)
	}
	if err != nil {
		return err
	}
	// Keep the socket alive until the fixture quits, and reject leaked requests.
	var extra channelRequest
	if err := peer.decoder.Decode(&extra); err != io.EOF {
		return fmt.Errorf("expected Qt to close without another request, got %+v: %v", extra, err)
	}
	return nil
}

func (peer *channelPeer) request(method string) (channelRequest, error) {
	var request channelRequest
	if err := peer.decoder.Decode(&request); err != nil {
		return request, err
	}
	if request.Method != method || request.ID == "" || peer.seen[request.ID] {
		return request, fmt.Errorf("want %s with a fresh string ID, got %+v", method, request)
	}
	peer.seen[request.ID] = true
	return request, nil
}

func (peer *channelPeer) reply(identity string, value any) error {
	response := map[string]any{"ok": value}
	if identity != "" {
		response["id"] = identity
	}
	return peer.encoder.Encode(response)
}

func (peer *channelPeer) event(text string) error {
	return peer.encoder.Encode(map[string]any{"event": map[string]string{"kind": "ask.text", "text": text}})
}
