package main

import (
	"flag"
	"github.com/garyburd/t2/server"
	"github.com/garyburd/t2/web"
	"text/template"
)

var addr = flag.String("addr", ":8080", "http service address")
var homeTempl = template.Must(template.ParseFiles("home.html"))

func serveHome(resp web.Response, req *web.Request) error {
	w := resp.Start(web.StatusOK,
		web.Header{web.HeaderContentType: {"text/html; charset=utf-8"}})
	return homeTempl.Execute(w, req.URL.Host)
}

func main() {
	flag.Parse()
	go h.run()
	server.Run(*addr, web.NewRouter().
		Register("/", "GET", serveHome).
		Register("/ws", "GET", serveWs))
}
