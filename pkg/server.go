package tnl

import (
	"bufio"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/hashicorp/yamux"
)

//Server is responsible for proxying public connections to
//the client over a tunnel connection. It also listens in to
//control messages from the client.
type Server struct {
	//All connections with multiple clients
	connections Connections
	// each session per host
	//yamux session lets us multiplex
	sessions sessions

	configuration *ServerConfig
}

//ServerConfig will be provided by the user
type ServerConfig struct {
	//decorates http requests before being
	//forwarded to the client, can be nil.
	Director func(*http.Request) //TODO: rename to Decorator

	//Auth middleware for tunnels.
	// If error is nil, authentication OK, and server will
	// continue with the tunnel creation procedure.
	// If error is NOT nil, server returns the error
	// to the client, without proceeding ahead with hijacking.
	Auth func(*http.Request) error

	//Server address the server is listening for incoming requests
	//e.g. tnl.goliat.one:443
	Address string

	//TLS certificate file. If present we listen for HTTPS
	Certificate string

	// Certificate key file
	Key string

	InsecureSkipVerify bool
}

func StartServer(config *ServerConfig) error {
	server := &Server{
		configuration: config,
		sessions: sessions{
			mapping: make(map[string]*yamux.Session),
		},
	}

	if config.Certificate != "" && config.Key != "" {
		if !pathExists(config.Certificate) || !pathExists(config.Key) {
			return errors.New("certificate or key not found")
		}

		http.DefaultTransport.(*http.Transport).TLSClientConfig = &tls.Config{
			InsecureSkipVerify: config.InsecureSkipVerify,
		}

		//Start HTTPS server
		return http.ListenAndServeTLS(config.Address, config.Certificate, config.Key, server)
	}

	//Start HTTP server
	return http.ListenAndServe(config.Address, server)
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {

	//we provided a Director function used to decorate requests
	//before tunneling. Used to decorate/modify or track
	if s.configuration.Director != nil {
		s.configuration.Director(r)
	}

	//TODO: validate URL for real
	if strings.ToLower(r.Host) == "" {
		http.Error(w, "wrong url", http.StatusBadRequest)
	}

	switch filepath.Clean(r.URL.Path) {
	case connectionPath:
		if r.Method != http.MethodConnect {
			http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
			return
		}
		if err := s.tunnelCreationHandler(w, r); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
		}
	default:
		s.handleHTTP(w, r)
	}
}

func (s *Server) tunnelCreationHandler(w http.ResponseWriter, r *http.Request) (ctErr error) {
	log.Println("Initialize tunnel creation...")

	token := r.Header.Get(TokenHeader)
	if token == "" {
		return errors.New("missing token")
	}

	if conn := s.connections.exists(token); conn {
		return errors.New("tunnel already exists")
	}

	//If we have auth function then run that here
	if s.configuration.Auth != nil {
		if err := s.configuration.Auth(r); err != nil {
			return err
		}
	}

	hj, ok := w.(http.Hijacker)
	if !ok {
		return fmt.Errorf("webserver does not support hijacking: %T", w)
	}

	conn, _, err := hj.Hijack()
	if err != nil {
		return fmt.Errorf("hijack failure: %w", err)
	}

	if _, err := io.WriteString(conn, TunnelConnectedResponse); err != nil {
		return fmt.Errorf("error writing response: %w", err)
	}

	if err := conn.SetDeadline(time.Time{}); err != nil {
		return fmt.Errorf("error setting connection deadline: %w", err)
	}

	log.Println("Start new session...")
	session, err := yamux.Server(conn, nil)
	if err != nil {
		return fmt.Errorf("error creating session: %w", err)
	}

	s.sessions.add(token, session)

	var stream net.Conn

	defer func() {
		if ctErr != nil {
			if stream != nil {
				stream.Close()
			}
			s.sessions.delete(token)
		}
	}()

	acceptStream := func() error {
		stream, err = session.Accept()
		return err
	}

	select {
	case err := <-makeChan(acceptStream):
		if err != nil {
			return err
		}
	case <-time.After(time.Second * 10):
		return errors.New("timeout getting session")
	}

	log.Println("Initiate handshake dance...")
	buf := make([]byte, len(HandshakeRequest))
	if _, err := stream.Read(buf); err != nil {
		return fmt.Errorf("error reading handshake response: %w", err)
	}

	if string(buf) != HandshakeRequest {
		return fmt.Errorf("handshake request failure: %s", string(buf))
	}

	if _, err := stream.Write([]byte(HandshakeResponse)); err != nil {
		return fmt.Errorf("handshake response error: %w", err)
	}

	connection := connection{
		dec:   json.NewDecoder(stream),
		enc:   json.NewEncoder(stream),
		conn:  stream,
		token: token,
		host:  r.URL.Hostname(),
	}

	s.connections.Add(connection)

	go s.listen(&connection)

	log.Printf("[server] Tunnel established successfully for host %s", connection.host)

	return nil
}

func (s *Server) handleHTTP(w http.ResponseWriter, r *http.Request) {
	host := r.URL.Hostname()

	stream, err := s.dial(host)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	defer func() {
		log.Println("Closing stream...")
		stream.Close()
	}()

	log.Println("Session opened by client, writing request to client")
	if err := r.Write(stream); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}

	log.Println("Waiting for tunneled response of the request from client")
	resp, err := http.ReadResponse(bufio.NewReader(stream), r)
	if err != nil {
		http.Error(w, "read from tunnel: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	//Return the response from the client, on the response writer
	copyHeader(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}

func (s *Server) listen(c *connection) {
	for {
		var msg map[string]interface{}
		err := c.dec.Decode(&msg)
		if err != nil {
			c.Close()

			s.connections.delete(*c)
			log.Println("Deleting connection", c.host)

			if err != io.EOF {
				log.Printf("decode err: %w", err)
			}
			return
		}

		//We are not doing anything with the message at the moment.
		//The underlying connection needs to be established, if there
		//is an error we can handle cleanup
		log.Printf("msg: %s", msg)
	}
}

func (s *Server) dial(host string) (net.Conn, error) {
	conn := s.connections.get(host)

	if conn.token == "" {
		return nil, errors.New("no tunnel exists for this host")
	}

	session, err := s.sessions.get(conn.token)
	if err != nil {
		return nil, fmt.Errorf("no tunnel exists for this host: %w", err)
	}

	msg := Protocol{
		Action: RequestClientSession,
		Type:   HTTP,
	}

	log.Println("Requesting session from client...")

	//ask client to open a session to us, so we can accept it
	if err := conn.send(msg); err != nil {
		//What happened? stream was closed, the session is shutting
		//down, the underlying connection could be broken... either
		//way, we bail
		conn.Close()
		s.connections.delete(*conn)
		return nil, err
	}

	var stream net.Conn
	acceptStream := func() error {
		stream, err = session.Accept()
		return err
	}
	log.Println("Waiting to accept incoming session")

	select {
	case err := <-makeChan(acceptStream):
		return stream, err
	case <-time.After(time.Second * 10):
		return nil, errors.New("timeout getting session")
	}
}
