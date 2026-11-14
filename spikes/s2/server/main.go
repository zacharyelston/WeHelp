// Spike S2 (throwaway): a pion/turn server plus a tiny HTTP long-poll
// signaling endpoint, so two iOS clients can hold a 1:1 WebRTC call with
// relay-only ICE. See docs/spikes/S2.md. Not production code.
package main

import (
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"strconv"
	"syscall"
	"time"

	"github.com/pion/logging"
	"github.com/pion/turn/v5"
)

func main() {
	ip := flag.String("ip", "192.168.1.20", "LAN address to listen on and advertise as the relay address")
	turnPort := flag.Int("turn-port", 3478, "TURN listen port (UDP and TCP)")
	httpAddr := flag.String("http", ":8080", "signaling HTTP listen address")
	relayMin := flag.Int("relay-min", 49160, "lowest relay port")
	relayMax := flag.Int("relay-max", 49999, "highest relay port")
	secret := flag.String("secret", "s2-spike-shared-secret", "TURN REST shared secret (spike only)")
	realm := flag.String("realm", "wehelp.spike", "TURN realm")
	statsEvery := flag.Duration("stats", 10*time.Second, "how often to log process and relay stats")
	flag.Parse()

	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})))

	relayIP := net.ParseIP(*ip)
	if relayIP == nil {
		fail("bad -ip %q", *ip)
	}
	turnAddr := net.JoinHostPort(*ip, strconv.Itoa(*turnPort))

	udpConn, err := net.ListenPacket("udp4", turnAddr)
	if err != nil {
		fail("listen udp: %v", err)
	}
	tcpLn, err := net.Listen("tcp4", turnAddr)
	if err != nil {
		fail("listen tcp: %v", err)
	}

	relays := newRelayRegistry()
	gen := func() turn.RelayAddressGenerator {
		return &countingGenerator{
			RelayAddressGeneratorPortRange: &turn.RelayAddressGeneratorPortRange{
				RelayAddress: relayIP,
				Address:      *ip,
				MinPort:      uint16(*relayMin),
				MaxPort:      uint16(*relayMax),
			},
			reg: relays,
		}
	}

	lf := logging.NewDefaultLoggerFactory()
	lf.DefaultLogLevel = logging.LogLevelWarn

	srv, err := turn.NewServer(turn.ServerConfig{
		Realm:         *realm,
		LoggerFactory: lf,
		AuthHandler:   turn.LongTermTURNRESTAuthHandler(*secret, lf.NewLogger("auth")),
		PacketConnConfigs: []turn.PacketConnConfig{
			{PacketConn: udpConn, RelayAddressGenerator: gen()},
		},
		ListenerConfigs: []turn.ListenerConfig{
			{Listener: tcpLn, RelayAddressGenerator: gen()},
		},
		EventHandler: turn.EventHandler{
			OnAuth: func(src, _ net.Addr, proto, user, _ string, method string, ok bool) {
				if !ok {
					slog.Warn("turn auth failed", "src", src, "proto", proto, "user", user, "method", method)
				}
			},
			OnAllocationCreated: func(src, _ net.Addr, proto, user, _ string, relay net.Addr, _ int) {
				slog.Info("turn allocation created", "src", src, "proto", proto, "user", user, "relay", relay)
			},
			OnAllocationDeleted: func(src, _ net.Addr, proto, user, _ string) {
				slog.Info("turn allocation deleted", "src", src, "proto", proto, "user", user)
			},
			OnPermissionCreated: func(src, _ net.Addr, _, user, _ string, relay net.Addr, peer net.IP) {
				slog.Info("turn permission", "user", user, "relay", relay, "peer", peer)
			},
			OnChannelCreated: func(_, _ net.Addr, _, user, _ string, relay, peer net.Addr, ch uint16) {
				slog.Info("turn channel bound", "user", user, "relay", relay, "peer", peer, "channel", ch)
			},
		},
	})
	if err != nil {
		fail("turn server: %v", err)
	}

	sig := newSignaling(*secret, *ip, *turnPort)
	mux := http.NewServeMux()
	sig.routes(mux)
	httpSrv := &http.Server{Addr: *httpAddr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			fail("http: %v", err)
		}
	}()

	slog.Info("spike s2 up", "turn_udp", turnAddr, "turn_tcp", turnAddr, "signaling", *httpAddr,
		"relay_ports", fmt.Sprintf("%d-%d", *relayMin, *relayMax), "pid", os.Getpid())

	go statsLoop(*statsEvery, srv, relays)

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	relays.logAll("final")
	_ = srv.Close()
	_ = httpSrv.Close()
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}

// statsLoop logs process CPU% (user+sys over the interval), RSS and per-relay
// byte counters.
func statsLoop(every time.Duration, srv *turn.Server, relays *relayRegistry) {
	var last syscall.Rusage
	_ = syscall.Getrusage(syscall.RUSAGE_SELF, &last)
	lastT := time.Now()
	for range time.Tick(every) {
		var ru syscall.Rusage
		_ = syscall.Getrusage(syscall.RUSAGE_SELF, &ru)
		now := time.Now()
		cpu := tvDur(ru.Utime) + tvDur(ru.Stime) - tvDur(last.Utime) - tvDur(last.Stime)
		pct := 100 * cpu.Seconds() / now.Sub(lastT).Seconds()
		last, lastT = ru, now
		var ms runtime.MemStats
		runtime.ReadMemStats(&ms)
		tot := relays.totals()
		slog.Info("proc",
			"cpu_pct", fmt.Sprintf("%.2f", pct),
			"maxrss_mb", fmt.Sprintf("%.1f", float64(ru.Maxrss)/1024/1024), // macOS reports bytes
			"heap_mb", fmt.Sprintf("%.1f", float64(ms.HeapAlloc)/1024/1024),
			"goroutines", runtime.NumGoroutine(),
			"allocations", srv.AllocationCount(),
			"relays_live", tot.live,
			"relay_kbps_total", fmt.Sprintf("%.0f", tot.kbps),
		)
		relays.logAll("interval")
	}
}

func tvDur(tv syscall.Timeval) time.Duration {
	return time.Duration(tv.Sec)*time.Second + time.Duration(tv.Usec)*time.Microsecond
}
