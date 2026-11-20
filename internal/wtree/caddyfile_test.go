package wtree

import (
	"strings"
	"testing"
)

func TestRenderCaddyfile(t *testing.T) {
	routes := []Route{{Slug: "zebra", Port: 3001}, {Slug: "apple", Port: 3002}}
	data, err := RenderCaddyfile(routes)
	if err != nil {
		t.Fatal(err)
	}
	config := string(data)
	for _, part := range []string{"auto_https disable_redirects", "https://apple.wtree.dev", "https://zebra.wtree.dev", "bind 127.0.0.1", "tls /tls/fullchain.pem /tls/privkey.pem", "reverse_proxy 127.0.0.1:3001", "reverse_proxy 127.0.0.1:3002"} {
		if !strings.Contains(config, part) {
			t.Errorf("missing %q in %s", part, config)
		}
	}
	if strings.Index(config, "apple.wtree.dev") > strings.Index(config, "zebra.wtree.dev") {
		t.Fatal("routes not sorted")
	}
	if routes[0].Slug != "zebra" {
		t.Fatal("renderer mutated caller slice")
	}
}

func TestRenderCaddyfileRejectsInvalidRoute(t *testing.T) {
	for _, route := range []Route{{Slug: "bad\nsite", Port: 3001}, {Slug: "valid", Port: 0}} {
		if _, err := RenderCaddyfile([]Route{route}); err == nil {
			t.Fatalf("accepted invalid route: %+v", route)
		}
	}
}
