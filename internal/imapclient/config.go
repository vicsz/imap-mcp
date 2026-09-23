package imapclient

import (
	"bufio"
	"errors"
	"net"
	"os"
	"strconv"
	"strings"
)

const (
	Host           = "imap.mail.me.com"
	Port           = 993
	DefaultMailbox = "INBOX"
)

type Config struct {
	Host          string
	Port          int
	TLSServerName string
	FromAddress   string
	Username      string
	Password      string
}

var (
	ErrInvalidConfiguration = errors.New("IMAP configuration is invalid")
	ErrConnectionFailed     = errors.New("IMAP connection failed")
)

func LoadConfig(getenv func(string) string) (Config, error) {
	host := getenv("IMAP_HOST")
	if host == "" {
		host = Host
	}
	port := Port
	if value := getenv("IMAP_PORT"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil {
			return Config{}, ErrInvalidConfiguration
		}
		port = parsed
	}
	tlsServerName := getenv("IMAP_TLS_SERVER_NAME")
	if tlsServerName == "" {
		tlsServerName = host
	}
	fromAddress := getenv("IMAP_FROM_ADDRESS")
	username := getenv("IMAP_USERNAME")
	password := getenv("IMAP_PASSWORD")
	config := Config{
		Host:          host,
		Port:          port,
		TLSServerName: tlsServerName,
		FromAddress:   fromAddress,
		Username:      username,
		Password:      password,
	}
	if err := validateConfig(config); err != nil {
		return Config{}, ErrInvalidConfiguration
	}
	return config, nil
}

func (config Config) withDefaults() Config {
	if config.Host == "" {
		config.Host = Host
	}
	if config.Port == 0 {
		config.Port = Port
	}
	if config.TLSServerName == "" {
		config.TLSServerName = config.Host
	}
	return config
}

func validateConfig(config Config) error {
	if !validCredential(config.Username) || !validCredential(config.Password) {
		return ErrInvalidConfiguration
	}
	if !validHost(config.Host) || config.Port < 1 || config.Port > 65535 || !validHost(config.TLSServerName) {
		return ErrInvalidConfiguration
	}
	return nil
}

// LoadDotEnv fills missing environment variables from a small, intentionally
// limited KEY=VALUE file. Existing process environment values always win.
func LoadDotEnv(path string) error {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, found := strings.Cut(line, "=")
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if !found || !validEnvKey(key) {
			return errors.New("invalid environment file line")
		}
		if len(value) >= 2 && ((value[0] == '\'' && value[len(value)-1] == '\'') || (value[0] == '"' && value[len(value)-1] == '"')) {
			value = value[1 : len(value)-1]
		}
		if _, exists := os.LookupEnv(key); !exists {
			if err := os.Setenv(key, value); err != nil {
				return err
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	return nil
}

func validEnvKey(key string) bool {
	if key == "" || (key[0] != '_' && (key[0] < 'A' || key[0] > 'Z') && (key[0] < 'a' || key[0] > 'z')) {
		return false
	}
	for _, character := range key[1:] {
		if character != '_' && (character < 'A' || character > 'Z') && (character < 'a' || character > 'z') && (character < '0' || character > '9') {
			return false
		}
	}
	return true
}

func validCredential(value string) bool {
	return strings.TrimSpace(value) != "" && !strings.ContainsAny(value, "\r\n\x00")
}

func validHost(host string) bool {
	if strings.TrimSpace(host) == "" || strings.TrimSpace(host) != host || strings.ContainsAny(host, "\r\n\x00") {
		return false
	}
	if net.ParseIP(host) != nil {
		return true
	}
	if len(host) > 253 || strings.HasPrefix(host, ".") || strings.HasSuffix(host, ".") {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if label == "" || len(label) > 63 || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
			return false
		}
		for _, character := range label {
			if (character < 'a' || character > 'z') && (character < 'A' || character > 'Z') && (character < '0' || character > '9') && character != '-' {
				return false
			}
		}
	}
	return true
}
