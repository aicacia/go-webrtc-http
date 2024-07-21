package example

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net/http"
	"net/url"
	"os"

	"github.com/aicacia/go-peer"
	"github.com/aicacia/go-webrtchttp"
	"github.com/pion/webrtc/v4"
	"golang.org/x/net/websocket"
)

var (
	JWT_TOKEN   string
	P2P_API_URL string
	P2P_WS_URL  string
)

var config webrtc.Configuration = webrtc.Configuration{
	ICEServers: []webrtc.ICEServer{
		{
			URLs: []string{"stun:stun.l.google.com:19302"},
		},
	},
}

func InitClient() {
	P2P_API_URL = os.Getenv("P2P_API_URL")
	P2P_WS_URL = os.Getenv("P2P_WS_URL")

	token, err := authenticate("client")
	if err != nil {
		slog.Error("error authenticating", "error", err)
		os.Exit(1)
	}
	payloadBytes, err := json.Marshal(map[string]interface{}{
		"name": "test",
	})
	if err != nil {
		slog.Error("error handling payload", "error", err)
		os.Exit(1)
	}
	conn, err := websocket.Dial(fmt.Sprintf(P2P_WS_URL+"/client/websocket?token=%s&payload=%s", url.QueryEscape(token), url.QueryEscape(string(payloadBytes))), "", P2P_WS_URL)
	if err != nil {
		slog.Error("error connecting to websocket", "error", err)
		os.Exit(1)
	}
	defer conn.Close()
	log.Printf("connecting to server")

	peerConnect := make(chan bool, 1)
	peerClose := make(chan bool, 1)
	ordered := true
	p := peer.NewPeer(peer.PeerOptions{
		Config: &config,
		ChannelConfig: &webrtc.DataChannelInit{
			Ordered: &ordered,
		},
		OnSignal: func(message map[string]interface{}) error {
			return websocket.JSON.Send(conn, message)
		},
		OnConnect: func() {
			peerConnect <- true
		},
		OnClose: func() {
			peerClose <- true
		},
	})
	if err = p.Init(); err != nil {
		slog.Error("error initializing peer", "error", err)
		os.Exit(1)
	}
	receiver := make(chan map[string]interface{})
	go func() {
		for {
			var msg map[string]interface{}
			if err := websocket.JSON.Receive(conn, &msg); err != nil {
				break
			} else {
				receiver <- msg
			}
		}
	}()
ReadLoop:
	for {
		select {
		case <-peerConnect:
			break ReadLoop
		case <-peerClose:
			slog.Error("peer closed", "error", err)
			os.Exit(1)
		case msg, ok := <-receiver:
			if !ok {
				slog.Error("websocket closed", "error", err)
				os.Exit(1)
			}
			err := p.Signal(msg)
			if err != nil {
				slog.Error("error signaling peer", "error", err)
				os.Exit(1)
			}
		}
	}

	client := &http.Client{
		Transport: webrtchttp.NewRoundTripper(p.Channel()),
	}
	resp, err := client.Post("/test", "text/plain", bytes.NewBufferString("Hello, world!"))
	if err != nil {
		slog.Error("error in http call", "error", err)
		os.Exit(1)
	}
	defer resp.Body.Close()
	bytes, err := io.ReadAll(resp.Body)
	if err != nil {
		slog.Error("error reading body", "error", err)
		os.Exit(1)
	}
	for key, values := range resp.Header {
		for _, value := range values {
			slog.Info("headers", key, value)
		}
	}
	slog.Info("status", "status", resp.StatusCode)
	slog.Info("response", "body", string(bytes))
	if err := p.Close(); err != nil {
		slog.Error("error closing peer", "error", err)
		os.Exit(1)
	}
}

func InitServer() {
	JWT_TOKEN = os.Getenv("JWT_TOKEN")
	P2P_API_URL = os.Getenv("P2P_API_URL")
	P2P_WS_URL = os.Getenv("P2P_WS_URL")

	token, err := authenticate("server")
	if err != nil {
		log.Fatal(err)
	}

	conn, err := websocket.Dial(fmt.Sprintf(P2P_WS_URL+"/server/websocket?token=%s", url.QueryEscape(token)), "", P2P_WS_URL)
	if err != nil {
		slog.Error("error connecting to websocket", "error", err)
		os.Exit(1)
	}
	peers := make(map[string]*peer.Peer)
	servers := make(map[string]*webrtchttp.WebRTCServerST)
	log.Printf("listening for peers")
	for {
		var msg map[string]interface{}
		err := websocket.JSON.Receive(conn, &msg)
		if err != nil {
			if err == io.EOF {
				continue
			}
			slog.Error("error receiving message", "error", err)
			os.Exit(1)
		}
		switch msg["type"].(string) {
		case "join":
			from := msg["from"].(string)
			slog.Info("join", "peer", from, "payload", msg["payload"])
			var p *peer.Peer
			ordered := true
			p = peer.NewPeer(peer.PeerOptions{
				Config: &config,
				ChannelConfig: &webrtc.DataChannelInit{
					Ordered: &ordered,
				},
				OnSignal: func(message map[string]interface{}) error {
					msg := map[string]interface{}{
						"to":      from,
						"payload": message,
					}
					return websocket.JSON.Send(conn, msg)
				},
				OnConnect: func() {
					servers[from] = webrtchttp.NewServer(p.Channel(), func(w http.ResponseWriter, r *http.Request) {
						for key, values := range r.Header {
							for _, value := range values {
								w.Header().Add(key, value)
							}
						}
						w.WriteHeader(http.StatusAccepted)
						if _, err := io.Copy(w, r.Body); err != nil {
							slog.Error("error copying body", "error", err)
						}
					})
				},
				OnClose: func() {
					delete(peers, from)
					delete(servers, from)
				},
			})
			peers[from] = p
		case "leave":
			slog.Info("left", "peer", msg["from"])
		case "message":
			if peer, ok := peers[msg["from"].(string)]; ok {
				if payload, ok := msg["payload"].(map[string]interface{}); ok {
					if err := peer.Signal(payload); err != nil {
						slog.Error("error signaling peer", "error", err)
					}
				} else {
					slog.Error("invalid payload", "peer", msg["from"])
				}
			} else {
				slog.Error("peer not found", "peer", msg["from"])
			}
		}
	}
}

func authenticate(kind string) (string, error) {
	requestBody := map[string]string{
		"password": "password",
	}
	if kind == "client" {
		requestBody["id"] = "some-globally-unique-id"
	}

	requestBodyBytes, err := json.Marshal(requestBody)
	if err != nil {
		return "", err
	}

	req, err := http.NewRequest("POST", P2P_API_URL+"/"+kind, bytes.NewBuffer(requestBodyBytes))
	if err != nil {
		return "", err
	}
	if kind == "server" {
		req.Header.Set("Authorization", "Bearer "+JWT_TOKEN)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return "", errors.New("failed to authenticate")
	}

	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	return string(responseBody), nil
}
