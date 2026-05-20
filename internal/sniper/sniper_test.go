package sniper

import (
	"testing"
)

func TestToFloat(t *testing.T) {
	tests := []struct {
		in   any
		want float64
	}{
		{float64(100.5), 100.5},
		{float64(0), 0},
		{"250.75", 250.75},
		{"0", 0},
		{"bad", 0},
		{nil, 0},
		{true, 0},
		{int(5), 0},
	}
	for _, tt := range tests {
		got := toFloat(tt.in)
		if got != tt.want {
			t.Errorf("toFloat(%v) = %v, want %v", tt.in, got, tt.want)
		}
	}
}

func TestFormatFloat(t *testing.T) {
	tests := []struct {
		in   float64
		want string
	}{
		{100.0, "100"},
		{100.50, "100.5"},
		{100.55, "100.55"},
		{0.0, "0"},
		{1234.10, "1234.1"},
	}
	for _, tt := range tests {
		got := formatFloat(tt.in)
		if got != tt.want {
			t.Errorf("formatFloat(%v) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestTrim(t *testing.T) {
	tests := []struct {
		s    string
		max  int
		want string
	}{
		{"hello", 10, "hello"},
		{"hello world", 5, "hello..."},
		{"", 5, ""},
		{"abc", 3, "abc"},
		{"abcd", 3, "abc..."},
	}
	for _, tt := range tests {
		got := trim(tt.s, tt.max)
		if got != tt.want {
			t.Errorf("trim(%q, %d) = %q, want %q", tt.s, tt.max, got, tt.want)
		}
	}
}

func TestParseTakeQR(t *testing.T) {
	tests := []struct {
		name        string
		body        string
		wantURL     string
		wantPayload string
		wantID      int64
	}{
		{
			name:        "full response with id",
			body:        `{"data":{"id":3190530,"url":"https://qr.nspk.ru/BD100027G","payload":"BD100027G"}}`,
			wantURL:     "https://qr.nspk.ru/BD100027G",
			wantPayload: "BD100027G",
			wantID:      3190530,
		},
		{
			name:        "empty url",
			body:        `{"data":{"id":1,"url":"","payload":"pay123"}}`,
			wantURL:     "",
			wantPayload: "pay123",
			wantID:      1,
		},
		{
			name:        "missing data",
			body:        `{}`,
			wantURL:     "",
			wantPayload: "",
			wantID:      0,
		},
		{
			name:        "invalid json",
			body:        `not-json`,
			wantURL:     "",
			wantPayload: "",
			wantID:      0,
		},
		{
			name:        "whitespace trimmed",
			body:        `{"data":{"id":99,"url":" https://qr.nspk.ru/X ","payload":" abc "}}`,
			wantURL:     "https://qr.nspk.ru/X",
			wantPayload: "abc",
			wantID:      99,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotURL, gotPayload, gotID := parseTakeQR(tt.body)
			if gotID != tt.wantID {
				t.Errorf("id = %d, want %d", gotID, tt.wantID)
			}
			if gotURL != tt.wantURL {
				t.Errorf("url = %q, want %q", gotURL, tt.wantURL)
			}
			if gotPayload != tt.wantPayload {
				t.Errorf("payload = %q, want %q", gotPayload, tt.wantPayload)
			}
		})
	}
}
