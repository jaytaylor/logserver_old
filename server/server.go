package server

import (
	"bufio"
	"fmt"
	"io"
	"log"
	"net"
	"time"

	. "github.com/sendhub/log"
)

type (
	Listener struct {
		Channel   chan Entry
		LastEntry time.Time
		Filter    EntryFilter
	}
	Drain struct {
		Listener *Listener
	}
	Server struct {
		AddListener    chan *Listener
		AddDrain       chan *Drain
		ReceiveEntry   chan Entry
		RemoveListener chan *Listener
		RemoveDrain    chan *Drain
		listeners      []*Listener
		history        History
		drains         []*Drain
	}
)

func (this *Server) addListener(listener *Listener) {
	this.listeners = append(this.listeners, listener)
	timeout := time.After(time.Second * 1)
	for entry := range this.history.GetEntriesSince(listener.LastEntry) {
		if !listener.Filter.Include(entry) {
			continue
		}
		select {
		case listener.Channel <- entry:
		case <-timeout:
			break
		}
	}

}
func (this *Server) addDrain(drain *Drain) {
	this.drains = append(this.drains, drain)
	this.addListener(drain.Listener)
}
func (this *Server) removeDrain(drain *Drain) {
	nds := make([]*Drain, 0, len(this.drains)-1)
	for _, thisDrain := range this.drains {
		if drain != thisDrain {
			nds = append(nds, thisDrain)
		} else {
			this.removeListener(thisDrain.Listener)
		}
	}
	this.drains = nds
}
func (this *Server) removeListener(listener *Listener) {
	nls := make([]*Listener, 0, len(this.listeners)-1)
	for _, thisListener := range this.listeners {
		if listener != thisListener {
			nls = append(nls, thisListener)
		}
	}
	this.listeners = nls
}
func (this *Server) receiveEntry(entry Entry) {
	log.Printf("[DEBUG] %v", entry)
	this.history.Add(entry)
	// Push an entry on to the end of the channel,
	//   if the channel is full remove the first entry
	//   and try to push it on the end again
	for _, listener := range this.listeners {
		if !listener.Filter.Include(entry) {
			continue
		}

		select {
		case listener.Channel <- entry:
			continue
		default:
		}

		select {
		case <-listener.Channel:
		default:
		}

		select {
		case listener.Channel <- entry:
		default:
		}
	}
}

func Start() (*Server, error) {
	this := &Server{
		ReceiveEntry:   make(chan Entry),
		listeners:      make([]*Listener, 0),
		AddListener:    make(chan *Listener),
		AddDrain:       make(chan *Drain),
		RemoveListener: make(chan *Listener),
		RemoveDrain:    make(chan *Drain),
		history: History{
			Position: 0,
			Size:     0,
			Capacity: 1000,
			Entries:  make([]Entry, 1000),
		},
	}

	log.Printf("starting logging server on: %v\n", Port)
	ln, err := net.Listen("tcp", ":"+fmt.Sprint(Port))
	if err != nil {
		return this, err
	}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				log.Printf("error: %v\n", err)
				continue
			}
			log.Printf("[%v] connected\n", conn.RemoteAddr())
			go this.handleConnection(conn)
		}
	}()

	go func() {
		for {
			select {
			// Add a listener to the list
			case listener := <-this.AddListener:
				this.addListener(listener)
			case drain := <-this.AddDrain:
				this.addDrain(drain)
			case drain := <-this.RemoveDrain:
				this.removeDrain(drain)
			// Remove a listener
			case listener := <-this.RemoveListener:
				this.removeListener(listener)
			// Receive a message
			case entry, ok := <-this.ReceiveEntry:
				if !ok {
					break
				}
				this.receiveEntry(entry)
			}
		}
	}()

	return this, nil
}
func (this *Server) handleConnection(conn net.Conn) {
	defer conn.Close()

	var typ string
	err := Read(conn, &typ)
	if err != nil {
		fmt.Printf("error: %v\n", err)
		return
	}
	switch typ {
	case "logger":
		this.handleLogger(conn)
	}
}

func (this *Server) handleLogger(conn net.Conn) {
	reader := bufio.NewReader(conn)
	for {
		entry, err := ReadEntry(reader)
		if err != nil {
			fmt.Printf("error: %v\n", err)
			break
		}
		this.ReceiveEntry <- entry
	}
}

func (this *Server) StartListener(w io.Writer, filter EntryFilter) error {
	c := make(chan Entry)
	listener := &Listener{
		Channel: c,
		Filter:  filter,
	}
	defer func() {
		this.RemoveListener <- listener
	}()
	this.AddListener <- listener

	for entry := range Throttle(c, 100) {
		err := Write(w, entry.Data)
		if err != nil {
			return err
		}
	}
	return nil
}