// Package pgurl converts Postgres DSNs. It has no data dependencies, so the
// Back Office, the Registry and the Relay can all use it.
package pgurl

import (
	"fmt"
	"net/url"
)

// JDBC converts a postgres:// DSN into the JDBC URL Flyway expects,
// carrying user/password as query params. DSNs must be in URL form.
func JDBC(databaseURL string) (string, error) {
	u, err := url.Parse(databaseURL)
	if err != nil || u.Scheme != "postgres" && u.Scheme != "postgresql" {
		return "", fmt.Errorf("database_url must be a postgres:// URL: %w", err)
	}
	q := u.Query()
	if u.User != nil {
		q.Set("user", u.User.Username())
		if pw, ok := u.User.Password(); ok {
			q.Set("password", pw)
		}
	}
	return "jdbc:postgresql://" + u.Host + u.Path + "?" + q.Encode(), nil
}
