package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/viper"
)

type Config struct {
	ListenAddr  string        `mapstructure:"listen_addr"`
	DatabaseURL string        `mapstructure:"database_url"`
	LogLevel    string        `mapstructure:"log_level"`
	Env         string        `mapstructure:"env"`
	JWTSecret   string        `mapstructure:"jwt_secret"`
	AccessTTL   time.Duration `mapstructure:"access_ttl"`
	RefreshTTL  time.Duration `mapstructure:"refresh_ttl"`

	// WeHelp Registry (wehelp-registry, #60). WEHELP_REGISTRY_* env vars.
	RegistryListenAddr  string `mapstructure:"registry_listen_addr"`
	RegistryDatabaseURL string `mapstructure:"registry_database_url"`
	RegistrySigningKey  string `mapstructure:"registry_signing_key"` // ed25519 PEM, mode 600
	RegistryTLSCert     string `mapstructure:"registry_tls_cert"`    // empty = plain HTTP (dev only)
	RegistryTLSKey      string `mapstructure:"registry_tls_key"`

	// Back Office identity (#35): keys, local CA and Registry registration.
	OfficeDir   string `mapstructure:"office_dir"`   // mode 700, outside the repo
	RegistryURL string `mapstructure:"registry_url"` // where wehelpd init installs; the Relay pulls lists here too

	// Office → Relay tunnel (#4), started by wehelpd serve once installed.
	RelayTunnel          bool   `mapstructure:"relay_tunnel"`
	RelayAddressOverride string `mapstructure:"relay_address_override"` // demos only

	// WeHelp Relay (wehelp-relay, #4). WEHELP_RELAY_* env vars.
	RelayDir          string        `mapstructure:"relay_dir"` // host key, TLS key, list cache; mode 700
	RelayID           string        `mapstructure:"relay_id"`  // from wehelp-registry relay add
	RelaySSHAddr      string        `mapstructure:"relay_ssh_addr"`
	RelayConnectAddr  string        `mapstructure:"relay_connect_addr"`
	RelayRegistryKey  string        `mapstructure:"relay_registry_key"` // pinned: wehelp-registry pubkey
	RelayListInterval time.Duration `mapstructure:"relay_list_interval"`
}

func defaultRelayDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "/var/lib/wehelp-relay"
	}
	return filepath.Join(home, ".wehelp", "relay")
}

// defaultRegistryKey keeps the signing key outside the repo by default.
func defaultRegistryKey() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "/var/lib/wehelp-registry/signing.key"
	}
	return filepath.Join(home, ".wehelp", "registry", "signing.key")
}

// defaultOfficeDir keeps the office keys outside the repo by default.
func defaultOfficeDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "/var/lib/wehelp/office"
	}
	return filepath.Join(home, ".wehelp", "office")
}

// Load initializes Viper: defaults, WEHELP_* env vars, and an optional YAML
// config file (./wehelp.yaml, /etc/wehelp/wehelp.yaml, or --config path).
func Load(cfgFile string) error {
	viper.SetEnvPrefix("WEHELP")
	viper.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	viper.AutomaticEnv()

	viper.SetDefault("listen_addr", ":8080")
	viper.SetDefault("database_url", "postgres://wehelp:wehelp@localhost:5432/wehelp?sslmode=disable")
	viper.SetDefault("log_level", "info")
	viper.SetDefault("env", "dev")
	viper.SetDefault("jwt_secret", "") // empty = ephemeral per-boot secret (dev only)
	viper.SetDefault("access_ttl", "15m")
	viper.SetDefault("refresh_ttl", "720h")

	viper.SetDefault("registry_listen_addr", ":8090")
	viper.SetDefault("registry_database_url", "postgres://wehelp:wehelp@localhost:5433/wehelp_registry?sslmode=disable")
	viper.SetDefault("registry_signing_key", defaultRegistryKey())
	viper.SetDefault("registry_tls_cert", "")
	viper.SetDefault("registry_tls_key", "")

	viper.SetDefault("office_dir", defaultOfficeDir())
	viper.SetDefault("registry_url", "http://localhost:8090")
	viper.SetDefault("relay_tunnel", true)
	viper.SetDefault("relay_address_override", "")

	viper.SetDefault("relay_dir", defaultRelayDir())
	viper.SetDefault("relay_id", "")
	viper.SetDefault("relay_ssh_addr", ":2222")
	viper.SetDefault("relay_connect_addr", ":8443")
	viper.SetDefault("relay_registry_key", "")
	viper.SetDefault("relay_list_interval", "1m")

	if cfgFile != "" {
		viper.SetConfigFile(cfgFile)
	} else {
		viper.SetConfigName("wehelp")
		viper.SetConfigType("yaml")
		viper.AddConfigPath(".")
		viper.AddConfigPath("/etc/wehelp")
	}

	if err := viper.ReadInConfig(); err != nil {
		var notFound viper.ConfigFileNotFoundError
		// A missing config file is fine unless one was explicitly requested.
		if cfgFile != "" || !errors.As(err, &notFound) {
			return err
		}
	}
	return nil
}

// C unmarshals the loaded configuration.
func C() Config {
	var c Config
	_ = viper.Unmarshal(&c)
	return c
}
