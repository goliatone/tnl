package tnl

//Type holds tunneled connection
type Type int

const (
	HTTP Type = iota + 1
	TCP
	WS

	Requested Type = iota
	Accepted
	Established
)

//Protocol has the tunnelling protocol
type Protocol struct {
	Type   Type
	Action Action
}
