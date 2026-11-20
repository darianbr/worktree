package wtree

import (
	"fmt"
	"sort"
	"strings"
)

func RenderCaddyfile(routes []Route) ([]byte, error) {
	routes = append([]Route(nil), routes...)
	sort.Slice(routes, func(i, j int) bool { return routes[i].Slug < routes[j].Slug })
	var b strings.Builder
	b.WriteString("{\n\tauto_https disable_redirects\n}\n")
	for _, r := range routes {
		if slug, err := ResolveSlug(r.Slug, ""); err != nil || slug != r.Slug || r.Port == 0 {
			return nil, fmt.Errorf("invalid route %q:%d", r.Slug, r.Port)
		}
		fmt.Fprintf(&b, "\nhttps://%s.wtree.dev {\n\tbind 127.0.0.1\n\ttls /tls/fullchain.pem /tls/privkey.pem\n\treverse_proxy 127.0.0.1:%d\n}\n", r.Slug, r.Port)
	}
	if len(routes) == 0 {
		b.WriteString("\nhttps://*.wtree.dev {\n\tbind 127.0.0.1\n\ttls /tls/fullchain.pem /tls/privkey.pem\n\trespond \"No active route\" 404\n}\n")
	}
	return []byte(b.String()), nil
}
