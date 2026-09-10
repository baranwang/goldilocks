package main

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestReleaseChecksumGate(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.Mkdir("scripts", 0755); err != nil {
		t.Fatal(err)
	}
	data := []byte("release binary")
	path := filepath.Join("scripts", "goldilocks-Linux-x86_64")
	if err := os.WriteFile(path, data, 0755); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, manifest string
		valid          bool
	}{
		{"pinned", fmt.Sprintf("%x  %s\n", sha256.Sum256(data), filepath.Base(path)), true},
		{"wrong digest", fmt.Sprintf("%x  %s\n", sha256.Sum256([]byte("other")), filepath.Base(path)), false},
		{"wrong platform", fmt.Sprintf("%x  goldilocks-Darwin-arm64\n", sha256.Sum256(data)), false},
		{"empty manifest", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.WriteFile("scripts/SHA256SUMS", []byte(tc.manifest), 0644); err != nil {
				t.Fatal(err)
			}
			if err := checkBinary(path); (err == nil) != tc.valid {
				t.Fatalf("valid=%v, error=%v", tc.valid, err)
			}
		})
	}
}
