package webrtchttp

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	http "net/http"

	"github.com/aicacia/go-cmap"
	webrtc "github.com/pion/webrtc/v4"
)

type WebRTCServerST struct {
	channel *webrtc.DataChannel
}

func NewServer(channel *webrtc.DataChannel, handler http.HandlerFunc) *WebRTCServerST {
	connections := cmap.New[uint32, *webrtcServerConnectionST]()

	maxMessageSize := int(channel.Transport().GetCapabilities().MaxMessageSize)
	if maxMessageSize <= 4 {
		maxMessageSize = defaultMaxMessageSize
	}
	// save room for id
	maxMessageSize -= 4

	server := &WebRTCServerST{
		channel: channel,
	}

	handleConnection := func(connection *webrtcServerConnectionST) {
		defer func() {
			connections.Delete(connection.id)
			connection.Close()
		}()
		connectionIdBytes := make([]byte, 4)
		binary.BigEndian.PutUint32(connectionIdBytes, connection.id)
		request, err := http.ReadRequest(bufio.NewReader(connection.reader))
		if err != nil {
			slog.Error("Error reading request", "error", err)
			return
		}
		response := newWebRTCServerResponseST(channel, connection.id, maxMessageSize, request)
		defer response.close()
		handler.ServeHTTP(response, request)
	}

	onData := func(connectionId uint32, data []byte) error {
		connection, ok := connections.Get(connectionId)
		if !ok {
			connection = newWebRTCServerConnection(connectionId)
			go handleConnection(connection)
			connections.Set(connectionId, connection)
		}
		_, err := connection.writer.Write(data)
		return err
	}

	onMessage := func(msg webrtc.DataChannelMessage) {
		connectionId := binary.BigEndian.Uint32(msg.Data[0:4])
		if err := onData(connectionId, msg.Data[4:]); err != nil {
			slog.Error("error handling message", "error", err)
		}
	}

	channel.OnMessage(onMessage)

	return server
}

func (server *WebRTCServerST) Close() error {
	server.channel.OnMessage(nil)
	return nil
}

type webrtcServerConnectionST struct {
	id     uint32
	reader io.ReadCloser
	writer io.WriteCloser
}

func (connection *webrtcServerConnectionST) Close() {
	var errs []error
	if err := connection.reader.Close(); err != nil {
		errs = append(errs, err)
	}
	if err := connection.writer.Close(); err != nil {
		errs = append(errs, err)
	}
	if err := errors.Join(errs...); err != nil {
		slog.Error("Error closing connection", "error", err)
	}
}

func newWebRTCServerConnection(connectionId uint32) *webrtcServerConnectionST {
	reader, writer := io.Pipe()
	connection := &webrtcServerConnectionST{
		connectionId,
		reader,
		writer,
	}
	return connection
}

type webRTCServerResponseST struct {
	channel           *webrtc.DataChannel
	headersWritten    bool
	statusCodeWritten bool
	proto             string
	statusCode        int
	writer            *bufio.Writer
	headers           http.Header
}

func newWebRTCServerResponseST(channel *webrtc.DataChannel, connectionId uint32, maxMessageSize int, request *http.Request) *webRTCServerResponseST {
	idBytes := make([]byte, 4)
	binary.BigEndian.PutUint32(idBytes, connectionId)
	return &webRTCServerResponseST{
		channel:    channel,
		proto:      request.Proto,
		statusCode: 200,
		writer:     bufio.NewWriter(newChannelWriter(idBytes, channel, maxMessageSize)),
		headers:    http.Header{},
	}
}

func (response *webRTCServerResponseST) Header() http.Header {
	return response.headers
}

func (response *webRTCServerResponseST) Write(bytes []byte) (int, error) {
	if err := response.writeHeaders(); err != nil {
		return 0, err
	}
	return response.writer.Write(bytes)
}

func (response *webRTCServerResponseST) WriteHeader(statusCode int) {
	response.statusCode = statusCode
	response.headers.Set("Status", http.StatusText(statusCode))
}

func (response *webRTCServerResponseST) writeStatus() error {
	if response.statusCodeWritten {
		return nil
	}
	response.statusCodeWritten = true
	_, err := response.writer.Write([]byte(fmt.Sprintf("%s %d %s\r\n", response.proto, response.statusCode, statusCodeToStatusText(response.statusCode))))
	return err
}

func (response *webRTCServerResponseST) writeHeaders() error {
	if response.headersWritten {
		return nil
	}
	if err := response.writeStatus(); err != nil {
		return err
	}
	response.headersWritten = true
	for k, v := range response.headers {
		for _, value := range v {
			if _, err := response.writer.Write([]byte(fmt.Sprintf("%s: %s\r\n", k, value))); err != nil {
				return err
			}
		}
	}
	_, err := response.writer.Write([]byte("\r\n"))
	return err
}

func (response *webRTCServerResponseST) close() {
	if err := response.writeHeaders(); err != nil {
		slog.Error("error writing headers", "error", err)
	}
	if err := response.writer.Flush(); err != nil {
		slog.Error("error flushing writer", "error", err)
	}
}
