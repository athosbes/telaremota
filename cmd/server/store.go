package main

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"sync"
)

// store guarda ID -> sha256(chave do dispositivo). Assim um agente mantém
// seu ID entre reinícios e ninguém consegue "sequestrar" o ID de outro.
type store struct {
	mu   sync.Mutex
	path string
	ids  map[string]string
}

func openStore(path string) (*store, error) {
	s := &store{path: path, ids: map[string]string{}}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	return s, json.Unmarshal(b, &s.ids)
}

func (s *store) claim(id, key string) (string, error) {
	if len(key) < 32 {
		return "", errors.New("chave de dispositivo inválida")
	}
	sum := sha256.Sum256([]byte(key))
	h := hex.EncodeToString(sum[:])

	s.mu.Lock()
	defer s.mu.Unlock()
	if validID(id) {
		cur, ok := s.ids[id]
		if ok && subtle.ConstantTimeCompare([]byte(cur), []byte(h)) == 1 {
			return id, nil
		}
		if !ok { // servidor perdeu os dados: devolve o ID ao dono
			s.ids[id] = h
			return id, s.save()
		}
	}
	for {
		id = newID()
		if _, taken := s.ids[id]; !taken {
			break
		}
	}
	s.ids[id] = h
	return id, s.save()
}

func (s *store) save() error {
	b, _ := json.MarshalIndent(s.ids, "", " ")
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func validID(id string) bool {
	if len(id) != 9 || id[0] == '0' {
		return false
	}
	for _, r := range id {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func newID() string {
	n, _ := rand.Int(rand.Reader, big.NewInt(900_000_000))
	return fmt.Sprintf("%d", n.Int64()+100_000_000)
}
