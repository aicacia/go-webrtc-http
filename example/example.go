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

	webrtcHttp "github.com/aicacia/go-webrtc-http"
	"github.com/gorilla/websocket"
	"github.com/pion/webrtc/v4"
)

var config webrtc.Configuration = webrtc.Configuration{
	ICEServers: []webrtc.ICEServer{
		{
			URLs: []string{"stun:stun.l.google.com:19302"},
		},
	},
}

func InitClient() {
	token, err := authenticate("client", "http://localhost:4000", "test")
	if err != nil {
		log.Fatal(err)
	}
	conn, _, err := websocket.DefaultDialer.Dial(fmt.Sprintf("ws://localhost:4000/client/websocket?token=%s", url.QueryEscape(token)), nil)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("connecting to server")
	ready := make(chan bool, 1)
	peerConnection, err := webrtc.NewPeerConnection(config)
	if err != nil {
		log.Fatal(err)
	}
	peerConnection.OnConnectionStateChange(func(pcs webrtc.PeerConnectionState) {
		if pcs == webrtc.PeerConnectionStateClosed {
			peerConnection.Close()
			peerConnection = nil
		}
	})
	pendingCandidates := make([]*webrtc.ICECandidate, 0)
	peerConnection.OnICECandidate(func(pendingCandidate *webrtc.ICECandidate) {
		if pendingCandidate == nil {
			return
		}
		remoteDescription := peerConnection.RemoteDescription()
		if remoteDescription == nil {
			pendingCandidates = append(pendingCandidates, pendingCandidate)
		} else {
			err := conn.WriteJSON(map[string]interface{}{
				"type":      "candidate",
				"candidate": pendingCandidate.ToJSON().Candidate,
			})
			if err != nil {
				log.Fatal(err)
			}
		}
	})
	channel, err := peerConnection.CreateDataChannel("webrtc-http-data-channel", nil)
	if err != nil {
		log.Fatal(err)
	}
	channel.OnOpen(func() {
		ready <- true
	})
	offer, err := peerConnection.CreateOffer(nil)
	if err != nil {
		log.Fatal(err)
	}
	if err = peerConnection.SetLocalDescription(offer); err != nil {
		log.Fatal(err)
	}
	err = conn.WriteJSON(offer)
	if err != nil {
		log.Fatal(err)
	}
	reader := createJSONReader[map[string]interface{}](conn)
Loop:
	for {
		select {
		case <-ready:
			break Loop
		case msg, ok := <-reader:
			if !ok {
				log.Fatal("connection closed")
				break
			}
			if peerConnection != nil {
				if kind, ok := msg["type"].(string); ok {
					switch kind {
					case "answer":
						log.Printf("received answer")
						err := peerConnection.SetRemoteDescription(webrtc.SessionDescription{
							Type: webrtc.SDPTypeAnswer,
							SDP:  msg["sdp"].(string),
						})
						if err != nil {
							log.Fatal(err)
						}
						for _, pendingCandidate := range pendingCandidates {
							err := conn.WriteJSON(map[string]interface{}{
								"type": "candidate",
								"candidate": map[string]interface{}{
									"candidate": pendingCandidate.ToJSON().Candidate,
								},
							})
							if err != nil {
								log.Fatal(err)
							}
						}
					}
				}
			}
		}
	}
	http.DefaultTransport = webrtcHttp.NewRoundTripper(channel)
	resp, err := http.Post("/test", "text/plain", bytes.NewBufferString("Hello, world!"))
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
	token, err := authenticate("server", "http://localhost:4000", "test")
	if err != nil {
		log.Fatal(err)
	}
	conn, _, err := websocket.DefaultDialer.Dial(fmt.Sprintf("ws://localhost:4000/server/websocket?token=%s", url.QueryEscape(token)), nil)
	if err != nil {
		log.Fatal(err)
	}
	peerConnections := make(map[string]*webrtc.PeerConnection)
	channels := make(map[string]*webrtc.DataChannel)
	servers := make(map[string]*webrtcHttp.WebRTCServerST)
	log.Printf("listening for peers")
	for {
		var msg map[string]interface{}
		err := conn.ReadJSON(&msg)
		if err != nil {
			log.Println(err)
			token, err := authenticate("server", "http://localhost:4000", "test")
			if err != nil {
				log.Fatal(err)
			}
			conn, _, err = websocket.DefaultDialer.Dial(fmt.Sprintf("ws://localhost:4000/server/websocket?token=%s", url.QueryEscape(token)), nil)
			if err != nil {
				log.Fatal(err)
			}
			continue
		}
		switch msg["type"].(string) {
		case "join":
			log.Printf("%s: join", msg["from"])
			peerConnection, err := webrtc.NewPeerConnection(config)
			if err != nil {
				log.Fatal(err)
			}
			from := msg["from"].(string)
			peerConnections[from] = peerConnection
			peerConnection.OnDataChannel(func(channel *webrtc.DataChannel) {
				log.Printf("New DataChannel %s %d\n", channel.Label(), channel.ID())

				channel.OnOpen(func() {
					channels[from] = channel
					servers[from] = webrtcHttp.NewServer(channel, func(w http.ResponseWriter, r *http.Request) {
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
				})
			})
			peerConnection.OnConnectionStateChange(func(pcs webrtc.PeerConnectionState) {
				if pcs == webrtc.PeerConnectionStateClosed {
					peerConnection.Close()
					delete(peerConnections, from)
					delete(channels, from)
					delete(servers, from)
				}
			})
		case "leave":
			log.Printf("%s: left", msg["from"])
		case "message":
			if peerConnection, ok := peerConnections[msg["from"].(string)]; ok {
				if kind, ok := msg["payload"].(map[string]interface{})["type"].(string); ok {
					switch kind {
					case "offer":
						log.Printf("received offer from %s", msg["from"])
						err := peerConnection.SetRemoteDescription(webrtc.SessionDescription{
							Type: webrtc.SDPTypeOffer,
							SDP:  msg["payload"].(map[string]interface{})["sdp"].(string),
						})
						if err != nil {
							log.Fatal(err)
						}
						answer, err := peerConnection.CreateAnswer(nil)
						if err != nil {
							log.Fatal(err)
						}
						gatherComplete := webrtc.GatheringCompletePromise(peerConnection)
						err = peerConnection.SetLocalDescription(answer)
						if err != nil {
							log.Fatal(err)
						}
						<-gatherComplete

						peerAnswer := peerConnection.LocalDescription()
						err = conn.WriteJSON(map[string]interface{}{
							"to":      msg["from"],
							"payload": peerAnswer,
						})
						if err != nil {
							log.Fatal(err)
						}
					case "candidate":
						log.Printf("received candidate from %s", msg["from"])
						candidate, ok := msg["payload"].(map[string]interface{})["candidate"].(map[string]interface{})["candidate"].(string)
						if !ok {
							log.Fatal("invalid candidate")
						}
						if err := peerConnection.AddICECandidate(webrtc.ICECandidateInit{Candidate: candidate}); err != nil {
							log.Fatal(err)
						}
					}
				}
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

func authenticate(kind string, url, secret string) (string, error) {
	requestBody, err := json.Marshal(map[string]string{
		"id":       "webrtc-example",
		"password": secret,
	})
	if err != nil {
		return "", err
	}

	resp, err := http.Post(url+"/"+kind, "application/json", bytes.NewBuffer(requestBody))
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
