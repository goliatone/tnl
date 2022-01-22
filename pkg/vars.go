package tnl

//Action represents a tunneled connection
type Action int

const (
	connectionPath = "/_connectPath"
	//TokenHeader is the header with the auth token/tunnel identifier
	TokenHeader = "X-TNL-Tunnel-Token"
	//TunnelConnected is the response sent by the server to the client
	//upon stablishing a connection
	TunnelConnected = "200 TNL Tunnel Established"

	TunnelConnectedResponse = "HTTP/1.1 " + TunnelConnected + "\n\n"

	//HandshakeRequest is the message sent by client to server to
	//stablish a connection
	HandshakeRequest = "tnl-handshake-request"
	//HandshakeResponse is the response sent by the server after
	//stablishing a connection
	HandshakeResponse = "tnl-handshake-ok"
	//RequestClientSession is the action to stablish a connection
	RequestClientSession Action = iota + 1
)
