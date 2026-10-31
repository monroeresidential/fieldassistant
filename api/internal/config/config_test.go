package config

import (
	"encoding/base64"
	"strings"
	"testing"
	"time"
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
		"ANTHROPIC_API_KEY":     "sk-ant-test",
		"LLM_EXTRACT_MODEL":     "claude-sonnet-5-5",

		"OBJECT_STORE_ENDPOINT":   "http://localhost:9000",
		"OBJECT_STORE_REGION":     "us-east-1",
		"OBJECT_STORE_BUCKET":     "fieldassistant-attachments",
		"OBJECT_STORE_ACCESS_KEY": "fa-minio",
		"OBJECT_STORE_SECRET_KEY": "fafafafa",
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

func TestLoad_LLMRequired(t *testing.T) {
	env := validEnv()
	delete(env, "ANTHROPIC_API_KEY")
	delete(env, "LLM_EXTRACT_MODEL")

	_, err := Load(getter(env))
	if err == nil {
		t.Fatal("expected error")
	}
	for _, name := range []string{"ANTHROPIC_API_KEY", "LLM_EXTRACT_MODEL"} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("error %q does not mention %s", err, name)
		}
	}
}

func TestLoad_LLMTimeoutDefault(t *testing.T) {
	cfg, err := Load(getter(validEnv()))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.LLM.Timeout != 20*time.Second {
		t.Errorf("LLM.Timeout = %v, want 20s", cfg.LLM.Timeout)
	}
	if cfg.LLM.APIKey != "sk-ant-test" || cfg.LLM.ExtractModel != "claude-sonnet-5-5" {
		t.Errorf("LLM fields not populated: %+v", cfg.LLM)
	}
}

func TestLoad_LLMTimeoutInvalid(t *testing.T) {
	for _, v := range []string{"abc", "0", "-5"} {
		env := validEnv()
		env["LLM_TIMEOUT_SECONDS"] = v
		_, err := Load(getter(env))
		if err == nil || !strings.Contains(err.Error(), "LLM_TIMEOUT_SECONDS") {
			t.Errorf("value %q: want error mentioning LLM_TIMEOUT_SECONDS, got %v", v, err)
		}
	}
}

var objectStoreVars = []string{
	"OBJECT_STORE_ENDPOINT",
	"OBJECT_STORE_REGION",
	"OBJECT_STORE_BUCKET",
	"OBJECT_STORE_ACCESS_KEY",
	"OBJECT_STORE_SECRET_KEY",
}

func TestConfig_MissingObjectStore_Fails(t *testing.T) {
	for _, name := range objectStoreVars {
		env := validEnv()
		delete(env, name)
		_, err := Load(getter(env))
		if err == nil || !strings.Contains(err.Error(), name) {
			t.Errorf("without %s: want error naming it, got %v", name, err)
		}
	}
}

func TestLoad_ObjectStore(t *testing.T) {
	cfg, err := Load(getter(validEnv()))
	if err != nil {
		t.Fatal(err)
	}
	want := ObjectStoreConfig{
		Endpoint:  "http://localhost:9000",
		Region:    "us-east-1",
		Bucket:    "fieldassistant-attachments",
		AccessKey: "fa-minio",
		SecretKey: "fafafafa",
	}
	if cfg.ObjectStore != want {
		t.Errorf("ObjectStore = %+v, want %+v", cfg.ObjectStore, want)
	}
}

func TestLoad_ObjectStoreCreateBucket(t *testing.T) {
	for v, want := range map[string]bool{"true": true, "1": true, "false": false, "0": false} {
		env := validEnv()
		env["OBJECT_STORE_CREATE_BUCKET"] = v
		cfg, err := Load(getter(env))
		if err != nil {
			t.Fatalf("value %q: %v", v, err)
		}
		if cfg.ObjectStore.CreateBucket != want {
			t.Errorf("value %q: CreateBucket = %v, want %v", v, cfg.ObjectStore.CreateBucket, want)
		}
	}
}

func TestLoad_ObjectStoreCreateBucketInvalid(t *testing.T) {
	env := validEnv()
	env["OBJECT_STORE_CREATE_BUCKET"] = "maybe"
	_, err := Load(getter(env))
	if err == nil || !strings.Contains(err.Error(), "OBJECT_STORE_CREATE_BUCKET") {
		t.Fatalf("want error naming OBJECT_STORE_CREATE_BUCKET, got %v", err)
	}
}

func TestLoad_FeaturesProbe(t *testing.T) {
	cfg, err := Load(getter(validEnv()))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.FeaturesProbe != "rfi_managers" {
		t.Errorf("default FeaturesProbe = %q, want rfi_managers", cfg.FeaturesProbe)
	}

	env := validEnv()
	env["FEATURES_PROBE"] = "create_outcome"
	if cfg, err = Load(getter(env)); err != nil || cfg.FeaturesProbe != "create_outcome" {
		t.Errorf("FEATURES_PROBE=create_outcome: cfg %q err %v", cfg.FeaturesProbe, err)
	}

	env["FEATURES_PROBE"] = "guess"
	if _, err := Load(getter(env)); err == nil || !strings.Contains(err.Error(), "FEATURES_PROBE") {
		t.Errorf("FEATURES_PROBE=guess: want an error mentioning FEATURES_PROBE, got %v", err)
	}
}
