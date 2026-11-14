package config

import (
	"errors"
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
