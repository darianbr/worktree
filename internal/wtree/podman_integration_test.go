package wtree

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// isolatedPodmanRunner redirects only the adapter's fixed resource names. It
// leaves the image, network, mount options and Caddy commands unchanged.
type isolatedPodmanRunner struct {
	name, candidate, token, dataDir string
}

func (r *isolatedPodmanRunner) Run(ctx context.Context, bin string, args ...string) ([]byte, error) {
	args = append([]string(nil), args...)
	for i, arg := range args {
		switch arg {
		case containerName:
			args[i] = r.name
		case "name=^/" + containerName + "$":
			args[i] = "name=^/" + r.name + "$"
		case "wtree-caddy-data:/data":
			args[i] = r.dataDir + ":/data:z"
		}
	}
	if len(args) > 0 && args[0] == "run" {
		extra := []string{"--pull=never", "--label", "dev.wtree.integration=" + r.token}
		if len(args) > 1 && args[1] == "--rm" {
			extra = append(extra, "--name", r.candidate)
		}
		args = append(append(args[:1:1], extra...), args[1:]...)
	}
	out, err := (ExecRunner{}).Run(ctx, bin, args...)
	if len(args) > 0 && args[0] == "ps" {
		out = []byte(strings.ReplaceAll(string(out), r.name, containerName))
	}
	return out, err
}

func (r *isolatedPodmanRunner) cleanup(t *testing.T, bin string) {
	t.Helper()
	for _, name := range []string{r.name, r.candidate} {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		out, err := (ExecRunner{}).Run(ctx, bin, "inspect", "--format", "{{json .}}", name)
		if err == nil {
			var info struct {
				ID     string `json:"Id"`
				Config struct{ Labels map[string]string }
			}
			if err := json.Unmarshal(out, &info); err != nil {
				t.Errorf("inspect temporary container %s: %v", name, err)
			} else if info.ID == "" || info.Config.Labels["dev.wtree.integration"] != r.token {
				t.Errorf("refusing to remove unowned container %s", name)
			} else {
				if t.Failed() {
					logs, _ := (ExecRunner{}).Run(ctx, bin, "logs", info.ID)
					t.Logf("temporary container logs: %s", logs)
				}
				if out, err := (ExecRunner{}).Run(ctx, bin, "rm", "-f", info.ID); err != nil {
					t.Errorf("remove temporary container %s: %v: %s", name, err, out)
				}
			}
		} else {
			_, existsErr := (ExecRunner{}).Run(ctx, bin, "container", "exists", name)
			var exitErr *exec.ExitError
			if !errors.As(existsErr, &exitErr) || exitErr.ExitCode() != 1 {
				t.Errorf("cannot confirm temporary container %s is absent: inspect: %v: %s; exists: %v", name, err, out, existsErr)
			}
		}
		cancel()
	}
}

func integrationCertificate(t *testing.T, configDir, tlsDir string) (Paths, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "*.wtree.dev"},
		DNSNames: []string{"*.wtree.dev"}, NotBefore: time.Now().Add(-time.Hour),
		NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, IsCA: true, BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	p := Paths{Dir: configDir, Config: filepath.Join(configDir, "Caddyfile"), Cert: filepath.Join(tlsDir, "cert.pem"), Key: filepath.Join(tlsDir, "key.pem")}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	if err := os.WriteFile(p.Cert, certPEM, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p.Key, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0600); err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(certPEM) {
		t.Fatal("add temporary certificate to test trust pool")
	}
	return p, pool
}

func integrationPort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return port
}

func TestPodmanCaddyIntegration(t *testing.T) {
	if os.Getenv("WTREE_PODMAN_INTEGRATION") != "1" {
		t.Skip("set WTREE_PODMAN_INTEGRATION=1 to run the rootless Podman integration test")
	}
	if runtime.GOOS != "linux" {
		t.Skip("rootless host-network test requires Linux")
	}
	// This test relabels only the disposable files it creates below.
	t.Setenv("WTREE_SELINUX_RELABEL", "1")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	bin := "podman"
	out, err := (ExecRunner{}).Run(ctx, bin, "info", "--format", "{{.Host.Security.Rootless}}")
	if err != nil || strings.TrimSpace(string(out)) != "true" {
		t.Fatalf("rootless Podman required: %v: %s", err, out)
	}
	out, err = (ExecRunner{}).Run(ctx, bin, "info", "--format", "{{.Host.Security.SELinuxEnabled}}")
	if err != nil {
		t.Fatalf("check Podman SELinux support: %v: %s", err, out)
	}
	selinuxEnabled := strings.TrimSpace(string(out)) == "true"
	t.Logf("Podman SELinux enabled: %t", selinuxEnabled)
	if out, err := (ExecRunner{}).Run(ctx, bin, "image", "exists", caddyImage); err != nil {
		t.Fatalf("pinned image %s must already be cached: %v: %s", caddyImage, err, out)
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		t.Fatal(err)
	}
	token := hex.EncodeToString(random[:])
	dir := t.TempDir()
	configDir := filepath.Join(dir, "config")
	tlsDir := filepath.Join(dir, "tls")
	dataDir := filepath.Join(dir, "data")
	for _, path := range []string{configDir, tlsDir, dataDir} {
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	runner := &isolatedPodmanRunner{name: "wtree-integration-" + token, candidate: "wtree-validation-" + token, token: token, dataDir: dataDir}
	t.Cleanup(func() { runner.cleanup(t, bin) })
	e := Engine{Binary: bin, Runner: runner}
	p, pool := integrationCertificate(t, configDir, tlsDir)
	if err := validateCertificate(p); err != nil {
		t.Fatalf("validate test certificate: %v", err)
	}
	upstream := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "upstream:%s:%s", r.Host, r.URL.Path)
	}))
	upstream.Listener, err = net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	upstream.Start()
	defer upstream.Close()
	upstreamPort := upstream.Listener.Addr().(*net.TCPAddr).Port
	httpsPort, adminPort := integrationPort(t), integrationPort(t)
	for adminPort == httpsPort {
		adminPort = integrationPort(t)
	}
	t.Logf("temporary container %s; HTTPS 127.0.0.1:%d; admin 127.0.0.1:%d; upstream 127.0.0.1:%d", runner.name, httpsPort, adminPort, upstreamPort)
	writeProductionConfig := func(routes []Route) {
		t.Helper()
		config, err := RenderCaddyfile(routes)
		if err != nil {
			t.Fatal(err)
		}
		if err := atomicConfig(p.Config, config); err != nil {
			t.Fatal(err)
		}
	}
	writeConfig := func(routes []Route) {
		t.Helper()
		config, err := RenderCaddyfile(routes)
		if err != nil {
			t.Fatal(err)
		}
		// Only global ports differ from the production-rendered Caddyfile.
		config = []byte(strings.Replace(string(config), "{\n", fmt.Sprintf("{\n\thttps_port %d\n\tadmin 127.0.0.1:%d\n", httpsPort, adminPort), 1))
		if err := atomicConfig(p.Config, config); err != nil {
			t.Fatal(err)
		}
	}
	state, err := e.State(ctx)
	if err != nil || state != ContainerAbsent {
		t.Fatalf("initial container state: %v, %v", state, err)
	}
	writeProductionConfig(nil)
	if err := e.ValidateCandidate(ctx, p, ContainerAbsent); err != nil {
		t.Fatalf("validate empty rendered configuration: %v", err)
	}
	t.Log("empty rendered configuration validated by Caddy")
	if err := os.WriteFile(p.Config, []byte("this is not a valid Caddyfile {\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := e.ValidateCandidate(ctx, p, ContainerAbsent); err == nil {
		t.Fatal("invalid Caddyfile was accepted")
	}
	t.Log("invalid configuration rejected by Caddy")
	writeProductionConfig([]Route{{Slug: "first", Port: uint16(upstreamPort)}})
	if err := e.ValidateCandidate(ctx, p, ContainerAbsent); err != nil {
		t.Fatalf("validate route configuration: %v", err)
	}
	t.Log("unmodified rendered route validated by Caddy")
	writeConfig([]Route{{Slug: "first", Port: uint16(upstreamPort)}})
	if err := e.ValidateCandidate(ctx, p, ContainerAbsent); err != nil {
		t.Fatalf("validate high-port route configuration: %v", err)
	}
	if err := e.Start(ctx, p); err != nil {
		t.Fatalf("start temporary container: %v", err)
	}
	state, err = e.State(ctx)
	if err != nil || state != ContainerRunning {
		t.Fatalf("running state: %v, %v", state, err)
	}
	if err := e.CheckMounts(ctx, p, state); err != nil {
		t.Fatalf("check real mounts: %v", err)
	}
	t.Log("verified real bind mounts are read-only")
	if selinuxEnabled {
		out, err := runner.Run(ctx, bin, "inspect", "--format", "{{.ProcessLabel}}", containerName)
		if err != nil || strings.TrimSpace(string(out)) == "" {
			t.Fatalf("container must retain an SELinux process label: %v: %s", err, out)
		}
		t.Logf("container SELinux process label: %s", strings.TrimSpace(string(out)))
	}
	if err := e.ValidateCandidate(ctx, p, state); err != nil {
		t.Fatalf("validate in running container: %v", err)
	}
	transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, ServerName: "first.wtree.dev", MinVersion: tls.VersionTLS12}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 2 * time.Second}
	get := func(host string) string {
		t.Helper()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://127.0.0.1:"+strconv.Itoa(httpsPort)+"/probe", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Host = host
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("HTTPS request: %v", err)
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		if err != nil || resp.StatusCode != http.StatusOK {
			t.Fatalf("proxy response: status=%s body=%q error=%v", resp.Status, body, err)
		}
		return string(body)
	}
	if got := get("first.wtree.dev"); got != "upstream:first.wtree.dev:/probe" {
		t.Fatalf("unexpected upstream response: %q", got)
	}
	t.Log("verified TLS request reached the host-loopback upstream")
	conn, err := net.DialTimeout("tcp4", net.JoinHostPort("127.0.0.2", strconv.Itoa(httpsPort)), time.Second)
	if err == nil {
		conn.Close()
		t.Fatal("Caddy listener is reachable on 127.0.0.2 instead of only 127.0.0.1")
	}
	if !errors.Is(err, syscall.ECONNREFUSED) {
		t.Fatalf("could not establish whether the alternate loopback address is listening: %v", err)
	}
	t.Log("connection to 127.0.0.2 was refused")
	writeConfig([]Route{{Slug: "second", Port: uint16(upstreamPort)}})
	if err := e.ValidateCandidate(ctx, p, ContainerRunning); err != nil {
		t.Fatalf("validate changed route: %v", err)
	}
	if err := e.Reload(ctx); err != nil {
		t.Fatalf("reload changed route: %v", err)
	}
	if got := get("second.wtree.dev"); got != "upstream:second.wtree.dev:/probe" {
		t.Fatalf("unexpected reloaded upstream response: %q", got)
	}
	t.Log("reloaded route reached the host-loopback upstream")
	if err := e.Stop(ctx); err != nil {
		t.Fatalf("stop container: %v", err)
	}
	state, err = e.State(ctx)
	if err != nil || state != ContainerStopped {
		t.Fatalf("stopped state: %v, %v", state, err)
	}
	if err := e.ValidateCandidate(ctx, p, state); err != nil {
		t.Fatalf("validate with stopped container: %v", err)
	}
	t.Setenv("WTREE_SELINUX_RELABEL", "")
	if err := e.StartExisting(ctx); err == nil || !strings.Contains(err.Error(), "WTREE_SELINUX_RELABEL=1") {
		t.Fatalf("restart without relabel consent must fail: %v", err)
	}
	state, err = e.State(ctx)
	if err != nil || state != ContainerStopped {
		t.Fatalf("container must remain stopped without consent: %v, %v", state, err)
	}
	t.Log("restart without relabel consent was refused")
	t.Setenv("WTREE_SELINUX_RELABEL", "1")
	if err := e.StartExisting(ctx); err != nil {
		t.Fatalf("restart container: %v", err)
	}
	if err := e.Remove(ctx); err != nil {
		t.Fatalf("remove owned container: %v", err)
	}
	state, err = e.State(ctx)
	if err != nil || state != ContainerAbsent {
		t.Fatalf("removed state: %v, %v", state, err)
	}
	t.Log("adapter stopped, restarted, and removed its temporary container")
}

// integrationSelfHealDeps builds a full CLI Dependencies backed by an isolated
// container (unique name + token), a temp config dir, and a provisioned local
// CA. It returns the deps, the engine, the runner (for cleanup), the CA pool,
// and the upstream port.
func integrationSelfHealDeps(t *testing.T) (Dependencies, Engine, *isolatedPodmanRunner, *x509.CertPool, int) {
	t.Helper()
	if os.Getenv("WTREE_PODMAN_INTEGRATION") != "1" {
		t.Skip("set WTREE_PODMAN_INTEGRATION=1 to run the rootless Podman integration test")
	}
	if runtime.GOOS != "linux" {
		t.Skip("rootless host-network test requires Linux")
	}
	t.Setenv("WTREE_SELINUX_RELABEL", "1")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	bin := "podman"
	if out, err := (ExecRunner{}).Run(ctx, bin, "info", "--format", "{{.Host.Security.Rootless}}"); err != nil || strings.TrimSpace(string(out)) != "true" {
		t.Fatalf("rootless Podman required: %v: %s", err, out)
	}
	if out, err := (ExecRunner{}).Run(ctx, bin, "image", "exists", caddyImage); err != nil {
		t.Fatalf("pinned image %s must already be cached: %v: %s", caddyImage, err, out)
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		t.Fatal(err)
	}
	token := hex.EncodeToString(random[:])
	dir := t.TempDir()
	configDir := filepath.Join(dir, "config")
	dataDir := filepath.Join(dir, "data")
	for _, path := range []string{configDir, dataDir} {
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	runner := &isolatedPodmanRunner{name: "wtree-integration-" + token, candidate: "wtree-validation-" + token, token: token, dataDir: dataDir}
	t.Cleanup(func() { runner.cleanup(t, bin) })
	e := Engine{Binary: bin, Runner: runner}
	p := Paths{Dir: configDir, Config: filepath.Join(configDir, "Caddyfile"), Cert: filepath.Join(configDir, "tls", "fullchain.pem"), Key: filepath.Join(configDir, "tls", "privkey.pem"), Registry: filepath.Join(configDir, "routes.json")}
	var hint bytes.Buffer
	if _, err := ProvisionTLS(p, &hint); err != nil {
		t.Fatalf("provision TLS: %v", err)
	}
	pool := x509.NewCertPool()
	caData, err := os.ReadFile(filepath.Join(configDir, "tls", "ca.pem"))
	if err != nil || !pool.AppendCertsFromPEM(caData) {
		t.Fatalf("load provisioned CA: %v", err)
	}
	r, err := OpenRegistry(p.Registry)
	if err != nil {
		t.Fatal(err)
	}
	upstream := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "upstream:%s", r.URL.Path)
	}))
	upstream.Listener, err = net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	upstream.Start()
	t.Cleanup(upstream.Close)
	upstreamPort := upstream.Listener.Addr().(*net.TCPAddr).Port
	d := Dependencies{Paths: p, Registry: r, Git: testGit{root: "/work/selfheal", branch: "feat/integration"}, Stderr: io.Discard, Engine: func(context.Context) (LifecycleEngine, error) { return &e, nil }}
	return d, e, runner, pool, upstreamPort
}

func httpsGet(t *testing.T, ctx context.Context, pool *x509.CertPool, host, path string) string {
	t.Helper()
	transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, ServerName: host, MinVersion: tls.VersionTLS12}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 3 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://127.0.0.1:443"+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = host
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("HTTPS request through 443: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("proxy response: status=%s body=%q error=%v", resp.Status, body, err)
	}
	return string(body)
}

// TestSelfHealAfterContainerKill proves the core self-healing guarantee: after
// the Caddy container is killed mid-session, the next `status` converges it back
// to serving with zero user action.
func TestSelfHealAfterContainerKill(t *testing.T) {
	d, e, runner, pool, upstreamPort := integrationSelfHealDeps(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	var out, errOut bytes.Buffer
	if code := Run(ctx, []string{"up", "--port", strconv.Itoa(upstreamPort), "--slug", "selfheal"}, &out, &errOut, d); code != 0 {
		t.Fatalf("up: %d %q %q", code, out.String(), errOut.String())
	}
	state, err := e.State(ctx)
	if err != nil || state != ContainerRunning {
		t.Fatalf("container not running after up: %v %v", state, err)
	}
	if got := httpsGet(t, ctx, pool, "selfheal.wtree.dev", "/probe"); got != "upstream:/probe" {
		t.Fatalf("unexpected upstream response before kill: %q", got)
	}
	// Kill the container mid-session (the isolated runner renames it).
	if out, err := (ExecRunner{}).Run(ctx, "podman", "kill", runner.name); err != nil {
		t.Fatalf("kill container: %v: %s", err, out)
	}
	state, err = e.State(ctx)
	if err != nil || state != ContainerStopped {
		t.Fatalf("container not stopped after kill: %v %v", state, err)
	}
	// The next status must self-heal to serving with no user action.
	out.Reset()
	errOut.Reset()
	if code := Run(ctx, []string{"status"}, &out, &errOut, d); code != 0 {
		t.Fatalf("status after kill: %d %q %q", code, out.String(), errOut.String())
	}
	if !strings.Contains(out.String(), "serving") {
		t.Fatalf("status not serving after self-heal: %q", out.String())
	}
	state, err = e.State(ctx)
	if err != nil || state != ContainerRunning {
		t.Fatalf("container not restarted by status: %v %v", state, err)
	}
	if got := httpsGet(t, ctx, pool, "selfheal.wtree.dev", "/probe"); got != "upstream:/probe" {
		t.Fatalf("unexpected upstream response after self-heal: %q", got)
	}
	t.Log("status self-healed the killed container back to serving")
}

// TestDownStopsContainerAndCleansConfig proves that removing the last route stops
// the proxy (443 bound only while active) and removes the on-disk Caddyfile, and
// that wtree leaves no threshold marker behind when it never lowered one.
func TestDownStopsContainerAndCleansConfig(t *testing.T) {
	d, e, _, _, upstreamPort := integrationSelfHealDeps(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	var out, errOut bytes.Buffer
	if code := Run(ctx, []string{"up", "--port", strconv.Itoa(upstreamPort), "--slug", "downstop"}, &out, &errOut, d); code != 0 {
		t.Fatalf("up: %d %q %q", code, out.String(), errOut.String())
	}
	state, err := e.State(ctx)
	if err != nil || state != ContainerRunning {
		t.Fatalf("container not running after up: %v %v", state, err)
	}
	out.Reset()
	errOut.Reset()
	if code := Run(ctx, []string{"down", "--slug", "downstop"}, &out, &errOut, d); code != 0 {
		t.Fatalf("down: %d %q %q", code, out.String(), errOut.String())
	}
	state, err = e.State(ctx)
	if err != nil || state != ContainerStopped {
		t.Fatalf("container not stopped after down: %v %v", state, err)
	}
	if _, err := os.Stat(d.Paths.Config); !os.IsNotExist(err) {
		t.Fatalf("Caddyfile not removed after last route: %v", err)
	}
	if _, err := os.Stat(markerPath(d.Paths)); !os.IsNotExist(err) {
		t.Fatalf("wtree left a threshold marker it did not create: %v", err)
	}
	t.Log("down stopped the proxy and removed the Caddyfile")
}
