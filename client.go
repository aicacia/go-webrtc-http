package webrtc_http

import (
	"encoding/binary"
	"fmt"
	"io"
	"log"
	http "net/http"
	"strconv"

	"github.com/aicacia/go-cmap"
	rao "github.com/aicacia/go-result-and-option"
	webrtc "github.com/pion/webrtc/v4"
)

type webrtcClientConnectionST struct {
	connectionId      uint32
	connectionIdBytes []byte
	readStatus        bool
	readHeaders       bool
	writer            io.WriteCloser
	response          *http.Response
	ch                chan rao.Result[*http.Response]
}

func createWebRTCClientConnection(connectionId uint32, request *http.Request) *webrtcClientConnectionST {
	connectionIdBytes := make([]byte, 4)
	binary.BigEndian.PutUint32(connectionIdBytes, connectionId)
	pr, pw := io.Pipe()
	return &webrtcClientConnectionST{
		connectionId:      connectionId,
		connectionIdBytes: connectionIdBytes,
		writer:            pw,
		response: &http.Response{
			Proto:         request.Proto,
			ProtoMajor:    request.ProtoMajor,
			ProtoMinor:    request.ProtoMinor,
			Request:       request,
			Body:          pr,
			ContentLength: 0,
			Header:        http.Header{},
		},
		ch: make(chan rao.Result[*http.Response], 1),
	}
}

type WebRTCRoundTripperST struct {
	connections cmap.CMap[uint32, *webrtcClientConnectionST]
	channel     *webrtc.DataChannel
	ProtoMajor  int
	ProtoMinor  int
}

func NewRoundTripper(channel *webrtc.DataChannel) *WebRTCRoundTripperST {
	rt := &WebRTCRoundTripperST{
		connections: cmap.New[uint32, *webrtcClientConnectionST](),
		channel:     channel,
		ProtoMajor:  PROTOCAL_MAJOR,
		ProtoMinor:  PROTOCAL_MINOR,
	}

	onConnectionLine := func(connectionId uint32, line []byte) error {
		if connection, ok := rt.connections.Get(connectionId); ok {
			if !connection.readStatus {
				connection.readStatus = true
				parts := reSpaces.Split(string(line), 3)
				if len(parts) == 3 {
					if status, err := strconv.Atoi(parts[1]); err != nil {
						return err
					} else {
						connection.response.StatusCode = status
					}
					connection.response.Status = parts[2]
				}
			} else if !connection.readHeaders {
				if line[0] == r && line[1] == n {
					connection.readHeaders = true
					connection.ch <- rao.Ok(connection.response)
					close(connection.ch)
				} else {
					header := reHeader.Split(string(line), 2)
					if len(header) == 2 {
						key := header[0]
						value := header[1]
						connection.response.Header.Add(key, value)
					}
				}
			} else {
				if line[0] == r && line[1] == n {
					rt.deleteConnection(connection.connectionId)
				} else {
					connection.writer.Write(line)
				}
			}
		}
		return nil
	}

	onMessage := func(msg webrtc.DataChannelMessage) {
		connectionId := binary.BigEndian.Uint32(msg.Data[0:4])
		err := onConnectionLine(connectionId, msg.Data[4:])
		if err != nil {
			log.Printf("error handling message: %v\n", err)
		}
	}

	channel.OnMessage(onMessage)

	return rt
}

func (rt *WebRTCRoundTripperST) RoundTrip(request *http.Request) (*http.Response, error) {
	ctx := request.Context()
	connection := rt.createConnection(request)
	err := rt.writeRequest(connection, request)
	if err != nil {
		return nil, err
	}
	select {
	case <-ctx.Done():
		rt.deleteConnection(connection.connectionId)
		return nil, ctx.Err()
	case result := <-connection.ch:
		if response, ok := result.Ok(); ok {
			return response, nil
		} else if err, ok := result.Err(); ok {
			return nil, err
		} else {
			return nil, fmt.Errorf("unknown error")
		}
	}
}

func (rt *WebRTCRoundTripperST) createConnection(request *http.Request) *webrtcClientConnectionST {
	connectionId := randUint32()
	for {
		if !rt.connections.Has(connectionId) {
			break
		}
		connectionId = randUint32()
	}
	connection := createWebRTCClientConnection(connectionId, request)
	rt.connections.Set(connectionId, connection)
	return connection
}

func (rt *WebRTCRoundTripperST) deleteConnection(connectionId uint32) {
	if connection, ok := rt.connections.Get(connectionId); ok {
		connection.writer.Close()
	}
	rt.connections.Delete(connectionId)
}

func (rt *WebRTCRoundTripperST) writeRequest(connection *webrtcClientConnectionST, request *http.Request) error {
	err := rt.channel.Send(encodeLine(
		connection.connectionIdBytes,
		fmt.Sprintf("%s %s %s",
			request.Method,
			request.URL.RequestURI(),
			fmt.Sprintf("%s/%d.%d", PROTOCAL_NAME, rt.ProtoMajor, rt.ProtoMinor))))
	if err != nil {
		rt.deleteConnection(connection.connectionId)
		return err
	}
	for name, values := range request.Header {
		for _, value := range values {
			err := rt.channel.Send(encodeLine(connection.connectionIdBytes, fmt.Sprintf("%s: %s", name, value)))
			if err != nil {
				rt.deleteConnection(connection.connectionId)
				return err
			}
		}
	}
	err = rt.channel.Send(encodeLineBytes(connection.connectionIdBytes, rn))
	if err != nil {
		rt.deleteConnection(connection.connectionId)
		return err
	}
	if request.Body != nil {
		maxMessageSize := int(rt.channel.Transport().GetCapabilities().MaxMessageSize)
		if maxMessageSize <= 4 {
			maxMessageSize = 16384
		}
		// save room for id
		maxMessageSize -= 4
		buf := make([]byte, maxMessageSize)
		for {
			n, err := request.Body.Read(buf)
			if err != nil {
				if err == io.EOF {
					break
				}
				rt.deleteConnection(connection.connectionId)
				return err
			}
			err = rt.channel.Send(encodeLineBytes(connection.connectionIdBytes, buf[:n]))
			if err != nil {
				rt.deleteConnection(connection.connectionId)
				return err
			}
		}
	}
	err = rt.channel.Send(encodeLineBytes(connection.connectionIdBytes, rn))
	if err != nil {
		rt.deleteConnection(connection.connectionId)
		return err
	}
	return nil
}
