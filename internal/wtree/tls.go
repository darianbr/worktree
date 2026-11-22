package wtree

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

const (
	wtreeCACommonName = "wtree local CA"
	caValidity        = 10 * 365 * 24 * time.Hour
	leafValidity      = 365 * 24 * time.Hour
)

// writeFileAtomic writes data to path with the given mode, replacing any
// existing file. The write is staged in a temp file and renamed into place so a
// crash never leaves a half-written file behind.
func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if err = f.Chmod(mode); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(name, path)
}

type caMaterial struct {
	key  *ecdsa.PrivateKey
	cert *x509.Certificate
}

func loadCA(p Paths) (caMaterial, bool) {
	var m caMaterial
	keyData, err := os.ReadFile(filepath.Join(p.Dir, "tls", "ca.key"))
	if err != nil {
		return m, false
	}
	certData, err := os.ReadFile(filepath.Join(p.Dir, "tls", "ca.pem"))
	if err != nil {
		return m, false
	}
	keyBlock, _ := pem.Decode(keyData)
	if keyBlock == nil || keyBlock.Type != "PRIVATE KEY" {
		return m, false
	}
	parsedKey, err := x509.ParsePKCS8PrivateKey(keyBlock.Bytes)
	if err != nil {
		return m, false
	}
	ecdsaKey, ok := parsedKey.(*ecdsa.PrivateKey)
	if !ok {
		return m, false
	}
	certBlock, _ := pem.Decode(certData)
	if certBlock == nil {
		return m, false
	}
	cert, err := x509.ParseCertificate(certBlock.Bytes)
	if err != nil || !cert.IsCA {
		return m, false
	}
	m.key = ecdsaKey
	m.cert = cert
	return m, true
}

func generateCA(now time.Time) (caMaterial, error) {
	var m caMaterial
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return m, fmt.Errorf("generate CA key: %w", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: wtreeCACommonName},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(caValidity),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return m, fmt.Errorf("create CA certificate: %w", err)
	}
	m.key = key
	m.cert, err = x509.ParseCertificate(der)
	if err != nil {
		return m, fmt.Errorf("parse CA certificate: %w", err)
	}
	return m, nil
}

func issueLeaf(ca caMaterial, now time.Time) (*ecdsa.PrivateKey, []byte, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("generate leaf key: %w", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: "*.wtree.dev"},
		DNSNames:     []string{"*.wtree.dev", "wtree.dev"},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.Add(leafValidity),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		return nil, nil, fmt.Errorf("create leaf certificate: %w", err)
	}
	return key, der, nil
}

func trustHint(p Paths) string {
	ca := filepath.Join(p.Dir, "tls", "ca.pem")
	switch runtime.GOOS {
	case "darwin":
		return fmt.Sprintf("trust the local CA once so browsers accept wtree URLs: sudo security add-trusted-cert -d -r trustRoot -k /Library/Keychains/System.keychain %s", ca)
	default:
		return fmt.Sprintf("trust the local CA once so browsers accept wtree URLs: sudo cp %s /usr/local/share/ca-certificates/wtree.crt && sudo update-ca-certificates", ca)
	}
}

// ProvisionTLS ensures a valid *.wtree.dev leaf exists at p.Cert/p.Key. If one
// already validates it is left untouched and (false, nil) is returned.
// Otherwise the local CA — reused when present, generated on first use — signs
// a fresh wildcard leaf. A brand-new CA prints a one-line trust hint exactly
// once, guarded by a marker file so later leaf regenerations stay silent.
func ProvisionTLS(p Paths, hint io.Writer) (bool, error) {
	if err := validateCertificate(p); err == nil {
		return false, nil
	}
	now := time.Now()
	ca, reused := loadCA(p)
	generatedCA := false
	if !reused {
		var err error
		ca, err = generateCA(now)
		if err != nil {
			return false, err
		}
		generatedCA = true
	}
	leafKey, leafDER, err := issueLeaf(ca, now)
	if err != nil {
		return false, err
	}
	tlsDir := filepath.Join(p.Dir, "tls")
	if generatedCA {
		keyDER, err := x509.MarshalPKCS8PrivateKey(ca.key)
		if err != nil {
			return false, fmt.Errorf("encode CA key: %w", err)
		}
		if err := writeFileAtomic(filepath.Join(tlsDir, "ca.key"), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0600); err != nil {
			return false, fmt.Errorf("write CA key: %w", err)
		}
		if err := writeFileAtomic(filepath.Join(tlsDir, "ca.pem"), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ca.cert.Raw}), 0644); err != nil {
			return false, fmt.Errorf("write CA certificate: %w", err)
		}
	}
	if err := writeFileAtomic(p.Cert, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafDER}), 0600); err != nil {
		return false, fmt.Errorf("write leaf certificate: %w", err)
	}
	leafKeyDER, err := x509.MarshalPKCS8PrivateKey(leafKey)
	if err != nil {
		return false, fmt.Errorf("encode leaf key: %w", err)
	}
	if err := writeFileAtomic(p.Key, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: leafKeyDER}), 0600); err != nil {
		return false, fmt.Errorf("write leaf key: %w", err)
	}
	marker := filepath.Join(tlsDir, "ca.trusted-hint-shown")
	if generatedCA {
		if _, err := os.Stat(marker); errors.Is(err, os.ErrNotExist) {
			if err := writeFileAtomic(marker, []byte("shown\n"), 0600); err != nil {
				return false, fmt.Errorf("write trust hint marker: %w", err)
			}
			if hint != nil {
				fmt.Fprintln(hint, trustHint(p))
			}
		}
	}
	return generatedCA, nil
}
