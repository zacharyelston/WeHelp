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

	// Staff web UI over HTTPS on the office LAN (#38).
	StaffTLSAddr string `mapstructure:"staff_tls_addr"` // empty = no HTTPS listener
	StaffHosts   string `mapstructure:"staff_hosts"`    // comma-separated names and IPs for the certificate

	// Reminders (#19): the Back Office's reminder worker.
	ReminderInterval time.Duration `mapstructure:"reminder_interval"` // 0 = reminders off
	ReminderOffsets  string        `mapstructure:"reminder_offsets"`  // before each appointment, e.g. "24h,1h"
	Timezone         string        `mapstructure:"timezone"`          // IANA name for reminder text; empty = host time

	// Form occurrences (#196): how often the runner creates occurrences, marks
	// missed ones and sends form_due. 0 = off.
	FormsInterval time.Duration `mapstructure:"forms_interval"`

	// Provider roster (#62): how often the Back Office pulls it from the Registry.
	RosterInterval time.Duration `mapstructure:"roster_interval"` // 0 = only when an admin presses Sync now

	// WeHelp Relay (wehelp-relay, #4). WEHELP_RELAY_* env vars.
	RelayDir          string        `mapstructure:"relay_dir"` // host key, TLS key, list cache; mode 700
	RelayID           string        `mapstructure:"relay_id"`  // from wehelp-registry relay add
	RelaySSHAddr      string        `mapstructure:"relay_ssh_addr"`
	RelayConnectAddr  string        `mapstructure:"relay_connect_addr"`
	RelayRegistryKey  string        `mapstructure:"relay_registry_key"` // pinned: wehelp-registry pubkey
	RelayListInterval time.Duration `mapstructure:"relay_list_interval"`
	// Push (#7): the app publisher's APNs auth key, held only by the Relay.
	RelayAPNsKey   string `mapstructure:"relay_apns_key"` // .p8 path, mode 600; empty = push off
	RelayAPNsKeyID string `mapstructure:"relay_apns_key_id"`
	RelayAPNsTeam  string `mapstructure:"relay_apns_team"`
	RelayAPNsTopic string `mapstructure:"relay_apns_topic"` // the app's bundle ID
	// Practice websites (#97): served read-only at /s/<office-id>/.
	RelaySiteAddr      string `mapstructure:"relay_site_addr"`       // HTTPS listener; empty = no websites
	RelaySitePublicURL string `mapstructure:"relay_site_public_url"` // e.g. https://relay.example:8444, told to offices
	RelaySiteCert      string `mapstructure:"relay_site_cert"`       // PEM cert for the site listener; empty = the Relay TLS cert
	RelaySiteKey       string `mapstructure:"relay_site_key"`        // its key, mode 600
	// Relay logs (#42): daily files kept relay_log_days; "-" = stderr (journald keeps them).
	RelayLogDir  string `mapstructure:"relay_log_dir"` // default relay_dir/logs
	RelayLogDays int    `mapstructure:"relay_log_days"`
	// Video-visit TURN (#6). Off until relay_turn_public_ip is set.
	RelayTURNAddr           string `mapstructure:"relay_turn_addr"`             // UDP and TCP
	RelayTURNPublicIP       string `mapstructure:"relay_turn_public_ip"`        // the address phones reach; empty = TURN off
	RelayTURNPorts          string `mapstructure:"relay_turn_ports"`            // relay port range
	RelayTURNMaxVisits      int    `mapstructure:"relay_turn_max_visits"`       // live visits per office when the office list gives no cap (#175)
	RelayTURNMaxTotalVisits int    `mapstructure:"relay_turn_max_total_visits"` // live visits across all offices; 0 = no cap
	RelayTURNSecret         string `mapstructure:"relay_turn_secret_file"`      // master secret, mode 600; default relay_dir/turn.secret
	RelayTURNTLSAddr        string `mapstructure:"relay_turn_tls_addr"`         // TURN over TLS, e.g. :443; needs a public certificate
	RelayTURNHostname       string `mapstructure:"relay_turn_hostname"`         // public DNS name: Let's Encrypt via autocert
	RelayACMEDir            string `mapstructure:"relay_acme_dir"`              // autocert cache; default relay_dir/acme
	RelayTURNTLSCert        string `mapstructure:"relay_turn_tls_cert"`         // or a certificate from certbot
	RelayTURNTLSKey         string `mapstructure:"relay_turn_tls_key"`
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
	viper.SetDefault("env", "production") // dev, demo and appliance are set explicitly (#161)
	viper.SetDefault("jwt_secret", "")    // empty = ephemeral per-boot secret (dev only)
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
	viper.SetDefault("staff_tls_addr", ":8443")
	viper.SetDefault("staff_hosts", "")
	viper.SetDefault("reminder_interval", "1m")
	viper.SetDefault("reminder_offsets", "24h,1h")
	viper.SetDefault("roster_interval", "15m")
	viper.SetDefault("timezone", "")
	viper.SetDefault("forms_interval", "5m")

	viper.SetDefault("relay_dir", defaultRelayDir())
	viper.SetDefault("relay_id", "")
	viper.SetDefault("relay_ssh_addr", ":2222")
	viper.SetDefault("relay_connect_addr", ":8443")
	viper.SetDefault("relay_registry_key", "")
	viper.SetDefault("relay_list_interval", "1m")
	viper.SetDefault("relay_apns_key", "")
	viper.SetDefault("relay_apns_key_id", "")
	viper.SetDefault("relay_apns_team", "")
	viper.SetDefault("relay_apns_topic", "com.zacelston.wehelp")
	viper.SetDefault("relay_site_addr", ":8444")
	viper.SetDefault("relay_site_public_url", "")
	viper.SetDefault("relay_site_cert", "")
	viper.SetDefault("relay_site_key", "")
	viper.SetDefault("relay_log_dir", "")
	viper.SetDefault("relay_log_days", 7)
	viper.SetDefault("relay_turn_addr", ":3478")
	viper.SetDefault("relay_turn_public_ip", "")
	viper.SetDefault("relay_turn_ports", "49160-49999")
	viper.SetDefault("relay_turn_max_visits", 4)
	viper.SetDefault("relay_turn_max_total_visits", 0)
	viper.SetDefault("relay_turn_secret_file", "")
	viper.SetDefault("relay_turn_tls_addr", "")
	viper.SetDefault("relay_turn_hostname", "")
	viper.SetDefault("relay_acme_dir", "")
	viper.SetDefault("relay_turn_tls_cert", "")
	viper.SetDefault("relay_turn_tls_key", "")

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
