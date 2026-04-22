package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Listen   string         `yaml:"listen"`
	TLS      TLSConfig      `yaml:"tls"`
	Telegram TelegramConfig `yaml:"telegram"`
	Clients  []ClientConfig `yaml:"clients"`
	Media    MediaConfig    `yaml:"media"`
	LogLevel string         `yaml:"log_level"`
}

type TLSConfig struct {
	Auto     bool     `yaml:"auto"`
	CertPath string   `yaml:"cert_path"`
	KeyPath  string   `yaml:"key_path"`
	Hosts    []string `yaml:"hosts"`
}

type TelegramConfig struct {
	APIID      int    `yaml:"api_id"`
	APIHash    string `yaml:"api_hash"`
	SessionDir string `yaml:"session_dir"`
}

type ClientConfig struct {
	Name         string  `yaml:"name"`
	Token        string  `yaml:"token"`
	AllowedChats []int64 `yaml:"allowed_chats"`
}

type MediaConfig struct {
	CacheDir string `yaml:"cache_dir"`
	MaxBytes int64  `yaml:"max_bytes"`
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	var c Config
	if err := yaml.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	if err := c.validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

func (c *Config) validate() error {
	if c.Listen == "" {
		return fmt.Errorf("listen must be set")
	}
	if c.Telegram.APIID == 0 || c.Telegram.APIHash == "" {
		return fmt.Errorf("telegram.api_id and telegram.api_hash must be set (see https://my.telegram.org/apps)")
	}
	if c.Telegram.SessionDir == "" {
		return fmt.Errorf("telegram.session_dir must be set")
	}
	if len(c.Clients) == 0 {
		return fmt.Errorf("at least one client with a bearer token must be configured")
	}
	for i, cl := range c.Clients {
		if cl.Token == "" {
			return fmt.Errorf("clients[%d].token must be set", i)
		}
	}
	if c.Media.CacheDir == "" {
		c.Media.CacheDir = "./data/media"
	}
	if c.Media.MaxBytes == 0 {
		c.Media.MaxBytes = 100 << 20
	}
	if c.LogLevel == "" {
		c.LogLevel = "info"
	}
	return nil
}

// TokenToClient returns the client config matching a bearer token, or nil.
func (c *Config) TokenToClient(token string) *ClientConfig {
	for i := range c.Clients {
		if c.Clients[i].Token == token {
			return &c.Clients[i]
		}
	}
	return nil
}
