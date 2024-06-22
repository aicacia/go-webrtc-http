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
	"sync"

	"github.com/aicacia/go-simplepeer"
	webrtcHttp "github.com/aicacia/go-webrtc-http"
	"github.com/gorilla/websocket"
	"github.com/pion/webrtc/v4"
)

var (
	TOKEN       string
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

	token, err := authenticate("client", "test")
	if err != nil {
		log.Fatal(err)
	}
	conn, _, err := websocket.DefaultDialer.Dial(fmt.Sprintf(P2P_WS_URL+"/client/websocket?token=%s", url.QueryEscape(token)), nil)
	if err != nil {
		log.Fatal(err)
	}
	defer conn.Close()
	log.Printf("connecting to server")

	peerConnect := make(chan struct{}, 1)
	peerClose := make(chan struct{}, 1)
	peer := simplepeer.NewPeer(simplepeer.PeerOptions{
		Config: &config,
		OnSignal: func(message simplepeer.SignalMessage) error {
			return conn.WriteJSON(message)
		},
		OnConnect: func() {
			peerConnect <- struct{}{}
		},
		OnClose: func() {
			peerClose <- struct{}{}
		},
	})
	err = peer.Init()
	if err != nil {
		log.Println(err)
	}
	reader := createJSONReader[map[string]interface{}](conn)
ReadLoop:
	for {
		select {
		case <-peerConnect:
			break ReadLoop
		case <-peerClose:
			log.Fatal("connection closed")
			break ReadLoop
		case msg, ok := <-reader:
			if !ok {
				log.Fatal("connection closed")
				break
			}
			err := peer.Signal(msg)
			if err != nil {
				log.Fatal(err)
			}
		}
	}

	client := &http.Client{
		Transport: webrtcHttp.NewRoundTripper(peer.Channel()),
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
}

func InitServer() {
	TOKEN = os.Getenv("JWT_TOKEN")
	P2P_API_URL = os.Getenv("P2P_API_URL")
	P2P_WS_URL = os.Getenv("P2P_WS_URL")

	token, err := authenticate("server", "test")
	if err != nil {
		log.Fatal(err)
	}
	var connRW sync.RWMutex
	conn, _, err := websocket.DefaultDialer.Dial(fmt.Sprintf(P2P_WS_URL+"/server/websocket?token=%s", url.QueryEscape(token)), nil)
	if err != nil {
		log.Fatal(err)
	}
	peers := make(map[string]*simplepeer.Peer)
	servers := make(map[string]*webrtcHttp.WebRTCServerST)
	log.Printf("listening for peers")
	for {
		var msg map[string]interface{}
		connRW.RLock()
		err := conn.ReadJSON(&msg)
		connRW.RUnlock()
		if err != nil {
			log.Println(err)
			token, err := authenticate("server", "test")
			if err != nil {
				log.Fatal(err)
			}
			conn, _, err = websocket.DefaultDialer.Dial(fmt.Sprintf(P2P_WS_URL+"/server/websocket?token=%s", url.QueryEscape(token)), nil)
			if err != nil {
				log.Fatal(err)
			}
			continue
		}
		switch msg["type"].(string) {
		case "join":
			from := msg["from"].(string)
			log.Printf("%s: join", from)
			var peer *simplepeer.Peer
			peer = simplepeer.NewPeer(simplepeer.PeerOptions{
				Config: &config,
				OnSignal: func(message simplepeer.SignalMessage) error {
					connRW.Lock()
					defer connRW.Unlock()
					return conn.WriteJSON(map[string]interface{}{
						"to":      from,
						"payload": message,
					})
				},
				OnConnect: func() {
					servers[from] = webrtcHttp.NewServer(peer.Channel(), func(w http.ResponseWriter, r *http.Request) {
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
			peers[from] = peer
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

func createJSONReader[T any](c *websocket.Conn) chan T {
	out := make(chan T)
	go func() {
		defer recover()
		for {
			var v T
			if err := c.ReadJSON(&v); err != nil {
				return
			}
			out <- v
		}
	}()
	return out
}

func authenticate(kind string, secret string) (string, error) {
	requestBody := map[string]string{
		"password": secret,
	}
	if kind == "client" {
		requestBody["id"] = "webrtc-example"
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
		req.Header.Set("Authorization", "Bearer "+TOKEN)
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
