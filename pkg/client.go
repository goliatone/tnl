package tnl

import (
	"bufio"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/hashicorp/yamux"
)

//ClientState represents the state of client's connection
//to the tunnel server
type ClientState uint32

//ClientState enums
const (
	Unknown ClientState = iota
	Started
	Connecting
	Connected
	Disconnected
	Closed
)

//Client holds methods to manage connection
type Client struct {
	//Address is the hostname the tunnel
	//is using to listen for public connections.
	//e.g. test.tnl.goliat.one:443
	Address string

	//Local port we are going to expose to the
	//outside world. We will be receiving incoming
	//proxy requests through the tunnel
	Port string

	//Auth token. Also will be used as an
	//identifier by sever to store tunnel connections
	Token string

	//Read only channel on which connections
	//current state is transmitted to.
	//You assign a listener to this channel to
	//get visibility on stuff.
	State chan<- *ClientState

	session *yamux.Session

	requestWaitGroup    sync.WaitGroup
	connectionWaitGroup sync.WaitGroup
}

//Init the client making sure
//we validate configurations and we
//can stablish a connection.
func (c *Client) Init() error {
	//Validate remote host is reachable
	_, err := net.Dial("tcp", c.Address)
	if err != nil {
		return err
	}

	//Validate local host is reachable
	_, err = net.Dial("tcp", ":"+c.Port)
	if err != nil {
		return err
	}

	return nil
}

//update client connection states
func (c *Client) changeState(value ClientState) {
	newState := value
	if c.State != nil {
		c.State <- &newState
	} else {
		c.State = make(chan *ClientState)
		c.State <- &newState
	}
}

//Connect
func (c *Client) Connect() error {
	c.changeState(Connecting)

	conf := &tls.Config{
		InsecureSkipVerify: true,
	}

	conn, err := tls.Dial("tcp", c.Address, conf)
	if err != nil {
		return err
	}
	remoteURL := fmt.Sprint(scheme(conn), "://", conn.RemoteAddr(), connectionPath)
	req, err := http.NewRequest(http.MethodConnect, remoteURL, nil)
	if err != nil {
		return fmt.Errorf("error creating request to %s: %w", remoteURL, err)
	}

	//set the auth token in the tunnel identification header
	req.Header.Set(TokenHeader, c.Token)

	if err := req.Write(conn); err != nil {
		return fmt.Errorf("failed sending CONNECT request to %s: %w", req.URL, err)
	}

	//read the server's response for the creation request
	resp, err := http.ReadResponse(bufio.NewReader(conn), req)
	if err != nil {
		return fmt.Errorf("failed reading CONNECT response from %s: %w", req.URL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK || resp.Status != TunnelConnected {
		out, err := ioutil.ReadAll(resp.Body)
		if err != nil {
			return fmt.Errorf("tunnel server error: status=%d, error=%w", resp.StatusCode, err)
		}
		return fmt.Errorf("tunnel server error: status=%d, body=%s", resp.StatusCode, string(out))
	}

	c.connectionWaitGroup.Wait()

	c.session, err = yamux.Client(conn, nil)
	if err != nil {
		return fmt.Errorf("error creating client: %w", err)
	}

	//we want to open a new stream with server
	var stream net.Conn
	openStream := func() error {
		//this will block until server accepts our session
		stream, err = c.session.Open()
		return err
	}

	//make sure we timeout if we don't hear from server in 10 sec
	select {
	case err := <-makeChan(openStream):
		if err != nil {
			return fmt.Errorf("failed to open session: %w", err)
		}
	case <-time.After(time.Second * 10):
		if stream != nil {
			stream.Close()
		}
		return errors.New("opening session timed out")
	}

	//send handshake request
	if _, err := stream.Write([]byte(HandshakeRequest)); err != nil {
		return fmt.Errorf("failed to write handshake: %w", err)
	}

	//read the server's response. We expect an OK response
	buf := make([]byte, len(HandshakeResponse))
	if _, err := stream.Read(buf); err != nil {
		return fmt.Errorf("failed to read handshake response: %w", err)
	}

	//ensure we got an OK response
	if string(buf) != HandshakeResponse {
		return fmt.Errorf("invalid handshake response: %s", string(buf))
	}

	//With the handshake complete, we have
	//a tunnel.
	tunnelConnection := connection{
		dec: json.NewDecoder(stream),
		enc: json.NewEncoder(stream),
		conn: stream,
		host: c.Address,
		port: c.Port.
	}

	c.changeState(Connected)

	return c.listen(&connection)
}


func (c *Client) listen(conn *connection) error {
	c.connectionWaitGroup.Add(1)
	defer c.connectionWaitGroup.Done()

	for {
		var msg Protocol
		if err := conn.dec.Decode(&msg); err != nil {
			//wait until all request are done
			c.requestWaitGroup.Wait()
			c.session.GoAway()
			c.session.Close()

			c.changeState(Disconnected)
			return fmt.Errorf("failed to unmarshal message from server: %w", err)
		}

		switch msg.Action {
		case RequestClientSession:
			remote, err := c.session.Open()
			if err != nil {
				return fmt.Errorf("failed to open session: %w", err)
			}

			go func() {
				defer remote.Close()
				//Tunnel the remote request to local reverse proxy
				if err := c.tunnel(remote); err != nil {
					log.Println(err)
					log.Println("failed to proxy data through tunnel")
				}

			}()
		}
	}
}

func (c *Client)  tunnel(removeConnection net.Conn) error {
	localConnection, err := net.Dial("tcp", ":"+c.Port)
	if err != nil {
		return fmt.Errorf("error stablishing local connection: %w", err)
	}
	defer localConnection.Close()

	c.requestWaitGroup.Add(2)
	//proxy requests
	go proxy(localConnection, remoteConnection, &c.requestWaitGroup)
	go proxy(remoteConnection, localConnection, &c.requestWaitGroup)

	//wait for all data to be transferred before closing stream
	c.requestWaitGroup.Wait()

	return nil
}

func proxy(dst, src net.Conn, wg *sync.WaitGroup) {
	defer wg.Done()
	io.Copy(dst, src)
}