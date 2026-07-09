package services

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	defaultWebAccessBindHost = "0.0.0.0"
	defaultWebAccessPort     = 3740
	legacyWebAccessPort      = 8765
)

type WebAccessSettings struct {
	Enabled     bool   `json:"enabled"`
	BindHost    string `json:"bindHost"`
	Port        int    `json:"port"`
	AccessToken string `json:"accessToken"`
	EnableTLS   bool   `json:"enableTLS"`
}

type WebAccessStatus struct {
	Enabled     bool     `json:"enabled"`
	Running     bool     `json:"running"`
	BindHost    string   `json:"bindHost"`
	Port        int      `json:"port"`
	AccessToken string   `json:"accessToken"`
	PrimaryURL  string   `json:"primaryUrl"`
	LANURLs     []string `json:"lanUrls"`
	EnableTLS   bool     `json:"enableTLS"`
	LastError   string   `json:"lastError,omitempty"`
}

type RuntimeStatus struct {
	ActiveKanbanWorkspaceIDs []string `json:"activeKanbanWorkspaceIds"`
}

type WorkspaceIconInput struct {
	Name      string `json:"name,omitempty"`
	MediaType string `json:"mediaType,omitempty"`
	DataURL   string `json:"dataUrl"`
	Bytes     int64  `json:"bytes,omitempty"`
}

type WebAccessController interface {
	ApplyWebAccessSettings(settings WebAccessSettings) (WebAccessStatus, error)
	LoadWebAccessStatus(settings WebAccessSettings) WebAccessStatus
}

func defaultWebAccessSettings() WebAccessSettings {
	return WebAccessSettings{
		Enabled:     false,
		BindHost:    defaultWebAccessBindHost,
		Port:        defaultWebAccessPort,
		AccessToken: newWebAccessToken(),
	}
}

func normalizeWebAccessSettings(settings WebAccessSettings, fallbackToken string) WebAccessSettings {
	settings.BindHost = strings.TrimSpace(settings.BindHost)
	if settings.BindHost == "" {
		settings.BindHost = defaultWebAccessBindHost
	}
	if settings.Port <= 0 || settings.Port > 65535 {
		settings.Port = defaultWebAccessPort
	}
	settings.AccessToken = strings.TrimSpace(settings.AccessToken)
	if settings.AccessToken == "" {
		settings.AccessToken = strings.TrimSpace(fallbackToken)
	}
	if settings.AccessToken == "" {
		settings.AccessToken = newWebAccessToken()
	}
	return settings
}

func migrateWebAccessDefaultPort(settings WebAccessSettings) (WebAccessSettings, bool) {
	if !settings.Enabled && settings.Port == legacyWebAccessPort {
		settings.Port = defaultWebAccessPort
		return settings, true
	}
	return settings, false
}

func validateWebAccessSettings(settings WebAccessSettings) error {
	if settings.Port <= 0 || settings.Port > 65535 {
		return fmt.Errorf("web access port must be between 1 and 65535")
	}
	if strings.TrimSpace(settings.BindHost) == "" {
		return fmt.Errorf("web access bind host is required")
	}
	if net.ParseIP(settings.BindHost) == nil && strings.ToLower(settings.BindHost) != "localhost" {
		return fmt.Errorf("web access bind host must be an IP address or localhost")
	}
	if strings.TrimSpace(settings.AccessToken) == "" {
		return fmt.Errorf("web access token is required")
	}
	return nil
}

func newWebAccessToken() string {
	var data [32]byte
	if _, err := rand.Read(data[:]); err != nil {
		return base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf("echo-web-access-%d", len(data))))
	}
	return base64.RawURLEncoding.EncodeToString(data[:])
}

func SetWebAccessController(service *SystemService, controller WebAccessController) {
	service.setWebAccessController(controller)
}

func (s *SystemService) setWebAccessController(controller WebAccessController) {
	s.mu.Lock()
	s.webAccessController = controller
	s.mu.Unlock()
}

func (s *SystemService) SaveWebAccessSettings(settings WebAccessSettings) (AppState, error) {
	s.mu.Lock()
	current := s.state.WebAccess
	settings = normalizeWebAccessSettings(settings, current.AccessToken)
	if err := validateWebAccessSettings(settings); err != nil {
		s.mu.Unlock()
		return AppState{}, err
	}
	controller := s.webAccessController
	s.mu.Unlock()

	if controller != nil {
		if _, err := controller.ApplyWebAccessSettings(settings); err != nil {
			return AppState{}, err
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.state.WebAccess = settings
	s.refreshWorkspaceStatusesLocked()
	if err := s.saveLocked(); err != nil {
		return AppState{}, err
	}
	return cloneState(s.state), nil
}

func (s *SystemService) LoadWebAccessStatus() WebAccessStatus {
	s.mu.Lock()
	settings := normalizeWebAccessSettings(s.state.WebAccess, "")
	controller := s.webAccessController
	s.mu.Unlock()
	if controller == nil {
		return webAccessStatusFromSettings(settings)
	}
	return controller.LoadWebAccessStatus(settings)
}

func (s *SystemService) RotateWebAccessToken() (AppState, error) {
	s.mu.Lock()
	settings := normalizeWebAccessSettings(s.state.WebAccess, "")
	settings.AccessToken = newWebAccessToken()
	s.mu.Unlock()
	return s.SaveWebAccessSettings(settings)
}

func (s *SystemService) LoadRuntimeStatus() RuntimeStatus {
	s.chatMu.Lock()
	defer s.chatMu.Unlock()
	workspaceIDs := make([]string, 0, len(s.kanbanRuns))
	for workspaceID := range s.kanbanRuns {
		workspaceIDs = append(workspaceIDs, workspaceID)
	}
	return RuntimeStatus{ActiveKanbanWorkspaceIDs: workspaceIDs}
}

func WebAccessTokenAllowed(service *SystemService, token string) bool {
	return service.isWebAccessTokenAllowed(token)
}

func (s *SystemService) isWebAccessTokenAllowed(token string) bool {
	token = strings.TrimSpace(token)
	if token == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	settings := normalizeWebAccessSettings(s.state.WebAccess, "")
	return settings.Enabled && token == settings.AccessToken
}

func webAccessStatusFromSettings(settings WebAccessSettings) WebAccessStatus {
	settings = normalizeWebAccessSettings(settings, "")
	return WebAccessStatus{
		Enabled:     settings.Enabled,
		Running:     false,
		BindHost:    settings.BindHost,
		Port:        settings.Port,
		AccessToken: settings.AccessToken,
		EnableTLS:   settings.EnableTLS,
		LANURLs:     []string{},
	}
}

// GenerateSelfSignedCert creates a self-signed ECDSA P-256 certificate and key
// at the given paths. The cert is valid for 1 year and covers localhost DNS.
func GenerateSelfSignedCert(certPath, keyPath string) error {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return fmt.Errorf("generate ECDSA key: %w", err)
	}

	template := x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject: pkix.Name{
			Organization: []string{"Echo"},
			CommonName:   "localhost",
		},
		NotBefore:             time.Now(),
		NotAfter:              time.Now().Add(365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              []string{"localhost"},
	}

	certDER, err := x509.CreateCertificate(rand.Reader, &template, &template, &priv.PublicKey, priv)
	if err != nil {
		return fmt.Errorf("create certificate: %w", err)
	}

	certOut, err := os.Create(certPath)
	if err != nil {
		return fmt.Errorf("create cert file: %w", err)
	}
	defer certOut.Close()
	if err := pem.Encode(certOut, &pem.Block{Type: "CERTIFICATE", Bytes: certDER}); err != nil {
		return fmt.Errorf("encode cert PEM: %w", err)
	}

	keyOut, err := os.Create(keyPath)
	if err != nil {
		return fmt.Errorf("create key file: %w", err)
	}
	defer keyOut.Close()
	keyBytes, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		return fmt.Errorf("marshal EC private key: %w", err)
	}
	if err := pem.Encode(keyOut, &pem.Block{Type: "EC PRIVATE KEY", Bytes: keyBytes}); err != nil {
		return fmt.Errorf("encode key PEM: %w", err)
	}

	return nil
}

// WebAccessConfigDir returns the user config directory where Echo stores
// persistent data (state.json, TLS certs, etc.).
func WebAccessConfigDir() (string, error) {
	configDir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("get user config dir: %w", err)
	}
	return filepath.Join(configDir, "Echo"), nil
}
