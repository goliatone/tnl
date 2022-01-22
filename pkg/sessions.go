package tnl

import (
	"fmt"
	"sync"

	"github.com/hashicorp/yamux"
)

type sessions struct {
	sync.Mutex
	mapping map[string]*yamux.Session
}

func (s *sessions) get(identifier string) (*yamux.Session, error) {
	s.Lock()
	session, ok := s.mapping[identifier]
	s.Unlock()

	if !ok {
		return nil, fmt.Errorf("no session for identifier: %w", identifier)
	}

	return session, nil
}

func (s *sessions) add(identifier string, session *yamux.Session) {
	s.Lock()
	s.mapping[identifier] = session
	s.Unlock()
}

func (s *sessions) delete(identifier string) {
	s.Lock()
	defer s.Unlock()

	session, ok := s.mapping[identifier]
	if !ok {
		return
	}
	if session != nil {
		session.GoAway() //do not accept a new connection
		session.Close()
	}
	delete(s.mapping, identifier)
}
