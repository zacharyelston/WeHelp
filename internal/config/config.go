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
}

// defaultRegistryKey keeps the signing key outside the repo by default.
func defaultRegistryKey() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "/var/lib/wehelp-registry/signing.key"
	}
	return filepath.Join(home, ".wehelp", "registry", "signing.key")
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
