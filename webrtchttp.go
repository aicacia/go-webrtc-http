package webrtchttp

import (
	"io"
	"math"
	"math/rand"

	webrtc "github.com/pion/webrtc/v4"
)

var defaultMaxMessageSize = 16384

func encodeLineBytes(requestIdBytes []byte, line []byte) []byte {
	return append(requestIdBytes, line...)
}

func randUint32() uint32 {
	return uint32(rand.Int31n(math.MaxInt32))
}

type channelWriter struct {
	connectionIdBytes []byte
	channel           *webrtc.DataChannel
	maxMessageSize    int
}

func newChannelWriter(connectionIdBytes []byte, channel *webrtc.DataChannel, maxMessageSize int) io.Writer {
	return &channelWriter{
		connectionIdBytes: connectionIdBytes,
		channel:           channel,
		maxMessageSize:    maxMessageSize,
	}
}

func (channelWriter *channelWriter) Write(bytes []byte) (int, error) {
	written := 0
	for len(bytes) > 0 {
		bytesCount := len(bytes)
		if bytesCount > channelWriter.maxMessageSize {
			bytesCount = channelWriter.maxMessageSize
		}
		err := channelWriter.channel.Send(encodeLineBytes(channelWriter.connectionIdBytes, bytes[:bytesCount]))
		if err != nil {
			return written, err
		}
		written += bytesCount
		bytes = bytes[bytesCount:]
	}
	return written, nil
}
