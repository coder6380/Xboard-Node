package main

import "testing"

func TestParseMemLimit(t *testing.T) {
	valid := []struct {
		in   string
		want int64
	}{
		{"256MiB", 256 << 20},
		{"256MB", 256_000_000},
		{"1GB", 1_000_000_000},
		{"1GiB", 1 << 30},
		{"512KB", 512_000},
		{"512KiB", 512 << 10},
		{"256M", 256 << 20},
		{"1024B", 1024},
		{"4096", 4096},
	}
	for _, tc := range valid {
		got, err := parseMemLimit(tc.in)
		if err != nil {
			t.Errorf("parseMemLimit(%q) error = %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("parseMemLimit(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}

	invalid := []string{"", "0MiB", "-10MiB", "1.5GiB", "256XB", "abc", "99999999TiB"}
	for _, in := range invalid {
		if got, err := parseMemLimit(in); err == nil {
			t.Errorf("parseMemLimit(%q) = %d, want error", in, got)
		}
	}
}
