package main

import (
	"crypto/rand"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gorilla/mux"
	"github.com/gorilla/websocket"
	redis "gopkg.in/redis.v3"
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
	redisChannel   = make(chan *redis.Message)
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
}

var redisClient *redis.Client

func uuid() string {
	// generate 32 bits timestamp
	unix32bits := uint32(time.Now().UTC().Unix())

	buff := make([]byte, 12)

	numRead, err := rand.Read(buff)

	if numRead != len(buff) || err != nil {
		panic(err)
	}

	return fmt.Sprintf("%x-%x-%x-%x-%x-%x\n", unix32bits, buff[0:2], buff[2:4], buff[4:6], buff[6:8], buff[8:])
}

func handleSubscription(w http.ResponseWriter, r *http.Request) {

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		println(err.Error())
		return
	}

	vars := mux.Vars(r)
	device := uuid()
	session := vars["session"]
	ch := make(chan []byte)

	addConnChannel <- addConnMessage{device, session, ch}

	// TODO: This should be a loop where it both reads and writes every once in a while.
	// That way if we do not receive the disconnect, we will get it on the next ping/write.
	go func() {
		println("Starting routine to read from device: ", device)
		messageType, p, err := conn.ReadMessage()
		println("read message: ", device, messageType, p, err)
		if err != nil {
			println("Device disconnected: ", device)
			delConnChannel <- delConnMessage{device, session}
			return
		}
	}()

	for {
		select {
		case newmsg := <-ch:
			println("Device:", device, "session:", session, "message from channel and for redis", string(newmsg))
			conn.WriteMessage(1, newmsg)
		}
	}

}

func redisMonitor() {

	pubsub, err := redisClient.PSubscribe("*")
	if err != nil {
		println("Error during PSubscribe: ", err.Error())
		return
	}

	for {
		v, err := pubsub.ReceiveMessage()
		if err != nil {
			println("Error during ReceiveMessage: ", err.Error())
			break
		}

		println("redis.PMessage: ", v.Channel, v.Payload)

		if v.Channel == "__keyevent@0__:expired" {
			println("Processing expired message")

			parts := strings.Split(string(v.Payload), ":")
			println("parts: ", parts)

			cmd := "data:" + parts[1] + ":" + parts[2]
			println("cmd: ", cmd)

			strCommand := redisClient.Get(cmd)

			redisClient.Publish(parts[1], strCommand.Val())

			redisClient.Del("data:" + parts[1] + ":" + parts[2])

		}

		redisChannel <- v
	}
}

// ConnectionsListType is just a container for all the connections
type ConnectionsListType map[string]map[string]chan []byte

func printConnections(conns ConnectionsListType) {
	println("------------------------------------------------------------------------")
	for session, deviceList := range conns {
		for device := range deviceList {
			println(session, device)
		}
	}
	println("------------------------------------------------------------------------")
}

func manager() {

	var connections ConnectionsListType
	connections = make(map[string]map[string]chan []byte)

	for {
		select {
		case newconn := <-addConnChannel:
			printConnections(connections)
			println("AddConnRequest:", newconn.session, newconn.device, newconn.channel)

			if _, ok := connections[newconn.session]; !ok {
				connections[newconn.session] = make(map[string]chan []byte)
			}

			connections[newconn.session][newconn.device] = newconn.channel
			printConnections(connections)

		case oldconn := <-delConnChannel:
			println("manager: DelConnRequest:", oldconn.device, oldconn.session)
			delete(connections[oldconn.session], oldconn.device)
			if len(connections[oldconn.session]) < 1 {
				delete(connections, oldconn.session)
			}

		case redismsg := <-redisChannel:
			// Todo, probably need to verify the channel is in the connections map
			for _, v := range connections[redismsg.Channel] {
				v <- []byte(redismsg.Payload)
			}
		}
	}
}

func main() {

	var err error
	options := &redis.Options{Network: "tcp", Addr: "localhost:6379"}
	redisClient = redis.NewClient(options)
	defer redisClient.Close()

	go redisMonitor()
	go manager()

	r := mux.NewRouter()
	r.HandleFunc("/sub/{session}", handleSubscription)
	r.Handle("/{rest}", http.FileServer(http.Dir(".")))
	http.Handle("/", r)
	err = http.ListenAndServe(":2001", nil)
	if err != nil {
		panic("Error: " + err.Error())
	}
}
