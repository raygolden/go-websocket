package main

import (
	"github.com/garyburd/go-websocket/websocket"
	"github.com/garyburd/t2/web"
	"io/ioutil"
)

type connection struct {
	// The websocket connection.
	ws *websocket.Conn

	// Buffered channel of outbound messages.
	send chan []byte
}

func (c *connection) reader() {
	for {
		op, r, err := c.ws.NextReader()
		if err != nil {
			break
		}
		if op != websocket.OpText {
			continue
		}
		message, err := ioutil.ReadAll(r)
		if err != nil {
			break
		}
		h.broadcast <- message
	}
	c.ws.Close()
}

func (c *connection) writer() {
	for message := range c.send {
		w, err := c.ws.NextWriter(websocket.OpText)
		if err != nil {
			break
		}
		if _, err = w.Write(message); err != nil {
			break
		}
		if err = w.Close(); err != nil {
			break
		}
	}
	c.ws.Close()
}

func serveWs(resp web.Response, req *web.Request) error {
	ws, err := websocket.Upgrade(resp, req, "")
	if err != nil {
		return err
	}
	c := &connection{send: make(chan []byte, 256), ws: ws}
	h.register <- c
	defer func() { h.unregister <- c }()
	go c.writer()
	c.reader()
	return nil
}
