// Package config loads service configuration from environment variables.
package config

import (
	"encoding/base64"
	"fmt"
	"regexp"
	"strings"
)

const (
	vaultKeyLen      = 32
	sessionSecretMin = 32
	defaultPort      = "8080"

	defaultAppRedirectScheme = "fieldassistant"
)

// schemePattern is the lowercase subset of an RFC 3986 URI scheme.
var schemePattern = regexp.MustCompile(`^[a-z][a-z0-9+.-]*$`)

// ProcoreConfig holds the Procore OAuth and API settings.
type ProcoreConfig struct {
	ClientID, ClientSecret, LoginURL, APIURL, RedirectURL string
}

// Config is the validated service configuration.
type Config struct {
	Port          string
	DatabaseURL   string
	PublicBaseURL string
	// AppRedirectScheme is the URL scheme the auth callback redirects to.
	AppRedirectScheme string
	Procore           ProcoreConfig
	TokenVaultKey     []byte
	SessionSecret     []byte
}

// Load reads configuration through getenv and validates it.
func Load(getenv func(string) string) (Config, error) {
	var missing []string
	req := func(name string) string {
		v := getenv(name)
		if v == "" {
			missing = append(missing, name)
		}
		return v
	}

	cfg := Config{
		Port:              getenv("PORT"),
		DatabaseURL:       req("DATABASE_URL"),
		PublicBaseURL:     req("PUBLIC_BASE_URL"),
		AppRedirectScheme: getenv("APP_REDIRECT_SCHEME"),
		Procore: ProcoreConfig{
			ClientID:     req("PROCORE_CLIENT_ID"),
			ClientSecret: req("PROCORE_CLIENT_SECRET"),
			LoginURL:     req("PROCORE_LOGIN_URL"),
			APIURL:       req("PROCORE_API_URL"),
			RedirectURL:  req("PROCORE_REDIRECT_URL"),
		},
	}
	vaultKey := req("TOKEN_VAULT_KEY")
	sessionSecret := req("SESSION_SECRET")

	if len(missing) > 0 {
		return Config{}, fmt.Errorf("missing required env: %s", strings.Join(missing, ", "))
	}
	if cfg.Port == "" {
		cfg.Port = defaultPort
	}
	if cfg.AppRedirectScheme == "" {
		cfg.AppRedirectScheme = defaultAppRedirectScheme
	}
	if !schemePattern.MatchString(cfg.AppRedirectScheme) {
		return Config{}, fmt.Errorf("APP_REDIRECT_SCHEME %q is not a valid URL scheme (want lowercase letters, digits, +, . or -, starting with a letter)", cfg.AppRedirectScheme)
	}

	var err error
	if cfg.TokenVaultKey, err = decodeKey("TOKEN_VAULT_KEY", vaultKey); err != nil {
		return Config{}, err
	}
	if len(cfg.TokenVaultKey) != vaultKeyLen {
		return Config{}, fmt.Errorf("TOKEN_VAULT_KEY must decode to exactly %d bytes, got %d", vaultKeyLen, len(cfg.TokenVaultKey))
	}
	if cfg.SessionSecret, err = decodeKey("SESSION_SECRET", sessionSecret); err != nil {
		return Config{}, err
	}
	if len(cfg.SessionSecret) < sessionSecretMin {
		return Config{}, fmt.Errorf("SESSION_SECRET must decode to at least %d bytes, got %d", sessionSecretMin, len(cfg.SessionSecret))
	}
	return cfg, nil
}

func decodeKey(name, value string) ([]byte, error) {
	b, err := base64.StdEncoding.DecodeString(value)
	if err != nil {
		return nil, fmt.Errorf("%s is not valid base64: %w", name, err)
	}
	return b, nil
}
