package main

import (
	"fmt"
	"github.com/garyburd/redigo/redis"
	"github.com/gorilla/mux"
	"github.com/gorilla/websocket"
	"net/http"
)

type addConnMessage struct {
	device  string
	session string
	channel chan []byte
}

type delConnMessage struct {
	device  string
	session string
}

var (
	addConnChannel = make(chan addConnMessage)
	delConnChannel = make(chan delConnMessage)
	redisChannel   = make(chan redis.PMessage)
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
}

var redisConnection redis.Conn

func handleSubscription(w http.ResponseWriter, r *http.Request) {

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		println(err.Error())
		return
	}

	vars := mux.Vars(r)
	device := vars["device"]
	session := vars["session"]
	ch := make(chan []byte)

	addConnChannel <- addConnMessage{device, session, ch}

	for {
		select {
		case newmsg := <-ch:
			println("I need to send this down the socket:", string(newmsg))
			conn.WriteMessage(1, newmsg)
		}
	}

	// TODO: Decide how to know when the connection is closed....
	// Do I really need a new goroutine for this to read on continually?
}

func redisMonitor() {
	psc := redis.PubSubConn{redisConnection}
	psc.PSubscribe("*")
	for {
		switch v := psc.Receive().(type) {
		case redis.PMessage:
			println("redis.PMessage:", v.Channel, string(v.Data))
			redisChannel <- v
		case error:
			fmt.Printf("redis.Error: %v:", v)
		default:
			fmt.Printf("redis type %v: ", v)
		}
	}
}

func manager() {

	var connections map[string]map[string]chan []byte
	connections = make(map[string]map[string]chan []byte)

	for {
		select {
		case newconn := <-addConnChannel:
			println("manager: AddConnRequest:", newconn.device, newconn.session, newconn.channel)
			connections[newconn.session] = make(map[string]chan []byte)
			connections[newconn.session][newconn.device] = newconn.channel

		case oldconn := <-delConnChannel:
			println("manager: DelConnRequest:", oldconn.device, oldconn.session)
			delete(connections[oldconn.session], oldconn.device)
			if len(connections[oldconn.session]) < 1 {
				delete(connections, oldconn.session)
			}

		case redismsg := <-redisChannel:
			// Todo, probably need to verify the channel is in the connections map
			for _, v := range connections[redismsg.Channel] {
				v <- redismsg.Data
			}
		}
	}
}

func main() {

	var err error
	redisConnection, err = redis.Dial("tcp", "localhost:6379")
	if err != nil {
		println(err.Error())
	}
	defer redisConnection.Close()

	go redisMonitor()
	go manager()

	r := mux.NewRouter()
	r.HandleFunc("/sub/{device}/{session}", handleSubscription)
	r.Handle("/{rest}", http.FileServer(http.Dir(".")))
	http.Handle("/", r)
	err = http.ListenAndServe(":2001", nil)
	if err != nil {
		panic("Error: " + err.Error())
	}
}
