package config

import (
	"encoding/base64"
	"strings"
	"testing"
)

func validEnv() map[string]string {
	return map[string]string{
		"DATABASE_URL":          "postgres://fa:fa@localhost:5432/fa",
		"PROCORE_CLIENT_ID":     "id",
		"PROCORE_CLIENT_SECRET": "secret",
		"PROCORE_LOGIN_URL":     "https://login.procore.test",
		"PROCORE_API_URL":       "https://api.procore.test",
		"PROCORE_REDIRECT_URL":  "http://localhost:8080/v1/auth/callback",
		"TOKEN_VAULT_KEY":       base64.StdEncoding.EncodeToString(make([]byte, 32)),
		"SESSION_SECRET":        base64.StdEncoding.EncodeToString(make([]byte, 32)),
		"PUBLIC_BASE_URL":       "http://localhost:8080",
	}
}

func getter(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestLoad_MissingRequired_Errors(t *testing.T) {
	env := validEnv()
	delete(env, "PROCORE_CLIENT_SECRET")
	delete(env, "DATABASE_URL")

	_, err := Load(getter(env))
	if err == nil {
		t.Fatal("expected error")
	}
	for _, name := range []string{"PROCORE_CLIENT_SECRET", "DATABASE_URL"} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("error %q does not mention %s", err, name)
		}
	}
}

func TestLoad_VaultKeyMustBe32Bytes(t *testing.T) {
	env := validEnv()
	env["TOKEN_VAULT_KEY"] = base64.StdEncoding.EncodeToString(make([]byte, 16))

	_, err := Load(getter(env))
	if err == nil || !strings.Contains(err.Error(), "TOKEN_VAULT_KEY") {
		t.Fatalf("want error mentioning TOKEN_VAULT_KEY, got %v", err)
	}
}

func TestLoad_InvalidBase64_Errors(t *testing.T) {
	env := validEnv()
	env["SESSION_SECRET"] = "not base64!!"

	_, err := Load(getter(env))
	if err == nil || !strings.Contains(err.Error(), "SESSION_SECRET") {
		t.Fatalf("want error mentioning SESSION_SECRET, got %v", err)
	}
}

func TestLoad_SessionSecretTooShort_Errors(t *testing.T) {
	env := validEnv()
	env["SESSION_SECRET"] = base64.StdEncoding.EncodeToString(make([]byte, 16))

	_, err := Load(getter(env))
	if err == nil || !strings.Contains(err.Error(), "SESSION_SECRET") {
		t.Fatalf("want error mentioning SESSION_SECRET, got %v", err)
	}
}

func TestLoad_Defaults(t *testing.T) {
	cfg, err := Load(getter(validEnv()))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Port != "8080" {
		t.Errorf("Port = %q, want 8080", cfg.Port)
	}
	if len(cfg.TokenVaultKey) != 32 || len(cfg.SessionSecret) != 32 {
		t.Errorf("key lengths = %d, %d", len(cfg.TokenVaultKey), len(cfg.SessionSecret))
	}
	if cfg.Procore.ClientID != "id" || cfg.DatabaseURL == "" || cfg.PublicBaseURL == "" {
		t.Errorf("fields not populated: %+v", cfg)
	}
}

func TestLoad_PortOverride(t *testing.T) {
	env := validEnv()
	env["PORT"] = "9090"
	cfg, err := Load(getter(env))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Port != "9090" {
		t.Errorf("Port = %q, want 9090", cfg.Port)
	}
}

func TestLoad_AppRedirectSchemeDefault(t *testing.T) {
	cfg, err := Load(getter(validEnv()))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AppRedirectScheme != "fieldassistant" {
		t.Errorf("AppRedirectScheme = %q, want fieldassistant", cfg.AppRedirectScheme)
	}
	env := validEnv()
	env["APP_REDIRECT_SCHEME"] = "fieldassistant-dev"
	cfg, err = Load(getter(env))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AppRedirectScheme != "fieldassistant-dev" {
		t.Errorf("AppRedirectScheme = %q, want fieldassistant-dev", cfg.AppRedirectScheme)
	}
}

func TestLoad_AppRedirectSchemeInvalid(t *testing.T) {
	env := validEnv()
	env["APP_REDIRECT_SCHEME"] = "Bad Scheme"
	_, err := Load(getter(env))
	if err == nil || !strings.Contains(err.Error(), "APP_REDIRECT_SCHEME") {
		t.Fatalf("err = %v, want error naming APP_REDIRECT_SCHEME", err)
	}
}
