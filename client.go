package webrtchttp

import (
	"bufio"
	"encoding/binary"
	"errors"
	"io"
	"log/slog"
	http "net/http"

	"github.com/aicacia/go-cmap"
	webrtc "github.com/pion/webrtc/v4"
)

var errInvalidResponse = errors.New("invalid connection message")

type WebRTCRoundTripperST struct {
	maxMessageSize int
	connections    cmap.CMap[uint32, *webrtcClientConnectionST]
	channel        *webrtc.DataChannel
}

func NewRoundTripper(channel *webrtc.DataChannel) *WebRTCRoundTripperST {
	maxMessageSize := int(channel.Transport().GetCapabilities().MaxMessageSize)
	if maxMessageSize <= 4 {
		maxMessageSize = 16384
	}
	// save room for id
	maxMessageSize -= 4

	roundTripper := &WebRTCRoundTripperST{
		maxMessageSize: maxMessageSize,
		connections:    cmap.New[uint32, *webrtcClientConnectionST](),
		channel:        channel,
	}

	onData := func(connectionId uint32, data []byte) error {
		if connection, ok := roundTripper.connections.Get(connectionId); ok {
			if _, err := connection.writer.Write(data); err != nil {
				return err
			}
			return nil
		}
		return errInvalidResponse
	}

	onMessage := func(msg webrtc.DataChannelMessage) {
		connectionId := binary.BigEndian.Uint32(msg.Data[0:4])
		if err := onData(connectionId, msg.Data[4:]); err != nil {
			slog.Debug("error handling message", "error", err)
		}
	}

	channel.OnMessage(onMessage)

	return roundTripper
}

func (roundTripper *WebRTCRoundTripperST) Close() error {
	roundTripper.channel.OnMessage(nil)
	return nil
}

func (roundTripper *WebRTCRoundTripperST) RoundTrip(request *http.Request) (*http.Response, error) {
	connection, err := roundTripper.newConnection()
	if err != nil {
		return nil, err
	}
	defer roundTripper.connections.Delete(connection.id)
	defer connection.writer.Close()
	writer := bufio.NewWriter(newChannelWriter(connection.idBytes, roundTripper.channel, roundTripper.maxMessageSize))
	if err := request.Write(writer); err != nil {
		return nil, err
	}
	if err := writer.Flush(); err != nil {
		return nil, err
	}
	return http.ReadResponse(bufio.NewReader(connection.reader), request)
}

func (roundTripper *WebRTCRoundTripperST) newConnection() (*webrtcClientConnectionST, error) {
	connectionId := randUint32()
	for roundTripper.connections.Has(connectionId) {
		connectionId = randUint32()
	}
	connection := newWebRTCClientConnection(connectionId)
	roundTripper.connections.Set(connectionId, connection)
	return connection, nil
}

type webrtcClientConnectionST struct {
	id      uint32
	idBytes []byte
	reader  io.ReadCloser
	writer  io.WriteCloser
}

func newWebRTCClientConnection(connectionId uint32) *webrtcClientConnectionST {
	idBytes := make([]byte, 4)
	binary.BigEndian.PutUint32(idBytes, connectionId)
	reader, writer := io.Pipe()
	return &webrtcClientConnectionST{
		id:      connectionId,
		idBytes: idBytes,
		reader:  reader,
		writer:  writer,
	}
}
