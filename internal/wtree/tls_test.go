package wtree

import (
	"bytes"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testPaths(t *testing.T) Paths {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "tls"), 0700); err != nil {
		t.Fatal(err)
	}
	return Paths{
		Dir:      dir,
		Cert:     filepath.Join(dir, "tls", "fullchain.pem"),
		Key:      filepath.Join(dir, "tls", "privkey.pem"),
		Registry: filepath.Join(dir, "routes.json"),
		Config:   filepath.Join(dir, "Caddyfile"),
	}
}

func caPath(p Paths) string { return filepath.Join(p.Dir, "tls", "ca.pem") }

func parseFirstCert(t *testing.T, path string) *x509.Certificate {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(data)
	if block == nil {
		t.Fatalf("no PEM in %s", path)
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	return cert
}

func TestProvisionGeneratesWildcard(t *testing.T) {
	p := testPaths(t)
	var hint bytes.Buffer
	generated, err := ProvisionTLS(p, &hint)
	if err != nil || !generated {
		t.Fatalf("generated=%v err=%v", generated, err)
	}
	if err := validateCertificate(p); err != nil {
		t.Fatalf("generated cert fails validation: %v", err)
	}
	leaf := parseFirstCert(t, p.Cert)
	ca := parseFirstCert(t, caPath(p))
	leafLife := leaf.NotAfter.Sub(leaf.NotBefore)
	if leafLife < 300*24*time.Hour || leafLife > 400*24*time.Hour {
		t.Fatalf("leaf validity %v not ~1 year", leafLife)
	}
	caLife := ca.NotAfter.Sub(ca.NotBefore)
	if caLife < 3500*24*time.Hour || caLife > 3700*24*time.Hour {
		t.Fatalf("CA validity %v not ~10 years", caLife)
	}
	lines := strings.Split(strings.TrimSpace(hint.String()), "\n")
	if len(lines) != 1 {
		t.Fatalf("hint should be exactly one line, got %d: %q", len(lines), hint.String())
	}
	if !strings.Contains(lines[0], caPath(p)) {
		t.Fatalf("hint does not mention CA path: %q", lines[0])
	}
}

func TestProvisionIdempotent(t *testing.T) {
	p := testPaths(t)
	var h1, h2 bytes.Buffer
	if _, err := ProvisionTLS(p, &h1); err != nil {
		t.Fatal(err)
	}
	caBefore, err := os.ReadFile(caPath(p))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ProvisionTLS(p, &h2); err != nil {
		t.Fatal(err)
	}
	if h2.Len() != 0 {
		t.Fatalf("second call printed hint: %q", h2.String())
	}
	caAfter, err := os.ReadFile(caPath(p))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(caBefore, caAfter) {
		t.Fatal("CA changed on idempotent re-provision")
	}
}

func TestProvisionReusesCAForExpiredLeaf(t *testing.T) {
	p := testPaths(t)
	var h1 bytes.Buffer
	if _, err := ProvisionTLS(p, &h1); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(p.Cert); err != nil {
		t.Fatal(err)
	}
	caBefore, err := os.ReadFile(caPath(p))
	if err != nil {
		t.Fatal(err)
	}
	var h2 bytes.Buffer
	generated, err := ProvisionTLS(p, &h2)
	if err != nil {
		t.Fatal(err)
	}
	if generated {
		t.Fatal("regenerated CA when only the leaf was missing")
	}
	caAfter, err := os.ReadFile(caPath(p))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(caBefore, caAfter) {
		t.Fatal("CA changed on leaf regeneration")
	}
	if h2.Len() != 0 {
		t.Fatalf("hint re-shown: %q", h2.String())
	}
}

func TestProvisionRespectsExistingValidCert(t *testing.T) {
	p := testPaths(t)
	testCertificate(t, p, []string{"*.wtree.dev"}, time.Now().Add(-time.Hour), time.Now().Add(time.Hour), false)
	var hint bytes.Buffer
	generated, err := ProvisionTLS(p, &hint)
	if generated || err != nil {
		t.Fatalf("must not touch user-provisioned certs: generated=%v err=%v", generated, err)
	}
	if _, err := os.Stat(caPath(p)); !os.IsNotExist(err) {
		t.Fatalf("created a CA for a user-provisioned cert: %v", err)
	}
}
