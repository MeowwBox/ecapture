// Copyright 2024 yuweizzz <yuwei764969238@gmail.com>. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//   http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package event_processor

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"log"

	"golang.org/x/net/http2"
	"golang.org/x/net/http2/hpack"
)

const frameHeaderLen = 9

// http2.FrameType
// const (
//     FrameData         FrameType = 0x0
//     FrameHeaders      FrameType = 0x1
//     FramePriority     FrameType = 0x2
//     FrameRSTStream    FrameType = 0x3
//     FrameSettings     FrameType = 0x4
//     FramePushPromise  FrameType = 0x5
//     FramePing         FrameType = 0x6
//     FrameGoAway       FrameType = 0x7
//     FrameWindowUpdate FrameType = 0x8
//     FrameContinuation FrameType = 0x9
// )

type HTTP2Response struct {
	framer     *http2.Framer
	packerType PacketType
	isDone     bool
	isInit     bool
	reader     *bytes.Buffer
	bufReader  *bufio.Reader
}

func (h2r *HTTP2Response) detect(payload []byte) error {
	payloadLen := len(payload)
	if payloadLen < frameHeaderLen {
		return errors.New("Payload less than http2 frame Header")
	}
	// https://httpwg.org/specs/rfc7540.html#FrameHeader
	// All frames begin with a fixed 9-octet header followed by a variable-length payload.
	//
	// +-----------------------------------------------+
	// |                 Length (24)                   |
	// +---------------+---------------+---------------+
	// |   Type (8)    |   Flags (8)   |
	// +-+-------------+---------------+-------------------------------+
	// |R|                 Stream Identifier (31)                      |
	// +=+=============================================================+
	// |                   Frame Payload (0...)                      ...
	// +---------------------------------------------------------------+
	data := payload[0:frameHeaderLen]
	// currentFrameLen := uint32(data[0])<<16 | uint32(data[1])<<8 | uint32(data[2])
	currentFrameType := http2.FrameType(data[3])
	// Frame type in http2.FrameType
	if currentFrameType > http2.FrameContinuation {
		return errors.New("Invalid frame type")
	}
	// R: A reserved 1-bit field.
	// The semantics of this bit are undefined, and the bit MUST remain unset (0x0) when
	// sending and MUST be ignored when receiving.
	reservedBit := uint32(data[5]) >> 7
	if reservedBit == 0 {
		return nil
	}
	return errors.New("Invalid reserved bit in frame header")
}

func (h2r *HTTP2Response) Init() {
	h2r.reader = bytes.NewBuffer(nil)
	h2r.bufReader = bufio.NewReader(h2r.reader)
	h2r.framer = http2.NewFramer(nil, h2r.bufReader)
	h2r.framer.ReadMetaHeaders = hpack.NewDecoder(0, nil)
}

func (h2r *HTTP2Response) Write(b []byte) (int, error) {
	if !h2r.isInit {
		h2r.Init()
		h2r.isInit = true
	}
	length, err := h2r.reader.Write(b)
	if err != nil {
		return 0, err
	}
	return length, nil
}

func (h2r *HTTP2Response) ParserType() ParserType {
	return ParserTypeHttp2Response
}

func (h2r *HTTP2Response) PacketType() PacketType {
	return h2r.packerType
}

func (h2r *HTTP2Response) Name() string {
	return "HTTP2Response"
}

func (h2r *HTTP2Response) IsDone() bool {
	return h2r.isDone
}

func (h2r *HTTP2Response) Display() []byte {
	var encoding string
	var dataFrameStreamID uint32
	dataBuf := bytes.NewBuffer(nil)
	frameBuf := bytes.NewBufferString("")
	for {
		f, err := h2r.framer.ReadFrame()
		if err != nil {
			if err != io.EOF {
				log.Println("[http2 response] Dump HTTP2 Frame error:", err)
			}
			break
		}
		switch f := f.(type) {
		case *http2.MetaHeadersFrame:
			frameBuf.WriteString(fmt.Sprintf("\nFrame Type\t=>\tHEADERS\nFrame StreamID\t=>\t%d\n", f.StreamID))
			for _, header := range f.Fields {
				frameBuf.WriteString(fmt.Sprintf("%s\n", header.String()))
				if header.Name == "content-encoding" {
					encoding = header.Value
				}
			}
		case *http2.DataFrame:
			dataFrameStreamID = f.StreamID
			_, err := dataBuf.Write(f.Data())
			if err != nil {
				log.Println("[http2 response] Write HTTP2 Data Frame buffuer error:", err)
			}
		default:
			fh := f.Header()
			frameBuf.WriteString(fmt.Sprintf("\nFrame Type\t=>\t%s\nFrame StreamID\t=>\t%d\n", fh.Type.String(), fh.StreamID))
		}
	}
	// merge data frame
	if dataBuf.Len() > 0 {
		frameBuf.WriteString(fmt.Sprintf("\nFrame Type\t=>\tDATA\nFrame StreamID\t=>\t%d\n", dataFrameStreamID))
		payload := dataBuf.Bytes()
		switch encoding {
		case "gzip":
			reader, err := gzip.NewReader(bytes.NewReader(payload))
			if err != nil {
				log.Println("[http2 response] Create gzip reader error:", err)
				break
			}
			payload, err = io.ReadAll(reader)
			if err != nil {
				log.Println("[http2 response] Uncompress gzip data error:", err)
				break
			}
			h2r.packerType = PacketTypeGzip
			defer reader.Close()
		default:
			h2r.packerType = PacketTypeNull
		}
		frameBuf.Write(payload)
	}
	return frameBuf.Bytes()
}

func init() {
	h2r := &HTTP2Response{}
	h2r.Init()
	Register(h2r)
}

func (h2r *HTTP2Response) Reset() {
	h2r.isDone = false
	h2r.isInit = false
	h2r.reader.Reset()
	h2r.bufReader.Reset(h2r.reader)
}
