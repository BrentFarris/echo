package swarm

import (
	"encoding/base64"
	"encoding/json"
	"errors"
)

// Proof delegates exactly one resolved request from its browser entry point to
// a worker. A chat's home cannot replace the entry point's target authorization.
func (m *Manager) Proof(node, fingerprint string) (string, error) {
	if !m.Authorized(node) {
		return "", ErrUnauthorized
	}
	m.mu.Lock()
	peer := m.peers[node].Node
	m.mu.Unlock()
	p, err := m.seal(peer, randomID(), "dispatch-permit", []string{m.Node().ID, node}, []byte(fingerprint))
	if err != nil {
		return "", err
	}
	data, err := json.Marshal(p)
	return base64.RawURLEncoding.EncodeToString(data), err
}
func (m *Manager) VerifyProof(origin, proof, fingerprint string) error {
	if !m.Authorized(origin) {
		return ErrUnauthorized
	}
	data, err := base64.RawURLEncoding.DecodeString(proof)
	if err != nil {
		return errors.New("invalid dispatch delegation")
	}
	var p packet
	if json.Unmarshal(data, &p) != nil || p.From.ID != origin || p.To != m.Node().ID || p.Kind != "dispatch-permit" {
		return errors.New("dispatch delegation identity mismatch")
	}
	aead, err := m.aead(p.From)
	if err != nil {
		return err
	}
	if len(p.Nonce) != aead.NonceSize() {
		return errors.New("invalid dispatch delegation nonce")
	}
	plain, err := aead.Open(nil, p.Nonce, p.Data, associated(p))
	if err != nil || string(plain) != fingerprint {
		return errors.New("dispatch delegation does not match this request")
	}
	return nil
}
