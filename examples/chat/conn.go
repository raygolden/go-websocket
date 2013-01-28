package main

import (
	"github.com/garyburd/go-websocket/websocket"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"time"
)

const (
	// Time allowed to write a message to the client.
	writeWait = 10 * time.Second

	// Time allowed to read the next message from the client.
	readWait = 60 * time.Second

	// Send pings to client with this period. Must be less than readWait.
	pingPeriod = (readWait * 9) / 10

	// Maximum message size allowed from client.
	maxMessageSize = 512
)

// connection is an middleman between the websocket connection and the hub.
type connection struct {
	// The websocket connection.
	ws *websocket.Conn

	// Buffered channel of outbound messages.
	send chan []byte
}

// readPump pumps messages from the websocket connection to the hub.
func (c *connection) readPump() {
	defer func() {
		h.unregister <- c
		c.ws.Close()
	}()
	for {
		// Use deadline to detect dead or stuck clients.
		c.ws.SetReadDeadline(time.Now().Add(readWait))
		op, r, err := c.ws.NextReader()
		if err != nil {
			return
		}
		if op == websocket.OpBinary {
			// unexpected
			return
		}
		if op != websocket.OpText {
			// ignore pongs and other control messages.
			continue
		}
		lr := io.LimitedReader{R: r, N: maxMessageSize + 1}
		message, err := ioutil.ReadAll(&lr)
		if err != nil {
			return
		}
		if lr.N <= 0 {
			// Message is larger than max allowed message size.
			c.ws.WriteControl(websocket.OpClose,
				websocket.FormatCloseMessage(websocket.CloseMessageTooBig, ""),
				time.Now().Add(time.Second))
			return
		}
		h.broadcast <- message
	}
}

// write writes a message with the given opCode and payload.
func (c *connection) write(opCode int, payload []byte) error {
	c.ws.SetWriteDeadline(time.Now().Add(writeWait))
	return c.ws.WriteMessage(opCode, payload)
}

// writePump pumps messages from the hub to the websocket connection.
func (c *connection) writePump() {
	ticker := time.NewTicker(pingPeriod)
	defer func() {
		ticker.Stop()
		c.ws.Close()
	}()
	for {
		select {
		case message, ok := <-c.send:
			if !ok {
				c.write(websocket.OpClose, []byte{})
				return
			}
			if err := c.write(websocket.OpText, message); err != nil {
				return
			}
		case <-ticker.C:
			if err := c.write(websocket.OpPing, []byte{}); err != nil {
				return
			}
		}
	}
}

// serverWs handles webocket requests from the client.
func serveWs(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		http.Error(w, "Method not allowed", 405)
		return
	}
	ws, err := websocket.Upgrade(w, r.Header, "", 1024, 1024)
	if err != nil {
		log.Println(err)
		http.Error(w, "Bad request", 400)
		return
	}
	c := &connection{send: make(chan []byte, 256), ws: ws}
	h.register <- c
	go c.writePump()
	c.readPump()
}
