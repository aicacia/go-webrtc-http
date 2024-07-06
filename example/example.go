package example

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
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
		log.Fatal(err)
	}
	payloadBytes, err := json.Marshal(map[string]interface{}{
		"name": "test",
	})
	if err != nil {
		log.Fatal(err)
	}
	conn, err := websocket.Dial(fmt.Sprintf(P2P_WS_URL+"/client/websocket?token=%s&payload=%s", url.QueryEscape(token), url.QueryEscape(string(payloadBytes))), "", P2P_WS_URL)
	if err != nil {
		log.Fatal(err)
	}
	defer conn.Close()
	log.Printf("connecting to server")

	peerConnect := make(chan struct{}, 1)
	peerClose := make(chan struct{}, 1)
	p := peer.NewPeer(peer.PeerOptions{
		Config: &config,
		OnSignal: func(message map[string]interface{}) error {
			return websocket.JSON.Send(conn, message)
		},
		OnConnect: func() {
			peerConnect <- struct{}{}
		},
		OnClose: func() {
			peerClose <- struct{}{}
		},
	})
	err = p.Init()
	if err != nil {
		log.Println(err)
	}
	receiver := make(chan map[string]interface{})
	go func() {
		defer recover()
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
			close(receiver)
			break ReadLoop
		case <-peerClose:
			log.Fatal("connection closed")
			close(receiver)
			break ReadLoop
		case msg, ok := <-receiver:
			if !ok {
				log.Fatal("connection closed")
				break
			}
			err := p.Signal(msg)
			if err != nil {
				log.Fatal(err)
			}
		}
	}

	client := &http.Client{
		Transport: webrtchttp.NewRoundTripper(p.Channel()),
	}
	resp, err := client.Post("/test", "text/plain", bytes.NewBufferString("Hello, world!"))
	if err != nil {
		log.Fatalf("error in http call: %v", err)
	}
	bytes, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Fatalf("error reading body: %v", err)
	}
	for key, values := range resp.Header {
		for _, value := range values {
			log.Printf("%s: %s", key, value)
		}
	}
	log.Printf("response: %s", string(bytes))
	err = p.Close()
	if err != nil {
		log.Fatal(err)
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
		log.Fatal(err)
	}
	peers := make(map[string]*peer.Peer)
	servers := make(map[string]*webrtchttp.WebRTCServerST)
	log.Printf("listening for peers")
	for {
		var msg map[string]interface{}
		err := websocket.JSON.Receive(conn, &msg)
		if err != nil {
			log.Fatal(err)
		}
		switch msg["type"].(string) {
		case "join":
			from := msg["from"].(string)
			log.Printf("%s: join %v", from, msg["payload"])
			var p *peer.Peer
			p = peer.NewPeer(peer.PeerOptions{
				Config: &config,
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
						_, err := io.Copy(w, r.Body)
						if err != nil {
							fmt.Println(err)
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
			log.Printf("%s: left", msg["from"])
		case "message":
			if peer, ok := peers[msg["from"].(string)]; ok {
				if payload, ok := msg["payload"].(map[string]interface{}); ok {
					err := peer.Signal(payload)
					if err != nil {
						log.Println(err)
					}
				} else {
					log.Printf("%s: invalid payload", msg["from"])
				}
			} else {
				log.Printf("%s: peer not found", msg["from"])
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
