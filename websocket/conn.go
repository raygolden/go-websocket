// Copyright 2012 Gary Burd
//
// Licensed under the Apache License, Version 2.0 (the "License"): you may
// not use this file except in compliance with the License. You may obtain
// a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS, WITHOUT
// WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied. See the
// License for the specific language governing permissions and limitations
// under the License.

// Package websocket implements the WebSocket protocol.
//
// The package is a work in progress. The API is not frozen. The documentation
// is incomplete. The tests are thin.
package websocket

import (
	"bufio"
	"bytes"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"io"
	"io/ioutil"
	"log"
	"net"
	"strconv"
	"sync"
)

// Close codes defined in RFC 6455, section 11.7.
const (
	CloseNormalClosure           = 1000
	CloseGoingAway               = 1001
	CloseProtocolError           = 1002
	CloseUnsupportedData         = 1003
	CloseNoStatusReceived        = 1005
	CloseAbnormalClosure         = 1006
	CloseInvalidFramePayloadData = 1007
	ClosePolicyViolation         = 1008
	CloseMessageTooBig           = 1009
	CloseMandatoryExtension      = 1010
	CloseInternalServerErr       = 1011
	CloseTLSHandshake            = 1015
)

// Opcodes defined in RFC 6455, section 11.8.
const (
	OpContinuation = 0
	OpText         = 1
	OpBinary       = 2
	OpClose        = 8
	OpPing         = 9
	OpPong         = 10
)

var (
	ErrCloseSent = errors.New("websocket: close sent")
)

var (
	keyGUID = []byte("258EAFA5-E914-47DA-95CA-C5AB0DC85B11")
)

const (
	maxFrameHeaderSize         = 2 + 8 + 4 // Fixed header + length + mask
	maxControlFramePayloadSize = 125
	finalBit                   = 1 << 7
)

type Conn struct {
	conn net.Conn

	// Write fields
	mu        sync.Mutex // protects write to the connection and closeSent
	closeSent bool       // true if close frame sent

	// Message writer fields.
	writeBuf    []byte // frame is constructed in this buffer.
	writePos    int    // end of data in writeBuf.
	writeOpCode int    // op code for the current frame.
	writeSeq    int    // incremented to invalidate message writers.

	// Read fields
	readErr    error
	br         *bufio.Reader
	readLength int64 // bytes remaining in current frame.
	readFinal  bool  // true the current message has more frames.
	readSeq    int   // incremented to invalidate message readers.
	savedPong  []byte

	// Masking
	maskPos int
	maskKey [4]byte
}

func NewServerConn(conn net.Conn, br *bufio.Reader, writeBufSize int, subProtocol string, key string) (*Conn, error) {

	h := sha1.New()
	h.Write([]byte(key))
	h.Write(keyGUID)
	accpektKey := base64.StdEncoding.EncodeToString(h.Sum(nil))

	var buf bytes.Buffer
	buf.WriteString("HTTP/1.1 101 Switching Protocols")
	buf.WriteString("\r\nUpgrade: websocket")
	buf.WriteString("\r\nConnection: Upgrade")
	buf.WriteString("\r\nSec-WebSocket-Accept: ")
	buf.WriteString(accpektKey)
	if subProtocol != "" {
		buf.WriteString("\r\nSec-WebSocket-Protocol: ")
		buf.WriteString(subProtocol)
	}
	buf.WriteString("\r\n\r\n")

	_, err := conn.Write(buf.Bytes())
	if err != nil {
		return nil, err
	}

	return &Conn{
		conn:        conn,
		writeBuf:    make([]byte, writeBufSize+maxFrameHeaderSize),
		br:          br,
		writePos:    maxFrameHeaderSize,
		writeOpCode: -1,
		readFinal:   true,
	}, nil
}

func (c *Conn) Close() error {
	return c.conn.Close()
}

func (c *Conn) maskBytes(b []byte) {
	maskPos := c.maskPos
	for i := range b {
		b[i] ^= c.maskKey[maskPos&3]
		maskPos += 1
	}
	c.maskPos = maskPos & 3
}

// Write methods

func (c *Conn) write(opCode int, bufs ...[]byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closeSent {
		return ErrCloseSent
	}
	if opCode == OpClose {
		c.closeSent = true
	}
	for _, buf := range bufs {
		if len(buf) > 0 {
			_, err := c.conn.Write(buf)
			if err != nil {
				return err
			}
		}
	}
	return nil
}

func (c *Conn) SendClose(closeCode int, message string) error {
	frame := make([]byte, 4+len(message))
	frame[0] = finalBit | OpClose
	frame[1] = byte(len(message) + 2)
	binary.BigEndian.PutUint16(frame[2:], uint16(closeCode))
	copy(frame[4:], message)
	return c.write(OpClose, frame)
}

func (c *Conn) NextWriter(opCode int) (io.WriteCloser, error) {
	if c.writeOpCode != -1 {
		if err := c.flushFrame(true, nil); err != nil {
			return nil, err
		}
	}

	if opCode != OpText &&
		opCode != OpBinary &&
		opCode != OpClose &&
		opCode != OpPing {
		return nil, errors.New("websocket: bad opcode")
	}

	c.writeOpCode = opCode
	return messageWriter{c, c.writeSeq}, nil
}

func (c *Conn) flushFrame(final bool, extra []byte) error {

	length := c.writePos - maxFrameHeaderSize + len(extra)

	// Check for invalid control frames.
	if (c.writeOpCode == OpClose || c.writeOpCode == OpPing) &&
		(!final || length > maxControlFramePayloadSize) {
		c.writeSeq += 1
		c.writeOpCode = -1
		c.writePos = maxFrameHeaderSize
		return errors.New("websocket: invalid control frame")
	}

	b0 := byte(c.writeOpCode)
	if final {
		b0 |= finalBit
	}

	pos := 4
	switch {
	case length >= 65536:
		c.writeBuf[pos] = b0
		c.writeBuf[pos+1] = 127
		binary.BigEndian.PutUint64(c.writeBuf[pos+2:], uint64(length))
	case length > 125:
		pos += 6
		c.writeBuf[pos] = b0
		c.writeBuf[pos+1] = 126
		binary.BigEndian.PutUint16(c.writeBuf[pos+2:], uint16(length))
	default:
		pos += 8
		c.writeBuf[pos] = b0
		c.writeBuf[pos+1] = byte(length)
	}

	// Write the buffers to the connection.
	err := c.write(c.writeOpCode, c.writeBuf[pos:c.writePos], extra)

	// Setup for next frame.
	c.writePos = maxFrameHeaderSize
	c.writeOpCode = OpContinuation
	if final {
		c.writeSeq += 1
		c.writeOpCode = -1
	}
	return err
}

type messageWriter struct {
	c   *Conn
	seq int
}

func (w messageWriter) ncopy(max int) (int, error) {
	n := len(w.c.writeBuf) - w.c.writePos
	if n <= 0 {
		if err := w.c.flushFrame(false, nil); err != nil {
			return 0, err
		}
		n = len(w.c.writeBuf) - w.c.writePos
	}
	if n > max {
		n = max
	}
	return n, nil
}

func (w messageWriter) Write(p []byte) (int, error) {
	if w.c.writeSeq != w.seq {
		return 0, errors.New("websocket: closed writer")
	}

	if len(p) > 2*len(w.c.writeBuf) {
		// Don't buffer large messages.
		err := w.c.flushFrame(false, p)
		if err != nil {
			return 0, err
		}
		return len(p), nil
	}

	nn := len(p)
	for len(p) > 0 {
		n, err := w.ncopy(len(p))
		if err != nil {
			return 0, err
		}
		copy(w.c.writeBuf[w.c.writePos:], p[:n])
		w.c.writePos += n
		p = p[n:]
	}
	return nn, nil
}

func (w messageWriter) WriteString(p string) (int, error) {
	if w.c.writeSeq != w.seq {
		return 0, errors.New("websocket: closed writer")
	}

	nn := len(p)
	for len(p) > 0 {
		n, err := w.ncopy(len(p))
		if err != nil {
			return 0, err
		}
		copy(w.c.writeBuf[w.c.writePos:], p[:n])
		w.c.writePos += n
		p = p[n:]
	}
	return nn, nil
}

func (w messageWriter) Close() error {
	if w.c.writeSeq != w.seq {
		return errors.New("websocket: closed writer")
	}
	return w.c.flushFrame(true, nil)
}

// Read methods

var closeFrame = []byte{finalBit | OpClose, 0}

func (c *Conn) advanceFrame() (int, error) {

	// 1. Skip remainder of previous frame.

	if c.readLength > 0 {
		log.Println("SKIPING")
		if _, err := io.CopyN(ioutil.Discard, c.br, c.readLength); err != nil {
			return -1, err
		}
	}

	// 2. Read and parse first two bytes of frame header.

	var b [8]byte
	if err := c.read(b[:2]); err != nil {
		return -1, err
	}

	final := b[0]&finalBit != 0
	opCode := int(b[0] & 0xf)
	reserved := int((b[0] >> 4) & 0x7)
	mask := b[1]&(1<<7) != 0
	c.readLength = int64(b[1] & 0x7f)

	if reserved != 0 {
		return -1, c.handleProtocolError("unexpected reserved bits")
	}

	switch opCode {
	case OpClose, OpPing, OpPong:
		if c.readLength > maxControlFramePayloadSize {
			return -1, c.handleProtocolError("control frame length > 125")
		}
		if !final {
			return -1, c.handleProtocolError("control frame not final")
		}
	case OpText, OpBinary:
		if !c.readFinal {
			return -1, c.handleProtocolError("message start before final message frame")
		}
		c.readFinal = final
	case OpContinuation:
		if c.readFinal {
			return -1, c.handleProtocolError("continuation after final message frame")
		}
		c.readFinal = final
	default:
		return -1, c.handleProtocolError("unknown opcode")
	}

	// 3. Read and parse frame length.

	switch c.readLength {
	case 126:
		if err := c.read(b[:2]); err != nil {
			return -1, err
		}
		c.readLength = int64(binary.BigEndian.Uint16(b[:2]))
	case 127:
		if err := c.read(b[:8]); err != nil {
			return -1, err
		}
		c.readLength = int64(binary.BigEndian.Uint64(b[:8]))
	}

	// 4. Handle frame masking.

	if !mask {
		return -1, c.handleProtocolError("improper masking")
	}

	c.maskPos = 0
	if err := c.read(c.maskKey[:]); err != nil {
		return -1, err
	}

	// 5. Return if not handling a control frame.

	if opCode == OpContinuation || opCode == OpText || opCode == OpBinary {
		return opCode, nil
	}

	// 6. Read control frame payload.

	payload := make([]byte, c.readLength)
	c.readLength = 0
	if err := c.read(payload); err != nil {
		return -1, err
	}
	c.maskBytes(payload)

	// 7. Process control frame payload.

	switch opCode {
	case OpPong:
		c.savedPong = payload
	case OpPing:
		frame := make([]byte, 2+len(payload))
		frame[0] = finalBit | OpPong
		frame[1] = byte(len(payload))
		copy(frame[2:], payload)
		c.write(OpPong, frame)
	case OpClose:
		c.write(OpClose, closeFrame)
		if len(payload) < 2 {
			return -1, io.EOF
		} else {
			closeCode := binary.BigEndian.Uint16(payload)
			switch closeCode {
			case CloseNormalClosure, CloseGoingAway:
				return -1, io.EOF
			default:
				return -1, errors.New("websocket: peer sent " +
					strconv.Itoa(int(closeCode)) + " " +
					string(payload[2:]))
			}
		}
		log.Println("CLOSE", c.readErr)
	}

	return opCode, nil
}

func (c *Conn) handleProtocolError(message string) error {
	c.SendClose(CloseProtocolError, message)
	return errors.New("websocket: " + message)
}

func (c *Conn) read(buf []byte) error {
	var err error
	for len(buf) > 0 && err == nil {
		var nn int
		nn, err = c.br.Read(buf)
		buf = buf[nn:]
	}
	if err == io.EOF {
		if len(buf) == 0 {
			err = nil
		} else {
			err = io.ErrUnexpectedEOF
		}
	}
	return err
}

func (c *Conn) NextReader() (opCode int, r io.Reader, err error) {

	c.readSeq += 1

	if c.savedPong != nil {
		r := bytes.NewReader(c.savedPong)
		c.savedPong = nil
		return OpPong, r, nil
	}

	for c.readErr == nil {
		var opCode int
		opCode, c.readErr = c.advanceFrame()
		switch opCode {
		case OpText, OpBinary:
			return opCode, messageReader{c, c.readSeq}, nil
		case OpPong:
			r := bytes.NewReader(c.savedPong)
			c.savedPong = nil
			return OpPong, r, nil
		case OpContinuation:
			panic("unexpected continuation")
		}
	}
	return -1, nil, c.readErr
}

type messageReader struct {
	c   *Conn
	seq int
}

func (r messageReader) Read(b []byte) (n int, err error) {

	if r.seq != r.c.readSeq {
		return 0, io.EOF
	}

	for r.c.readErr == nil {

		if r.c.readLength > 0 {
			if int64(len(b)) > r.c.readLength {
				b = b[:r.c.readLength]
			}
			r.c.readErr = r.c.read(b)
			r.c.maskBytes(b)
			r.c.readLength -= int64(len(b))
			return len(b), r.c.readErr
		}

		if r.c.readFinal {
			r.c.readSeq += 1
			return 0, io.EOF
		}

		var opCode int
		opCode, r.c.readErr = r.c.advanceFrame()

		if opCode == OpText || opCode == OpBinary {
			panic("unexpected text or binary frame")
		}
	}
	return 0, r.c.readErr
}
