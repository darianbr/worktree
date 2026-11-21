// wtree-test-server is a small, loopback-only upstream for manual proxy tests.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
)

func newHandler(instance string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(struct {
			Instance string `json:"instance"`
			Host     string `json:"host"`
			Path     string `json:"path"`
		}{instance, r.Host, r.URL.Path})
	})
}

func main() {
	port := flag.Int("port", 3000, "loopback port (1–65535)")
	name := flag.String("name", "test-server", "instance name")
	flag.Parse()
	if *port < 1 || *port > 65535 || *name == "" {
		log.Fatal("port must be 1–65535 and name must not be empty")
	}
	addr := fmt.Sprintf("127.0.0.1:%d", *port)
	log.Printf("%s listening on http://%s", *name, addr)
	log.Fatal(http.ListenAndServe(addr, newHandler(*name)))
}
