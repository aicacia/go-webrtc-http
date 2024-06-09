package webrtchttp

import (
	"encoding/binary"
	"fmt"
	"io"
	"log"
	http "net/http"
	"net/url"

	"github.com/aicacia/go-cmap"
	webrtc "github.com/pion/webrtc/v4"
)

type WebRTCServerST struct {
	channel *webrtc.DataChannel
}

type webrtcServerConnectionST struct {
	version     string
	readHeaders bool
	writer      io.WriteCloser
	request     *http.Request
}

func createWebRTCServerConnection(method, path, version string) (*webrtcServerConnectionST, error) {
	url, err := url.Parse("webrtc-http:" + path)
	if err != nil {
		return nil, err
	}
	proto, major, minor, err := parseVersion(version)
	if err != nil {
		return nil, err
	}
	pr, pw := io.Pipe()
	return &webrtcServerConnectionST{
		version:     version,
		readHeaders: false,
		writer:      pw,
		request: &http.Request{
			Proto:      proto,
			ProtoMajor: major,
			ProtoMinor: minor,
			Method:     method,
			URL:        url,
			Body:       pr,
			RequestURI: url.RequestURI(),
			Close:      false,
			Header:     http.Header{},
		},
	}, nil
}

type webRTCServerResponseST struct {
	connectionIdBytes []byte
	channel           *webrtc.DataChannel
	closed            bool
	headersWritten    bool
	statusCodeWritten bool
	version           string
	statusCode        int
	headers           http.Header
}

func (r *webRTCServerResponseST) Header() http.Header {
	return r.headers
}

func (r *webRTCServerResponseST) Write(bytes []byte) (int, error) {
	r.writeHeaders()
	maxMessageSize := int(r.channel.Transport().GetCapabilities().MaxMessageSize)
	if maxMessageSize <= 4 {
		maxMessageSize = 16384
	}
	// save room for id
	maxMessageSize -= 4
	written := 0
	for len(bytes) > 0 {
		bytesCount := len(bytes)
		if bytesCount > maxMessageSize {
			bytesCount = maxMessageSize
		}
		err := r.channel.Send(encodeLineBytes(r.connectionIdBytes, bytes[:bytesCount]))
		if err != nil {
			return written, err
		}
		written += bytesCount
		bytes = bytes[bytesCount:]
	}
	return written, nil
}

func (r *webRTCServerResponseST) WriteHeader(statusCode int) {
	r.statusCode = statusCode
	r.headers.Set("Status", http.StatusText(statusCode))
}

func (r *webRTCServerResponseST) writeStatus() error {
	if r.statusCodeWritten {
		return nil
	}
	r.statusCodeWritten = true
	return r.channel.Send(encodeLine(r.connectionIdBytes, fmt.Sprintf("%s %d %s", r.version, r.statusCode, statusCodeToStatusText(r.statusCode))))
}

func (r *webRTCServerResponseST) writeHeaders() error {
	if r.headersWritten {
		return nil
	}
	err := r.writeStatus()
	if err != nil {
		return err
	}
	r.headersWritten = true
	for k, v := range r.headers {
		for _, value := range v {
			err := r.channel.Send(encodeLine(r.connectionIdBytes, fmt.Sprintf("%s: %s", k, value)))
			if err != nil {
				return err
			}
		}
	}
	return r.channel.Send(encodeLineBytes(r.connectionIdBytes, rn))
}

func (r *webRTCServerResponseST) Close() error {
	if r.closed {
		return nil
	}
	r.closed = true
	err := r.writeHeaders()
	if err != nil {
		return err
	}
	return r.channel.Send(encodeLineBytes(r.connectionIdBytes, rn))
}

func NewServer(channel *webrtc.DataChannel, handler http.HandlerFunc) *WebRTCServerST {
	connections := cmap.New[uint32, *webrtcServerConnectionST]()

	server := &WebRTCServerST{
		channel: channel,
	}

	handle := func(connectionId uint32, connection *webrtcServerConnectionST) {
		connectionIdBytes := make([]byte, 4)
		binary.BigEndian.PutUint32(connectionIdBytes, connectionId)
		response := &webRTCServerResponseST{
			channel:           channel,
			version:           connection.version,
			connectionIdBytes: connectionIdBytes,
			statusCode:        200,
			headers:           http.Header{},
		}
		handler.ServeHTTP(response, connection.request)
		err := response.Close()
		if err != nil {
			log.Printf("Error closing response: %v", err)
		}
	}

	onRequestLine := func(connectionId uint32, line []byte) error {
		if connection, ok := connections.Get(connectionId); !ok {
			parts := reSpaces.Split(string(line), -1)
			if len(parts) == 3 {
				method := parts[0]
				path := parts[1]
				version := parts[2]
				connection, err := createWebRTCServerConnection(method, path, version)
				if err != nil {
					return err
				}
				connections.Set(connectionId, connection)
			}
		} else {
			if !connection.readHeaders {
				if line[0] == r && line[1] == n {
					connection.readHeaders = true
					// TODO: use a context here
					go handle(connectionId, connection)
				} else {
					header := reHeader.Split(string(line), 2)
					if len(header) == 2 {
						key := header[0]
						value := header[1]
						connection.request.Header.Add(key, value)
					}
				}
			} else {
				if line[0] == r && line[1] == n {
					connection.writer.Close()
					connections.Delete(connectionId)
				} else {
					written, err := connection.writer.Write(line)
					connection.request.ContentLength += int64(written)
					if err != nil {
						return err
					}
				}
			}
		}
		return nil
	}

	onMessage := func(msg webrtc.DataChannelMessage) {
		connectionId := binary.BigEndian.Uint32(msg.Data[0:4])
		err := onRequestLine(connectionId, msg.Data[4:])
		if err != nil {
			log.Printf("error handling message: %v\n", err)
		}
	}

	channel.OnMessage(onMessage)

	return server
}
