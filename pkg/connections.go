package tnl

import (
	"encoding/json"
	"errors"
	"net"
	"sync"
)

//Connections holds client-server connections
type Connections struct {
	sync.Mutex
	list []connection
}

type connection struct {
	enc *json.Encoder
	dec *json.Decoder

	conn net.Conn

	host string

	port string

	token string

	closed bool
}

func (c *Connections) Add(conn connection) {
	c.Lock()
	c.list = append(c.list, conn)
	c.Unlock()
}

func (c *Connections) get(host string) *connection {
	c.Lock()
	var response *connection
	for _, item := range c.list {
		if item.host == host {
			response = &item
			break
		}
	}
	c.Unlock()
	return response
}

func (c *Connections) exists(host string) bool {
	c.Lock()
	var response bool
	for _, item := range c.list {
		if item.host == host {
			response = true
			break
		}
	}
	c.Unlock()
	return response
}
func (c *Connections) delete(conn connection) {
	c.Lock()
	var i int
	for index, item := range c.list {
		if item == conn {
			i = index
			break
		}
	}
	c.list = append(c.list[:i], c.list[i+1:]...)
	c.Unlock()
}

func (c *connection) Close() error {
	if c == nil {
		return nil
	}
	c.closed = true
	return c.conn.Close()
}

func (c *connection) send(v interface{}) error {
	if c.enc == nil {
		return errors.New("connection missing encoder")
	}

	if c.closed {
		return errors.New("connection closed")
	}

	return c.enc.Encode(v)
}

func (c *connection) recv(v interface{}) error {
	if c.dec == nil {
		return errors.New("connection missing decoder")
	}

	if c.closed {
		return errors.New("connection closed")
	}

	return c.dec.Decode(v)
}
