package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/pion/turn/v5"
)

// Spike-only signaling: HTTP long-poll mailboxes per peer, per room. In V1
// signaling goes through the Back Office over each party's pinned channel.
//
//	POST /join?room=R           -> {peer, initiator, iceServers}
//	POST /send?room=R&peer=P    body {type, sdp?, candidate?...}: delivered to the other peer
//	GET  /poll?room=R&peer=P    -> [messages] (waits up to 20s)
//	POST /report?room=R&peer=P  body: client stats JSON, logged and checked
type signaling struct {
	secret  string
	ip      string
	port    int
	mu      sync.Mutex
	rooms   map[string]*room
	fpRegex *regexp.Regexp
}

type room struct {
	peers map[string]*peer
	order []string
}

type peer struct {
	id      string
	inbox   []json.RawMessage
	notify  chan struct{}
	localFP string // DTLS fingerprint from this peer's own SDP
}

func newSignaling(secret, ip string, port int) *signaling {
	return &signaling{
		secret: secret, ip: ip, port: port,
		rooms:   map[string]*room{},
		fpRegex: regexp.MustCompile(`a=fingerprint:(\S+) (\S+)`),
	}
}

func (s *signaling) routes(mux *http.ServeMux) {
	mux.HandleFunc("POST /join", s.join)
	mux.HandleFunc("POST /send", s.send)
	mux.HandleFunc("GET /poll", s.poll)
	mux.HandleFunc("POST /report", s.report)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "ok\n") })
}

func newID() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func (s *signaling) join(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("room")
	if name == "" {
		http.Error(w, "room required", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	rm := s.rooms[name]
	if rm == nil || len(rm.peers) >= 2 {
		// A full room is reset: the spike reruns calls with the same name.
		rm = &room{peers: map[string]*peer{}}
		s.rooms[name] = rm
	}
	p := &peer{id: newID(), notify: make(chan struct{}, 1)}
	initiator := len(rm.peers) == 1
	rm.peers[p.id] = p
	rm.order = append(rm.order, p.id)
	if initiator {
		// Tell the first peer someone arrived; the second peer sends the offer.
		for id, other := range rm.peers {
			if id != p.id {
				other.push(json.RawMessage(`{"type":"peer-joined"}`))
			}
		}
	}
	s.mu.Unlock()

	user, cred, err := turn.GenerateLongTermTURNRESTCredentials(s.secret, name+"-"+p.id, time.Hour)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	slog.Info("signal join", "room", name, "peer", p.id, "initiator", initiator, "turn_user", user)
	writeJSON(w, map[string]any{
		"peer":      p.id,
		"initiator": initiator,
		"iceServers": []map[string]any{{
			"urls": []string{
				fmt.Sprintf("turn:%s:%d?transport=udp", s.ip, s.port),
				fmt.Sprintf("turn:%s:%d?transport=tcp", s.ip, s.port),
			},
			"username":   user,
			"credential": cred,
		}},
	})
}

func (p *peer) push(m json.RawMessage) {
	p.inbox = append(p.inbox, m)
	select {
	case p.notify <- struct{}{}:
	default:
	}
}

func (s *signaling) lookup(r *http.Request) (*room, *peer, bool) {
	q := r.URL.Query()
	rm := s.rooms[q.Get("room")]
	if rm == nil {
		return nil, nil, false
	}
	p := rm.peers[q.Get("peer")]
	return rm, p, p != nil
}

func (s *signaling) send(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var msg struct {
		Type string `json:"type"`
		SDP  string `json:"sdp"`
	}
	if err := json.Unmarshal(body, &msg); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rm, p, ok := s.lookup(r)
	if !ok {
		http.Error(w, "unknown room or peer", http.StatusNotFound)
		return
	}
	if msg.SDP != "" {
		fp := ""
		if m := s.fpRegex.FindStringSubmatch(msg.SDP); m != nil {
			fp = m[1] + " " + m[2]
			p.localFP = m[2]
		}
		slog.Info("signal sdp", "room", r.URL.Query().Get("room"), "peer", p.id, "type", msg.Type,
			"fingerprint", fp,
			"savpf", strings.Contains(msg.SDP, "UDP/TLS/RTP/SAVPF"),
			"plain_rtp_avp", strings.Contains(msg.SDP, " RTP/AVP "))
	}
	for id, other := range rm.peers {
		if id != p.id {
			other.push(json.RawMessage(body))
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *signaling) poll(w http.ResponseWriter, r *http.Request) {
	deadline := time.After(20 * time.Second)
	for {
		s.mu.Lock()
		_, p, ok := s.lookup(r)
		if !ok {
			s.mu.Unlock()
			http.Error(w, "unknown room or peer", http.StatusNotFound)
			return
		}
		if len(p.inbox) > 0 {
			msgs := p.inbox
			p.inbox = nil
			s.mu.Unlock()
			writeJSON(w, msgs)
			return
		}
		ch := p.notify
		s.mu.Unlock()
		select {
		case <-ch:
		case <-deadline:
			writeJSON(w, []json.RawMessage{})
			return
		case <-r.Context().Done():
			return
		}
	}
}

// report logs the client's WebRTC stats summary and checks that the remote
// DTLS certificate fingerprint the client actually negotiated equals the
// fingerprint the other peer put in its own SDP (end-to-end DTLS, no MITM).
func (s *signaling) report(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var rep map[string]any
	if err := json.Unmarshal(body, &rep); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	rm, p, ok := s.lookup(r)
	var otherFP string
	if ok {
		for id, other := range rm.peers {
			if id != p.id {
				otherFP = other.localFP
			}
		}
	}
	s.mu.Unlock()
	if !ok {
		http.Error(w, "unknown room or peer", http.StatusNotFound)
		return
	}
	remoteFP, _ := rep["remoteCertFingerprint"].(string)
	args := []any{"room", r.URL.Query().Get("room"), "peer", p.id}
	for _, k := range []string{"event", "connectionState", "localCandidateType", "remoteCandidateType",
		"localRelayProtocol", "dtlsState", "dtlsCipher", "srtpCipher", "tlsVersion",
		"bytesSent", "bytesReceived", "framesSent", "framesDecoded", "fps", "width", "height",
		"availableOutgoingBitrate", "rttMs", "packetsLost", "jitter", "qualityLimitationReason",
		"sendKbps", "recvKbps", "sendWidth", "sendHeight", "sendFps", "codec", "encoder", "decoder"} {
		if v, ok := rep[k]; ok {
			args = append(args, k, v)
		}
	}
	if remoteFP != "" && otherFP != "" {
		args = append(args, "remote_fp_matches_peer_sdp", strings.EqualFold(remoteFP, otherFP))
	}
	slog.Info("client report", args...)
	w.WriteHeader(http.StatusNoContent)
}
