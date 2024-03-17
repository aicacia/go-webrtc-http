package main

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

type P2PMessageST struct {
	Type    string                 `json:"type" validate:"required"`
	From    string                 `json:"from" validate:"required"`
	Payload map[string]interface{} `json:"payload"`
}

func main() {
	token, err := authenticateServer("http://localhost:4000", "test")
	if err != nil {
		panic(err)
	}
	conn, _, err := websocket.DefaultDialer.Dial(fmt.Sprintf("ws://localhost:4000/server/websocket?token=%s", url.QueryEscape(token)), nil)
	if err != nil {
		panic(err)
	}
	peerConnections := make(map[string]*webrtc.PeerConnection)
	channels := make(map[string]*webrtc.DataChannel)
	servers := make(map[string]*webrtcHttp.WebRTCServerST)
	config := webrtc.Configuration{
		ICEServers: []webrtc.ICEServer{
			{
				URLs: []string{"stun:stun.l.google.com:19302"},
			},
		},
	}
	for {
		var msg P2PMessageST
		err := conn.ReadJSON(&msg)
		if err != nil {
			log.Println(err)
			token, err := authenticateServer("http://localhost:4000", "test")
			if err != nil {
				panic(err)
			}
			conn, _, err = websocket.DefaultDialer.Dial(fmt.Sprintf("ws://localhost:4000/server/websocket?token=%s", url.QueryEscape(token)), nil)
			if err != nil {
				panic(err)
			}
			continue
		}
		switch msg.Type {
		case "join":
			peerConnection, err := webrtc.NewPeerConnection(config)
			if err != nil {
				panic(err)
			}
			peerConnections[msg.From] = peerConnection
			peerConnection.OnDataChannel(func(channel *webrtc.DataChannel) {
				channels[msg.From] = channel
				servers[msg.From] = webrtcHttp.NewServer(channel, func(w http.ResponseWriter, r *http.Request) {
					_, err := io.Copy(w, r.Body)
					if err != nil {
						fmt.Println(err)
					}
					w.WriteHeader(200)
				})
			})
			peerConnection.OnConnectionStateChange(func(pcs webrtc.PeerConnectionState) {
				if pcs == webrtc.PeerConnectionStateFailed {
					peerConnection.Close()
					delete(peerConnections, msg.From)
					delete(channels, msg.From)
					delete(servers, msg.From)
				}
			})
		case "leave":
			log.Printf("%s: left", msg.From)
		case "message":
			if peerConnection, ok := peerConnections[msg.From]; ok {
				if kind, ok := msg.Payload["type"]; ok {
					switch kind {
					case "offer":
						peerConnection.SetRemoteDescription(webrtc.SessionDescription{
							Type: webrtc.SDPTypeOffer,
							SDP:  msg.Payload["sdp"].(string),
						})
						answer, err := peerConnection.CreateAnswer(nil)
						if err != nil {
							panic(err)
						}
						gatherComplete := webrtc.GatheringCompletePromise(peerConnection)
						err = peerConnection.SetLocalDescription(answer)
						if err != nil {
							panic(err)
						}
						<-gatherComplete

						peerAnswer := peerConnection.LocalDescription()
						conn.WriteJSON(struct {
							To      string                     `json:"to" validate:"required"`
							Payload *webrtc.SessionDescription `json:"payload" validate:"required"`
						}{
							To:      msg.From,
							Payload: peerAnswer,
						})
					case "candidate":
						candidateRaw, ok := msg.Payload["candidate"].(map[string]interface{})
						if !ok {
							panic("invalid candidate")
						}
						candidate, ok := candidateRaw["candidate"].(string)
						if !ok {
							panic("invalid candidate")
						}
						if err := peerConnection.AddICECandidate(webrtc.ICECandidateInit{Candidate: candidate}); err != nil {
							panic(err)
						}
					}
				}
			}
		}
	}
}

func authenticateServer(url, secret string) (string, error) {
	requestBody, err := json.Marshal(map[string]string{
		"id":       "webrtc-example",
		"password": secret,
	})
	if err != nil {
		return "", err
	}

	resp, err := http.Post(url+"/server", "application/json", bytes.NewBuffer(requestBody))
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
