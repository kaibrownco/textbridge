package gateway

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Client is an OAuth client registered through dynamic client registration.
type Client struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	RedirectURIs []string  `json:"redirect_uris"`
	CreatedAt    time.Time `json:"created_at"`
}

// Token is a stored access or refresh token. Only the SHA-256 of the token
// value is kept, so a leaked state file does not leak usable tokens.
type Token struct {
	ClientID  string    `json:"client_id"`
	Kind      string    `json:"kind"` // "access" or "refresh"
	ExpiresAt time.Time `json:"expires_at"`
}

type state struct {
	Clients map[string]*Client `json:"clients"`
	Tokens  map[string]*Token  `json:"tokens"` // key: sha256(token) hex
}

// Store keeps clients and tokens in memory and persists them to a JSON file.
type Store struct {
	mu   sync.Mutex
	path string
	s    state
	now  func() time.Time
}

const maxClients = 50

func OpenStore(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	st := &Store{
		path: filepath.Join(dir, "state.json"),
		s:    state{Clients: map[string]*Client{}, Tokens: map[string]*Token{}},
		now:  time.Now,
	}
	b, err := os.ReadFile(st.path)
	if err == nil {
		if err := json.Unmarshal(b, &st.s); err != nil {
			return nil, err
		}
		if st.s.Clients == nil {
			st.s.Clients = map[string]*Client{}
		}
		if st.s.Tokens == nil {
			st.s.Tokens = map[string]*Token{}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	return st, nil
}

// saveLocked writes state atomically. Caller holds mu.
func (st *Store) saveLocked() error {
	b, err := json.MarshalIndent(st.s, "", "  ")
	if err != nil {
		return err
	}
	tmp := st.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, st.path)
}

func (st *Store) pruneLocked() {
	now := st.now()
	for k, t := range st.s.Tokens {
		if now.After(t.ExpiresAt) {
			delete(st.s.Tokens, k)
		}
	}
}

func (st *Store) AddClient(name string, redirects []string) (*Client, error) {
	st.mu.Lock()
	defer st.mu.Unlock()
	if len(st.s.Clients) >= maxClients {
		// Drop the oldest registration so repeated reconnects can't fill the store.
		var oldest *Client
		for _, c := range st.s.Clients {
			if oldest == nil || c.CreatedAt.Before(oldest.CreatedAt) {
				oldest = c
			}
		}
		if oldest != nil {
			delete(st.s.Clients, oldest.ID)
		}
	}
	c := &Client{ID: randomToken(16), Name: name, RedirectURIs: redirects, CreatedAt: st.now()}
	st.s.Clients[c.ID] = c
	return c, st.saveLocked()
}

func (st *Store) Client(id string) (*Client, bool) {
	st.mu.Lock()
	defer st.mu.Unlock()
	c, ok := st.s.Clients[id]
	if !ok {
		return nil, false
	}
	cp := *c
	return &cp, true
}

// IssueToken creates a new token value of the given kind and stores its hash.
func (st *Store) IssueToken(clientID, kind string, ttl time.Duration) (string, error) {
	v := randomToken(32)
	st.mu.Lock()
	defer st.mu.Unlock()
	st.pruneLocked()
	st.s.Tokens[hashToken(v)] = &Token{ClientID: clientID, Kind: kind, ExpiresAt: st.now().Add(ttl)}
	return v, st.saveLocked()
}

// CheckToken reports whether value is a live token of the given kind.
func (st *Store) CheckToken(value, kind string) (*Token, bool) {
	if value == "" {
		return nil, false
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	t, ok := st.s.Tokens[hashToken(value)]
	if !ok || t.Kind != kind || st.now().After(t.ExpiresAt) {
		return nil, false
	}
	cp := *t
	return &cp, true
}

// ConsumeToken deletes a token and returns it if it was live. Used for
// single-use refresh tokens.
func (st *Store) ConsumeToken(value, kind string) (*Token, bool) {
	if value == "" {
		return nil, false
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	h := hashToken(value)
	t, ok := st.s.Tokens[h]
	if !ok || t.Kind != kind || st.now().After(t.ExpiresAt) {
		return nil, false
	}
	delete(st.s.Tokens, h)
	_ = st.saveLocked()
	return t, true
}

// RevokeAll removes every token (used by the "sign out everywhere" command).
func (st *Store) RevokeAll() error {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.s.Tokens = map[string]*Token{}
	return st.saveLocked()
}

func hashToken(v string) string {
	sum := sha256.Sum256([]byte(v))
	return hex.EncodeToString(sum[:])
}

func randomToken(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}
