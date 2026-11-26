package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadToken(t *testing.T) {
	dir := t.TempDir()

	// DASHBOARD_TOKEN wins and is never written anywhere.
	file := filepath.Join(dir, "unused")
	if tok, gen, err := loadToken("from-env-0123456789", file); err != nil || gen || tok != "from-env-0123456789" {
		t.Fatalf("env token: %q %v %v", tok, gen, err)
	}
	if _, err := os.Stat(file); err == nil {
		t.Fatal("env token was written to the file")
	}
	if _, _, err := loadToken("short", ""); err == nil {
		t.Fatal("short token accepted")
	}

	// Nothing set: a fresh token every time.
	a, gen, err := loadToken("", "")
	if err != nil || !gen || len(a) != 48 {
		t.Fatalf("generated: %q %v %v", a, gen, err)
	}
	if b, _, _ := loadToken("", ""); b == a {
		t.Fatal("two generated tokens are equal")
	}

	// A missing file is created with a generated token, then reused.
	file = filepath.Join(dir, "token")
	first, gen, err := loadToken("", file)
	if err != nil || !gen {
		t.Fatalf("first start: %q %v %v", first, gen, err)
	}
	if info, err := os.Stat(file); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("token file: %v %v", info, err)
	}
	again, gen, err := loadToken("", file)
	if err != nil || gen || again != first {
		t.Fatalf("restart: got %q (generated %v, %v), want %q", again, gen, err, first)
	}

	// A user-provided file (Docker secret) is read and trimmed.
	secret := filepath.Join(dir, "secret")
	os.WriteFile(secret, []byte("  my-secret-token-123  \n"), 0o600)
	if tok, gen, err := loadToken("", secret); err != nil || gen || tok != "my-secret-token-123" {
		t.Fatalf("secret file: %q %v %v", tok, gen, err)
	}
	empty := filepath.Join(dir, "empty")
	os.WriteFile(empty, nil, 0o600)
	if _, _, err := loadToken("", empty); err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("empty file: %v", err)
	}
}
