package wtree

import (
	"os"
	"path/filepath"
)

type Paths struct {
	Dir, Cert, Key, Registry, Config string
}

func ConfigPaths() (Paths, error) {
	dir := os.Getenv("WTREE_CONFIG_DIR")
	if dir == "" {
		root, err := os.UserConfigDir()
		if err != nil {
			return Paths{}, err
		}
		dir = filepath.Join(root, "wtree")
	}
	cert, key := os.Getenv("WTREE_CERT_FILE"), os.Getenv("WTREE_KEY_FILE")
	if cert == "" {
		cert = filepath.Join(dir, "tls", "fullchain.pem")
	}
	if key == "" {
		key = filepath.Join(dir, "tls", "privkey.pem")
	}
	return Paths{Dir: dir, Cert: cert, Key: key, Registry: filepath.Join(dir, "routes.json"), Config: filepath.Join(dir, "Caddyfile")}, nil
}