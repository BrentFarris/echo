// Package swarm connects Echo hosts without sharing their execution scopes.
// Resource IDs remain local to their owner; NodeID supplies the namespace.
package swarm

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"
)

const Version = 1
const frameSize = 32 * 1024
const windowSize = 8

type Ref struct {
	NodeID string `json:"nodeId"`
	ID     string `json:"id"`
}

func Key(node, id string) string { return node + ":" + id }
func Split(key string) (string, string) {
	a, b, ok := strings.Cut(key, ":")
	if !ok {
		return "", key
	}
	return a, b
}

type Node struct {
	ID           string   `json:"nodeId"`
	Name         string   `json:"name"`
	URL          string   `json:"url,omitempty"`
	PublicKey    []byte   `json:"publicKey"`
	Version      int      `json:"version"`
	Epoch        string   `json:"epoch"`
	Capabilities []string `json:"capabilities"`
}

func capabilities() []string {
	return []string{"owner-api/1", "dispatch/1", "stream/1", "native-monaco/1", "native-terminal/1", "development-update/1", "fork-history/1"}
}
func compatible(n Node) bool {
	return n.Version == Version && slices.Contains(n.Capabilities, "owner-api/1") && slices.Contains(n.Capabilities, "dispatch/1") && slices.Contains(n.Capabilities, "stream/1")
}

func (n Node) Valid() bool {
	h := sha256.Sum256(n.PublicKey)
	return len(n.PublicKey) == 32 && n.ID == hex.EncodeToString(h[:16]) && namePattern.MatchString(n.Name)
}

var namePattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,47}$`)

func ValidName(name string) bool { return namePattern.MatchString(name) }
func HostName(name string) string {
	name = strings.ToLower(name)
	var out strings.Builder
	for _, c := range name {
		if c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_' {
			out.WriteRune(c)
		} else {
			out.WriteByte('-')
		}
	}
	name = strings.Trim(out.String(), "-_")
	if name == "" || name[0] < 'a' || name[0] > 'z' {
		name = "echo-" + name
	}
	if len(name) > 48 {
		name = name[:48]
	}
	return name
}

type Peer struct {
	Node       Node      `json:"node"`
	Connected  bool      `json:"connected"`
	Authorized bool      `json:"authorized"`
	Blocked    bool      `json:"blocked,omitempty"`
	Disabled   bool      `json:"disconnected,omitempty"`
	Error      string    `json:"error,omitempty"`
	LastSeen   time.Time `json:"lastSeen,omitempty"`
	// Secret references and grant hashes are never included in directory responses.
	Outbound    string  `json:"outbound,omitempty"`
	Inbound     string  `json:"inbound,omitempty"`
	InboundHash string  `json:"inboundHash,omitempty"`
	Discovery   string  `json:"discovery,omitempty"`
	PairID      string  `json:"pairId,omitempty"`
	Grants      []Grant `json:"grants,omitempty"`
}

type Grant struct {
	PairID   string `json:"pairId"`
	Inbound  string `json:"inbound"`
	Outbound string `json:"outbound"`
	Hash     string `json:"hash"`
}

type Route struct {
	Node Node     `json:"node"`
	Path []string `json:"path"`
}

type PairRequest struct {
	Version     int    `json:"version"`
	ID          string `json:"id"`
	Node        Node   `json:"node"`
	Password    string `json:"password"`
	ReturnToken string `json:"returnToken"`
	Activate    bool   `json:"activate"`
}
type PairResponse struct {
	Node  Node   `json:"node"`
	Token string `json:"token"`
}

type Records interface {
	Get(string, string, any) error
	Put(string, string, any) error
	Delete(string, string) error
	List(string) ([]json.RawMessage, error)
}
type Secrets interface {
	Get(context.Context, string) (string, error)
	Set(context.Context, string, string) error
	Delete(context.Context, string) error
}

type Config struct {
	Store         Records
	Secrets       Secrets
	SecretPrefix  string
	IdentityFile  string
	Name          string
	Epoch         string
	Enabled       func() bool
	Enable        func() error
	CheckPassword func(context.Context, string, string) error
	Handler       http.Handler
	Changed       func()
}

type Principal struct {
	NodeID  string
	Pairing bool
}
type principalKey struct{}

func Caller(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(Principal)
	return p, ok
}
func Owner(ctx context.Context) bool { p, ok := Caller(ctx); return ok && !p.Pairing }

var ErrUnavailable = errors.New("Echo instance is unavailable")
var ErrUnauthorized = errors.New("connect to this instance with its owner password")

type address string

func (a address) Network() string { return "swarm" }
func (a address) String() string  { return string(a) }

// A listener per virtual connection lets net/http retain its streaming,
// cancellation and WebSocket upgrade behavior without an HTTP buffering proxy.
type singleListener struct {
	conn     net.Conn
	accepted bool
	done     <-chan struct{}
}

func (l *singleListener) Accept() (net.Conn, error) {
	if !l.accepted {
		l.accepted = true
		return l.conn, nil
	}
	<-l.done
	return nil, net.ErrClosed
}
func (l *singleListener) Close() error   { return l.conn.Close() }
func (l *singleListener) Addr() net.Addr { return l.conn.LocalAddr() }
