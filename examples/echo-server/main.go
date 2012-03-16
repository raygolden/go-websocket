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

// Command echo-server is a test server for the Autobahn WebSockets Test Suite.
package main

import (
	"errors"
	"flag"
	"github.com/garyburd/go-websocket/websocket"
	"github.com/garyburd/t2/server"
	"github.com/garyburd/t2/web"
	"io"
	"io/ioutil"
	"unicode/utf8"
)

func valid(p []byte) bool {
	i := 0
	for i < len(p) {
		if p[i] < utf8.RuneSelf {
			i++
		} else {
			r, size := utf8.DecodeRune(p[i:])
			if size == 1 {
				// All valid runes of size of 1 (those
				// below RuneSelf) were handled above.
				// This must be a RuneError.
				return false
			}
			if r > utf8.MaxRune {
				return false
			}
			i += size
		}
	}
	return true
}

type option int

const (
	optionReadAll option = iota
	optionCopy
	optionValidateUTF8
)

func (o option) ServeWeb(resp web.Response, req *web.Request) error {
	conn, err := websocket.Upgrade(resp, req, "")
	if err != nil {
		return err
	}
	defer conn.Close()
	for {
		op, r, err := conn.NextReader()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if op == websocket.OpPong {
			continue
		}
		switch o {
		case optionCopy:
			w, err := conn.NextWriter(op)
			if err != nil {
				return err
			}
			_, err = io.Copy(w, r)
			if err != nil {
				return err
			}
			err = w.Close()
			if err != nil {
				return err
			}
		case optionReadAll, optionValidateUTF8:
			b, err := ioutil.ReadAll(r)
			if err != nil {
				return err
			}
			if o == optionValidateUTF8 && op == websocket.OpText && !valid(b) {
				conn.SendClose(websocket.CloseInvalidFramePayloadData, "")
				return errors.New("invalid utf8")
			}
			w, err := conn.NextWriter(op)
			if err != nil {
				return err
			}
			if _, err := w.Write(b); err != nil {
				return err
			}
			if err := w.Close(); err != nil {
				return err
			}
		default:
			panic("bad option")
		}
	}
	return nil
}

func serveHome(resp web.Response, req *web.Request) error {
	w := resp.Start(web.StatusOK, web.Header{web.HeaderContentType: {"text/plain; charset=utf-8"}})
	_, err := io.WriteString(w, "<html><body>Echo Server</body></html")
	return err
}

var addr = flag.String("addr", ":9000", "http service address")

func main() {
	flag.Parse()
	server.Run(*addr, web.NewRouter().
		Register("/", "GET", serveHome).
		Register("/c", "GET", optionCopy).
		Register("/r", "GET", optionReadAll).
		Register("/v", "GET", optionValidateUTF8))
}
