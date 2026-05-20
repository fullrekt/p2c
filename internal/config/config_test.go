package config

import (
	"os"
	"testing"
)

func TestIntFromEnv(t *testing.T) {
	tests := []struct {
		name     string
		key      string
		val      string
		fallback int
		want     int
	}{
		{"valid value", "TEST_INT", "5", 1, 5},
		{"empty uses fallback", "TEST_INT", "", 3, 3},
		{"non-numeric uses fallback", "TEST_INT", "abc", 2, 2},
		{"zero uses fallback", "TEST_INT", "0", 4, 4},
		{"negative uses fallback", "TEST_INT", "-1", 7, 7},
		{"whitespace trimmed", "TEST_INT", "  10  ", 1, 10},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.val != "" {
				os.Setenv(tt.key, tt.val)
				defer os.Unsetenv(tt.key)
			} else {
				os.Unsetenv(tt.key)
			}
			got := intFromEnv(tt.key, tt.fallback)
			if got != tt.want {
				t.Errorf("intFromEnv(%q, %d) = %d, want %d", tt.key, tt.fallback, got, tt.want)
			}
		})
	}
}
