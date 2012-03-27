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
	readWait       = 60 * time.Second
	pingPeriod     = 25 * time.Second
	writeWait      = 10 * time.Second
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
	defer c.ws.Close()
	for {
		c.ws.SetReadDeadline(time.Now().Add(readWait))
		op, r, err := c.ws.NextReader()
		if err != nil {
			return
		}
		if op != websocket.OpText {
			continue
		}
		lr := io.LimitedReader{R: r, N: maxMessageSize + 1}
		message, err := ioutil.ReadAll(&lr)
		if err != nil {
			return
		}
		if lr.N <= 0 {
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
	w, err := c.ws.NextWriter(opCode)
	if err != nil {
		return err
	}
	if _, err := w.Write(payload); err != nil {
		w.Close()
		return err
	}
	return w.Close()
}

// writePump pumps messages from the hub to the websocket connection.
func (c *connection) writePump() {
	defer c.ws.Close()
	ticker := time.NewTicker(pingPeriod)
	defer ticker.Stop()
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
	defer func() { h.unregister <- c }()
	go c.writePump()
	c.readPump()
}
