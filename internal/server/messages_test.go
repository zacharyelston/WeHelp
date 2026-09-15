package server

import (
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestCursorRoundTrip(t *testing.T) {
	ts := time.Date(2026, 9, 15, 12, 30, 45, 123456789, time.UTC)
	id := uuid.New()

	c := encodeCursor(ts, id)
	gotTS, gotID, err := decodeCursor(c)
	if err != nil {
		t.Fatalf("decodeCursor: %v", err)
	}
	if !gotTS.Equal(ts) {
		t.Fatalf("timestamp mismatch: got %v want %v", gotTS, ts)
	}
	if gotID != id {
		t.Fatalf("id mismatch: got %v want %v", gotID, id)
	}
}

func TestDecodeCursorInvalid(t *testing.T) {
	tests := []struct {
		name   string
		cursor string
	}{
		{"not base64", "!!!not-base64!!!"},
		{"no separator", base64.RawURLEncoding.EncodeToString([]byte("no-pipe-here"))},
		{"bad timestamp", base64.RawURLEncoding.EncodeToString([]byte("not-a-time|" + uuid.New().String()))},
		{"bad uuid", base64.RawURLEncoding.EncodeToString([]byte(time.Now().UTC().Format(time.RFC3339Nano) + "|not-a-uuid"))},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, _, err := decodeCursor(tt.cursor); err == nil {
				t.Fatal("expected error, got nil")
			}
		})
	}
}

func TestNormalizeMetadata(t *testing.T) {
	tests := []struct {
		name    string
		input   json.RawMessage
		want    json.RawMessage
		wantErr bool
	}{
		{"empty defaults to object", nil, json.RawMessage(`{}`), false},
		{"json null defaults to object", json.RawMessage(`null`), json.RawMessage(`{}`), false},
		{"valid object passes through", json.RawMessage(`{"weight":72.5}`), json.RawMessage(`{"weight":72.5}`), false},
		{"array rejected", json.RawMessage(`[1,2,3]`), nil, true},
		{"string rejected", json.RawMessage(`"hello"`), nil, true},
		{"number rejected", json.RawMessage(`42`), nil, true},
		{"invalid json rejected", json.RawMessage(`{bad`), nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := normalizeMetadata(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if string(got) != string(tt.want) {
				t.Fatalf("got %s want %s", got, tt.want)
			}
		})
	}
}

func TestMessageKinds(t *testing.T) {
	for _, k := range []string{"message", "memo", "checkin", "reminder"} {
		if !messageKinds[k] {
			t.Errorf("expected kind %q to be valid", k)
		}
	}
	for _, k := range []string{"", "chat", "note", "alert"} {
		if messageKinds[k] {
			t.Errorf("expected kind %q to be invalid", k)
		}
	}
}
