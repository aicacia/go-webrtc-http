package webrtc_http

import (
	"fmt"
	"regexp"
)

var reSpaces = regexp.MustCompile(`\s+`)
var reHeader = regexp.MustCompile(`\:\s+`)

const r = byte('\r')
const n = byte('\n')

var rn = []byte{r, n}

const PROTOCAL_NAME = "HTTP-WEBRTC"
const PROTOCAL_MAJOR = 1
const PROTOCAL_MINOR = 0

var PROTOCAL_VERSION = fmt.Sprintf("%d.%d", PROTOCAL_MAJOR, PROTOCAL_MINOR)
var PROTOCAL = fmt.Sprintf("%s/%s", PROTOCAL_NAME, PROTOCAL_VERSION)
