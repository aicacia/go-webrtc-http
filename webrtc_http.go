package webrtc_http

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"log"
	http "net/http"
	"net/url"
	"regexp"

	webrtc "github.com/pion/webrtc/v4"
)

var reSpaces = regexp.MustCompile(`\s+`)
var reHeader = regexp.MustCompile(`\:\s+`)
var n = byte('\n')
var r = byte('\r')

var PROTOCAL_NAME = "HTTP-WEBRTC"
var PROTOCAL_VERSION = "1.0"
var PROTOCAL = PROTOCAL_NAME + "/" + PROTOCAL_VERSION

type WebRTCServerST struct {
	channel *webrtc.DataChannel
}

type WebRTCRequestST struct {
	version     string
	readHeaders bool
	method      string
	path        string
	body        bytes.Buffer
	headers     http.Header
}

func (r *WebRTCRequestST) ToNativeRequest() (*http.Request, error) {
	url, err := url.Parse("webrtc-http://" + r.path)
	if err != nil {
		return nil, err
	}
	request := &http.Request{
		Method:        r.method,
		URL:           url,
		Header:        r.headers,
		Body:          io.NopCloser(bufio.NewReader(&r.body)),
		ContentLength: int64(r.body.Len()),
		RequestURI:    url.RequestURI(),
		Close:         true,
	}
	return request, nil
}

func createWebRTCRequest(method, path, version string) *WebRTCRequestST {
	return &WebRTCRequestST{
		version:     version,
		readHeaders: false,
		method:      method,
		path:        path,
		headers:     http.Header{},
	}
}

type WebRTCResponseST struct {
	version        string
	requestId      uint32
	requestIdBytes []byte
	statusCode     int
	headers        http.Header
	body           bytes.Buffer
}

func createWebRTCResponse(requestId uint32, version string) *WebRTCResponseST {
	requestIdBytes := make([]byte, 4)
	binary.BigEndian.PutUint32(requestIdBytes, requestId)
	return &WebRTCResponseST{
		version:        version,
		requestId:      requestId,
		requestIdBytes: requestIdBytes,
		statusCode:     200,
		headers:        http.Header{},
	}
}

func (r *WebRTCResponseST) Header() http.Header {
	return r.headers
}

func (r *WebRTCResponseST) Write(bytes []byte) (int, error) {
	return r.body.Write(bytes)
}

func (r *WebRTCResponseST) WriteHeader(statusCode int) {
	r.statusCode = statusCode
	r.headers.Set("Status", http.StatusText(statusCode))
}

func (r *WebRTCResponseST) writeToChannel(channel *webrtc.DataChannel) error {
	err := channel.Send(encodeLine(r.requestIdBytes, fmt.Sprintf("%s %d %s", r.version, r.statusCode, statusCodeToStatusText(r.statusCode))))
	if err != nil {
		return err
	}
	for k, v := range r.headers {
		for _, value := range v {
			err := channel.Send(encodeLine(r.requestIdBytes, fmt.Sprintf("%s: %s", k, value)))
			if err != nil {
				return err
			}
		}
	}
	err = channel.Send(encodeLine(r.requestIdBytes, "\r\n"))
	if err != nil {
		return err
	}
	if r.body.Len() > 0 {
		maxMessageSize := int(channel.Transport().GetCapabilities().MaxMessageSize)
		if maxMessageSize == 0 {
			maxMessageSize = 8192
		}
		for r.body.Len() > 0 {
			bytesCount := r.body.Len()
			if bytesCount > maxMessageSize {
				bytesCount = maxMessageSize
			}
			bytes := r.body.Next(bytesCount)
			err := channel.Send(encodeLineBytes(r.requestIdBytes, bytes))
			if err != nil {
				return err
			}
		}
	}
	return channel.Send(encodeLine(r.requestIdBytes, "\r\n"))
}

func NewServer(channel *webrtc.DataChannel, handler http.HandlerFunc) *WebRTCServerST {
	requests := make(map[uint32]*WebRTCRequestST)

	server := &WebRTCServerST{
		channel: channel,
	}

	handle := func(requestId uint32, webrtcRequest *WebRTCRequestST) error {
		request, err := webrtcRequest.ToNativeRequest()
		if err != nil {
			return err
		}
		response := createWebRTCResponse(requestId, webrtcRequest.version)
		handler.ServeHTTP(response, request)
		return response.writeToChannel(channel)
	}

	onRequestLine := func(requestId uint32, line []byte) error {
		if request, ok := requests[requestId]; !ok {
			parts := reSpaces.Split(string(line), -1)
			if len(parts) == 3 {
				method := parts[0]
				path := parts[1]
				version := parts[2]
				requests[requestId] = createWebRTCRequest(method, path, version)
			}
		} else {
			if !request.readHeaders {
				if line[0] == r && line[1] == n {
					request.readHeaders = true
				} else {
					header := reHeader.Split(string(line), 2)
					if len(header) == 2 {
						key := header[0]
						value := header[1]
						request.headers.Add(key, value)
					}
				}
			} else {
				if line[0] == r && line[1] == n {
					err := handle(requestId, request)
					delete(requests, requestId)
					return err
				} else {
					request.body.Write(line)
				}
			}
		}
		return nil
	}

	onMessage := func(msg webrtc.DataChannelMessage) {
		requestId := binary.BigEndian.Uint32(msg.Data[0:4])
		err := onRequestLine(requestId, msg.Data[4:])
		if err != nil {
			log.Printf("Error handling request: %v", err)
		}
	}

	channel.OnMessage(onMessage)

	return server
}
