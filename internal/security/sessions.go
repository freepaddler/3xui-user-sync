package security

import (
	"context"
	"sync"
	"time"
)

type Session struct {
	ID         string
	Username   string
	CreatedAt  time.Time
	LastSeenAt time.Time
	ExpiresAt  time.Time
}

type SessionStore struct {
	mu          sync.RWMutex
	sessions    map[string]Session
	sessionTTL  time.Duration
	idleTimeout time.Duration
}

func NewSessionStore(sessionTTL, idleTimeout time.Duration) *SessionStore {
	return &SessionStore{
		sessions:    make(map[string]Session),
		sessionTTL:  sessionTTL,
		idleTimeout: idleTimeout,
	}
}

func (s *SessionStore) Create(_ context.Context, username string, rememberTTL time.Duration, remember bool) (Session, error) {
	id, err := NewSessionID()
	if err != nil {
		return Session{}, err
	}
	now := time.Now().UTC()
	expiresAt := now.Add(s.sessionTTL)
	if remember && rememberTTL > expiresAt.Sub(now) {
		expiresAt = now.Add(rememberTTL)
	}
	session := Session{
		ID:         id,
		Username:   username,
		CreatedAt:  now,
		LastSeenAt: now,
		ExpiresAt:  expiresAt,
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[id] = session
	return session, nil
}

func (s *SessionStore) Get(_ context.Context, id string) (Session, bool) {
	s.mu.RLock()
	session, ok := s.sessions[id]
	s.mu.RUnlock()
	if !ok {
		return Session{}, false
	}
	if time.Now().UTC().After(session.ExpiresAt) {
		s.Delete(context.Background(), id)
		return Session{}, false
	}
	if s.idleTimeout > 0 && time.Since(session.LastSeenAt) > s.idleTimeout {
		s.Delete(context.Background(), id)
		return Session{}, false
	}
	return session, true
}

func (s *SessionStore) Touch(_ context.Context, id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	session, ok := s.sessions[id]
	if !ok {
		return
	}
	session.LastSeenAt = time.Now().UTC()
	s.sessions[id] = session
}

func (s *SessionStore) Delete(_ context.Context, id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, id)
}
