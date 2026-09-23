package imapclient

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadConfigUsesAppleDefaults(t *testing.T) {
	config, err := LoadConfig(func(key string) string {
		values := map[string]string{
			"IMAP_USERNAME": "user@example.com",
			"IMAP_PASSWORD": "app-password",
		}
		return values[key]
	})
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if config.Username != "user@example.com" || config.Password != "app-password" {
		t.Fatalf("unexpected config: %+v", config)
	}
	if config.Host != Host || config.Port != Port || config.TLSServerName != Host {
		t.Fatalf("unexpected Apple endpoint: %+v", config)
	}
}

func TestLoadConfigUsesConfiguredEndpoint(t *testing.T) {
	config, err := LoadConfig(func(key string) string {
		values := map[string]string{
			"IMAP_HOST":            "imap.example.test",
			"IMAP_PORT":            "1993",
			"IMAP_TLS_SERVER_NAME": "mail.example.test",
			"IMAP_USERNAME":        "user@example.com",
			"IMAP_PASSWORD":        "app-password",
		}
		return values[key]
	})
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if config.Host != "imap.example.test" || config.Port != 1993 || config.TLSServerName != "mail.example.test" {
		t.Fatalf("unexpected endpoint config: %+v", config)
	}
}

func TestLoadConfigRejectsMissingOrInjectedCredentials(t *testing.T) {
	for _, values := range []map[string]string{
		{},
		{"IMAP_USERNAME": "user", "IMAP_PASSWORD": ""},
		{"IMAP_USERNAME": "user\r\nX", "IMAP_PASSWORD": "pass"},
		{"IMAP_USERNAME": "user", "IMAP_PASSWORD": "pass\x00x"},
		{"IMAP_HOST": "bad host", "IMAP_USERNAME": "user", "IMAP_PASSWORD": "pass"},
		{"IMAP_PORT": "not-a-port", "IMAP_USERNAME": "user", "IMAP_PASSWORD": "pass"},
		{"IMAP_PORT": "0", "IMAP_USERNAME": "user", "IMAP_PASSWORD": "pass"},
		{"IMAP_PORT": "65536", "IMAP_USERNAME": "user", "IMAP_PASSWORD": "pass"},
		{"IMAP_TLS_SERVER_NAME": "bad name", "IMAP_USERNAME": "user", "IMAP_PASSWORD": "pass"},
	} {
		_, err := LoadConfig(func(key string) string { return values[key] })
		if err != ErrInvalidConfiguration {
			t.Fatalf("values %#v returned %v, want ErrInvalidConfiguration", values, err)
		}
	}
}

func TestLoadDotEnvFillsMissingValuesWithoutOverridingEnvironment(t *testing.T) {
	t.Setenv("DOTENV_TEST_EXISTING", "from-process")
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte("DOTENV_TEST_NEW='from-file'\nDOTENV_TEST_EXISTING=from-file\n# comment\n"), 0600); err != nil {
		t.Fatalf("write test dotenv: %v", err)
	}
	if err := LoadDotEnv(path); err != nil {
		t.Fatalf("LoadDotEnv: %v", err)
	}
	if got := os.Getenv("DOTENV_TEST_NEW"); got != "from-file" {
		t.Fatalf("new value = %q", got)
	}
	if got := os.Getenv("DOTENV_TEST_EXISTING"); got != "from-process" {
		t.Fatalf("existing value = %q", got)
	}
}

func TestLoadDotEnvMissingFileIsOptional(t *testing.T) {
	if err := LoadDotEnv(filepath.Join(t.TempDir(), "missing.env")); err != nil {
		t.Fatalf("missing dotenv: %v", err)
	}
}
