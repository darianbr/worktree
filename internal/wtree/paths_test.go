package wtree

import (
	"path/filepath"
	"testing"
)

func TestPaths(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("WTREE_CONFIG_DIR", dir)
	t.Setenv("WTREE_CERT_FILE", "")
	t.Setenv("WTREE_KEY_FILE", "")
	p, err := ConfigPaths()
	if err != nil {
		t.Fatal(err)
	}
	if p.Dir != dir || p.Cert != filepath.Join(dir, "tls", "fullchain.pem") || p.Key != filepath.Join(dir, "tls", "privkey.pem") {
		t.Fatalf("unexpected paths: %+v", p)
	}
	t.Setenv("WTREE_CERT_FILE", "/custom/cert.pem")
	t.Setenv("WTREE_KEY_FILE", "/custom/key.pem")
	p, err = ConfigPaths()
	if err != nil || p.Cert != "/custom/cert.pem" || p.Key != "/custom/key.pem" {
		t.Fatalf("unexpected overrides: %+v, %v", p, err)
	}
}
