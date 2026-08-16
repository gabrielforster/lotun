// Package config loads and saves server and client configuration for lotun,
// layering environment variables and YAML files over built-in defaults via
// viper.
package config

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/spf13/viper"
	"go.yaml.in/yaml/v3"
)

// ServerConfig holds configuration for the lotund server.
type ServerConfig struct {
	ControlAddr    string `mapstructure:"control_addr"`     // default ":7000"
	ControlTLSCert string `mapstructure:"control_tls_cert"` // "" => plaintext control (dev/test)
	ControlTLSKey  string `mapstructure:"control_tls_key"`
	HTTPAddr       string `mapstructure:"http_addr"`    // default ":8000" (Caddy fronts it)
	BaseDomain     string `mapstructure:"base_domain"`  // e.g. "lvh.me"
	TCPPortMin     int    `mapstructure:"tcp_port_min"` // default 20000
	TCPPortMax     int    `mapstructure:"tcp_port_max"` // default 30000
	Token          string `mapstructure:"token"`
	DataDir        string `mapstructure:"data_dir"` // default "./data"
}

// TunnelConfig declares one tunnel to expose. A list of these under `tunnels:`
// is what `lotun serve` registers on a single control session.
type TunnelConfig struct {
	Type       string   `mapstructure:"type" yaml:"type"`                         // "http" or "tcp"
	Domain     string   `mapstructure:"domain" yaml:"domain,omitempty"`           // claimed subdomain; empty => server assigns
	Port       int      `mapstructure:"port" yaml:"port"`                         // local port to forward to
	RemotePort int      `mapstructure:"remote_port" yaml:"remote_port,omitempty"` // tcp only; 0 => server assigns
	Private    bool     `mapstructure:"private" yaml:"private,omitempty"`
	Password   string   `mapstructure:"password" yaml:"password,omitempty"`   // http only
	AllowIPs   []string `mapstructure:"allow_ips" yaml:"allow_ips,omitempty"` // tcp only
}

// ClientConfig holds configuration for the lotun client.
type ClientConfig struct {
	ControlAddr   string `mapstructure:"control_addr" yaml:"control_addr"`
	Token         string `mapstructure:"token" yaml:"token"`
	DefaultDomain string `mapstructure:"default_domain" yaml:"default_domain,omitempty"`
	// TLS dials the control port over TLS. TLSInsecure additionally skips
	// certificate verification, for a self-signed control cert.
	TLS         bool `mapstructure:"tls" yaml:"tls,omitempty"`
	TLSInsecure bool `mapstructure:"tls_insecure" yaml:"tls_insecure,omitempty"`
	// Tunnels is the declarative tunnel set served by `lotun serve`.
	Tunnels []TunnelConfig `mapstructure:"tunnels" yaml:"tunnels,omitempty"`
}

// newViper builds a fresh viper instance with the given env prefix and, if a
// non-empty path is supplied, configures it to read that YAML file. Using a new
// instance (never the global) keeps configuration state isolated per call.
func newViper(prefix, path string) *viper.Viper {
	v := viper.New()
	v.SetEnvPrefix(prefix)
	v.AutomaticEnv()
	if path != "" {
		v.SetConfigFile(path)
	}
	return v
}

// LoadServer reads path (yaml) with env overrides (prefix LOTUND_, e.g.
// LOTUND_TOKEN) layered over defaults. path may be "" => defaults+env only.
func LoadServer(path string) (ServerConfig, error) {
	v := newViper("LOTUND", path)
	v.SetDefault("control_addr", ":7000")
	v.SetDefault("http_addr", ":8000")
	v.SetDefault("tcp_port_min", 20000)
	v.SetDefault("tcp_port_max", 30000)
	v.SetDefault("data_dir", "./data")

	if path != "" {
		if err := v.ReadInConfig(); err != nil {
			return ServerConfig{}, err
		}
	}

	var c ServerConfig
	if err := v.Unmarshal(&c); err != nil {
		return ServerConfig{}, err
	}
	return c, nil
}

// LoadClient reads path with env prefix LOTUN_. Missing file => zero-value
// config + env, no error (so `lotun login` can create it).
func LoadClient(path string) (ClientConfig, error) {
	v := newViper("LOTUN", path)

	if path != "" {
		if err := v.ReadInConfig(); err != nil {
			var notFound *os.PathError
			var viperNotFound viper.ConfigFileNotFoundError
			if !errors.As(err, &notFound) && !errors.As(err, &viperNotFound) {
				return ClientConfig{}, err
			}
		}
	}

	var c ClientConfig
	if err := v.Unmarshal(&c); err != nil {
		return ClientConfig{}, err
	}
	return c, nil
}

// SaveClient writes c to path as yaml (used by `lotun login`), creating parent
// dirs. The file carries the auth token and any tunnel passwords, so it is
// written 0600 inside a 0700 directory.
func SaveClient(path string, c ClientConfig) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := yaml.Marshal(c)
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o600)
}
