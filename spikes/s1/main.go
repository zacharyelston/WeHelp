// Command s1 is Spike S1 (#32): prove a phone can reach its Back Office
// through a relay using HTTP CONNECT + an SSH channel, with TLS end to end to
// a pinned office key. Throwaway code; see docs/spikes/S1.md.
//
//	s1 keys   -dir keys -relay-ip 192.168.1.20
//	s1 relay  -dir keys -ssh :2222 -connect :8443 [-dump dumps]
//	s1 office -dir keys -relay 192.168.1.20:2222 -upstream http://127.0.0.1:8080
package main

import (
	"flag"
	"fmt"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: s1 keys|relay|office [flags]")
		os.Exit(2)
	}
	fs := flag.NewFlagSet(os.Args[1], flag.ExitOnError)
	dir := fs.String("dir", "keys", "key directory")
	var err error
	switch os.Args[1] {
	case "keys":
		ip := fs.String("relay-ip", "127.0.0.1", "relay IP for its TLS certificate")
		host := fs.String("office-host", "office-1.wehelp.invalid", "office TLS hostname")
		_ = fs.Parse(os.Args[2:])
		err = genKeys(*dir, *ip, *host)
	case "relay":
		sshAddr := fs.String("ssh", ":2222", "SSH listen address (offices)")
		connAddr := fs.String("connect", ":8443", "CONNECT listen address (phones)")
		dump := fs.String("dump", "", "directory to record spliced bytes (plaintext check)")
		_ = fs.Parse(os.Args[2:])
		err = runRelay(*dir, *sshAddr, *connAddr, *dump)
	case "office":
		relayAddr := fs.String("relay", "127.0.0.1:2222", "relay SSH address")
		upstream := fs.String("upstream", "http://127.0.0.1:8080", "local wehelpd")
		_ = fs.Parse(os.Args[2:])
		err = runOffice(*dir, *relayAddr, *upstream)
	default:
		err = fmt.Errorf("unknown command %q", os.Args[1])
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
