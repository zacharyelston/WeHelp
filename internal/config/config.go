package config

import (
	"errors"
	"strings"

	"github.com/spf13/viper"
)

type Config struct {
	ListenAddr  string `mapstructure:"listen_addr"`
	DatabaseURL string `mapstructure:"database_url"`
	LogLevel    string `mapstructure:"log_level"`
	Env         string `mapstructure:"env"`
	AutoMigrate bool   `mapstructure:"auto_migrate"`
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
	viper.SetDefault("auto_migrate", true)

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
