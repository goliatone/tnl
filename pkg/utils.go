package tnl

import (
	"crypto/tls"
	"net"
	"net/http"
	"os"
)

func scheme(conn net.Conn) string {
	var protocol string
	switch conn.(type) {
	case *tls.Conn:
		protocol = "https"
	default:
		protocol = "http"
	}
	return protocol
}

func makeChan(fn func() error) <-chan error {
	errChan := make(chan error)
	go func() {
		select {
		case errChan <- fn():
		default:
		}
		close(errChan)
	}()
	return errChan
}

func pathExists(filepath string) bool {
	_, err := os.Stat(filepath)
	return err == nil
}

func copyHeader(dst, src http.Header) {
	for k, vv := range src {
		for _, v := range vv {
			dst.Add(k, v)
		}
	}
}
