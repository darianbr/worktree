package wtree

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type testGit struct {
	root, branch string
	err          error
}

func (g testGit) Worktree(context.Context) (string, string, error) { return g.root, g.branch, g.err }

type testEngine struct {
	paths     Paths
	state     ContainerState
	active    string
	fail      string
	stopError bool
	onReload  func()
}

func (e *testEngine) State(context.Context) (ContainerState, error)            { return e.state, nil }
func (e *testEngine) CheckMounts(context.Context, Paths, ContainerState) error { return nil }
func (e *testEngine) Start(context.Context, Paths) error {
	if e.fail == "start" {
		return errors.New("startup failed")
	}
	if e.fail == "exited" {
		e.state = ContainerStopped
		return nil
	}
	e.state = ContainerRunning
	return e.load()
}
func (e *testEngine) StartExisting(context.Context) error {
	if e.fail == "start" {
		return errors.New("startup failed")
	}
	e.state = ContainerRunning
	return e.load()
}
func (e *testEngine) ValidateCandidate(context.Context, Paths, ContainerState) error {
	if e.fail == "validate" {
		return errors.New("validation failed")
	}
	return nil
}
func (e *testEngine) Reload(context.Context) error {
	if e.fail == "reload" {
		e.fail = ""
		return errors.New("reload failed")
	}
	if e.onReload != nil {
		f := e.onReload
		e.onReload = nil
		f()
	}
	return e.load()
}
func (e *testEngine) Stop(context.Context) error { e.state = ContainerStopped; return nil }
func (e *testEngine) Remove(context.Context) error {
	if e.stopError {
		return errors.New("cannot remove container")
	}
	e.state = ContainerAbsent
	e.active = ""
	return nil
}
func (e *testEngine) load() error {
	b, err := os.ReadFile(e.paths.Config)
	e.active = string(b)
	return err
}

func testCertificate(t *testing.T, p Paths, names []string, before, after time.Time, mismatch bool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test"}, DNSNames: names, NotBefore: before, NotAfter: after, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	if mismatch {
		key, err = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
	}
	k, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(p.Cert, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(p.Key, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: k}), 0600); err != nil {
		t.Fatal(err)
	}
}

func fixture(t *testing.T) (Dependencies, *testEngine, *Registry) {
	t.Helper()
	dir := t.TempDir()
	p := Paths{Dir: dir, Cert: filepath.Join(dir, "cert.pem"), Key: filepath.Join(dir, "key.pem"), Registry: filepath.Join(dir, "routes.json"), Config: filepath.Join(dir, "Caddyfile")}
	testCertificate(t, p, []string{"*.wtree.dev"}, time.Now().Add(-time.Hour), time.Now().Add(time.Hour), false)
	r, err := OpenRegistry(p.Registry)
	if err != nil {
		t.Fatal(err)
	}
	e := &testEngine{paths: p}
	return Dependencies{Paths: p, Registry: r, Git: testGit{root: "/work/tree", branch: "feat/JIRA-12345-feature"}, Engine: func(context.Context) (LifecycleEngine, error) { return e, nil }}, e, r
}
func invoke(d Dependencies, args ...string) (int, string, string) {
	var out, stderr bytes.Buffer
	code := Run(context.Background(), args, &out, &stderr, d)
	return code, out.String(), stderr.String()
}

func TestRunHelpAndInvalidInput(t *testing.T) {
	d, e, _ := fixture(t)
	for _, tc := range []struct {
		args []string
		code int
	}{{[]string{"--help"}, 0}, {[]string{"wat"}, 1}, {[]string{"up", "--port", "0"}, 1}, {[]string{"up", "--port", "65536"}, 1}, {[]string{"up", "--port", "abc"}, 1}, {[]string{"up"}, 1}, {[]string{"up", "--port", "80", "extra"}, 1}} {
		if code, _, _ := invoke(d, tc.args...); code != tc.code {
			t.Errorf("%v: got %d", tc.args, code)
		}
	}
	if e.state != ContainerAbsent {
		t.Fatal("invalid arguments started container")
	}
}

func TestRunLifecycleAndDuplicate(t *testing.T) {
	d, e, r := fixture(t)
	if code, out, err := invoke(d, "up", "--port", "3000"); code != 0 || !strings.Contains(out, "https://jira-12345.wtree.dev") {
		t.Fatalf("up: %d %q %q", code, out, err)
	}
	old := e.active
	if code, _, err := invoke(d, "up", "--port", "4000"); code == 0 || !strings.Contains(err, "use --slug") {
		t.Fatalf("duplicate: %d %q", code, err)
	}
	if e.active != old {
		t.Fatal("duplicate replaced active config")
	}
	if code, _, err := invoke(d, "up", "--port", "4000", "--slug", "Zebra"); code != 0 {
		t.Fatalf("explicit: %s", err)
	}
	if code, out, err := invoke(d, "status"); code != 0 || strings.Index(out, "jira-12345") >= strings.Index(out, "zebra") {
		t.Fatalf("status %d %q %q", code, out, err)
	}
	if code, _, err := invoke(d, "down"); code == 0 || !strings.Contains(err, "--slug") {
		t.Fatalf("ambiguous down: %d %s", code, err)
	}
	if code, _, err := invoke(d, "down", "--slug", "zebra"); code != 0 {
		t.Fatalf("down: %s", err)
	}
	if code, _, err := invoke(d, "down"); code != 0 {
		t.Fatalf("down current: %s", err)
	}
	if code, _, err := invoke(d, "down"); code != 0 {
		t.Fatalf("repeat down: %s", err)
	}
	if routes, err := r.List(); err != nil || len(routes) != 0 {
		t.Fatalf("routes: %v %v", routes, err)
	}
}

func TestRunDuplicateHasHintEvenIfCertificateDisappears(t *testing.T) {
	d, e, _ := fixture(t)
	if code, _, err := invoke(d, "up", "--port", "3000"); code != 0 {
		t.Fatal(err)
	}
	old := e.active
	if err := os.Remove(d.Paths.Cert); err != nil {
		t.Fatal(err)
	}
	if code, _, err := invoke(d, "up", "--port", "4000"); code == 0 || !strings.Contains(err, "use --slug") {
		t.Fatalf("duplicate lost hint: %d %q", code, err)
	}
	if e.active != old {
		t.Fatal("duplicate altered active configuration")
	}
}

func TestRunDetachedRequiresSlug(t *testing.T) {
	d, _, _ := fixture(t)
	d.Git = testGit{root: "/work/tree"}
	if code, _, err := invoke(d, "up", "--port", "3000"); code == 0 || !strings.Contains(err, "--slug") {
		t.Fatalf("detached: %d %q", code, err)
	}
	if code, _, err := invoke(d, "up", "--port", "3000", "--slug", "detached"); code != 0 {
		t.Fatalf("explicit: %s", err)
	}
}

func TestCertificatePreflight(t *testing.T) {
	for _, tc := range []struct {
		name              string
		names             []string
		before, after     time.Time
		mismatch, missing bool
	}{
		{"missing", nil, time.Time{}, time.Time{}, false, true},
		{"mismatched", []string{"*.wtree.dev"}, time.Now().Add(-time.Hour), time.Now().Add(time.Hour), true, false},
		{"specific only", []string{"probe.wtree.dev"}, time.Now().Add(-time.Hour), time.Now().Add(time.Hour), false, false},
		{"wrong name", []string{"*.other.dev"}, time.Now().Add(-time.Hour), time.Now().Add(time.Hour), false, false},
		{"expired", []string{"*.wtree.dev"}, time.Now().Add(-2 * time.Hour), time.Now().Add(-time.Hour), false, false},
		{"not yet valid", []string{"*.wtree.dev"}, time.Now().Add(time.Hour), time.Now().Add(2 * time.Hour), false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, e, r := fixture(t)
			if tc.missing {
				os.Remove(d.Paths.Key)
			} else {
				testCertificate(t, d.Paths, tc.names, tc.before, tc.after, tc.mismatch)
			}
			if code, _, err := invoke(d, "up", "--port", "3000"); code == 0 {
				t.Fatalf("accepted certificate: %q", err)
			}
			routes, err := r.List()
			if err != nil || len(routes) != 0 || e.state != ContainerAbsent {
				t.Fatalf("side effect: %v %v %v", routes, err, e.state)
			}
		})
	}
}

func TestRunRollbackActivationFailures(t *testing.T) {
	for _, failure := range []string{"validate", "reload", "start"} {
		t.Run(failure, func(t *testing.T) {
			d, e, r := fixture(t)
			if code, _, err := invoke(d, "up", "--port", "3000", "--slug", "one"); code != 0 {
				t.Fatal(err)
			}
			original, err := os.ReadFile(d.Paths.Config)
			if err != nil {
				t.Fatal(err)
			}
			active := e.active
			if failure == "start" {
				e.state = ContainerStopped
			}
			e.fail = failure
			if code, _, err := invoke(d, "up", "--port", "4000", "--slug", "two"); code == 0 {
				t.Fatalf("%s unexpectedly succeeded: %s", failure, err)
			}
			config, err := os.ReadFile(d.Paths.Config)
			if err != nil || !bytes.Equal(config, original) {
				t.Fatalf("config not restored: %s %v", config, err)
			}
			if failure != "start" && e.active != active {
				t.Fatal("active configuration changed")
			}
			routes, err := r.List()
			if err != nil || len(routes) != 1 || routes[0].Slug != "one" {
				t.Fatalf("routes: %v %v", routes, err)
			}
		})
	}
}

func TestRunRollbackRegistryCommitFailure(t *testing.T) {
	d, e, r := fixture(t)
	if code, _, err := invoke(d, "up", "--port", "3000", "--slug", "one"); code != 0 {
		t.Fatal(err)
	}
	old, err := os.ReadFile(d.Paths.Config)
	if err != nil {
		t.Fatal(err)
	}
	active := e.active
	originalPath := r.path
	e.onReload = func() { r.path = d.Paths.Dir }
	code, _, message := invoke(d, "up", "--port", "4000", "--slug", "two")
	r.path = originalPath
	if code == 0 {
		t.Fatalf("commit unexpectedly succeeded: %s", message)
	}
	config, err := os.ReadFile(d.Paths.Config)
	if err != nil || !bytes.Equal(old, config) || e.active != active {
		t.Fatalf("failed to compensate: %v active=%q", err, e.active)
	}
	routes, err := r.List()
	if err != nil || len(routes) != 1 || routes[0].Slug != "one" {
		t.Fatalf("registry: %v %v", routes, err)
	}
}

func TestRunFirstStartFailureRestoresAbsence(t *testing.T) {
	for _, failure := range []string{"start", "validate", "exited"} {
		t.Run(failure, func(t *testing.T) {
			d, e, r := fixture(t)
			e.fail = failure
			if code, _, err := invoke(d, "up", "--port", "3000"); code == 0 {
				t.Fatalf("unexpected success: %s", err)
			}
			if _, err := os.Stat(d.Paths.Config); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("candidate remains: %v", err)
			}
			if e.state != ContainerAbsent {
				t.Fatalf("container remains: %v", e.state)
			}
			if routes, err := r.List(); err != nil || len(routes) != 0 {
				t.Fatalf("routes: %v %v", routes, err)
			}
		})
	}
}

func TestRunDownCommitFailureRestoresRoute(t *testing.T) {
	d, e, r := fixture(t)
	if code, _, err := invoke(d, "up", "--port", "3000", "--slug", "one"); code != 0 {
		t.Fatal(err)
	}
	old, err := os.ReadFile(d.Paths.Config)
	if err != nil {
		t.Fatal(err)
	}
	active := e.active
	originalPath := r.path
	e.onReload = func() { r.path = d.Paths.Dir }
	code, _, message := invoke(d, "down", "--slug", "one")
	r.path = originalPath
	if code == 0 {
		t.Fatalf("commit unexpectedly succeeded: %s", message)
	}
	config, err := os.ReadFile(d.Paths.Config)
	if err != nil || !bytes.Equal(old, config) || e.active != active {
		t.Fatalf("failed to restore: %v", err)
	}
	routes, err := r.List()
	if err != nil || len(routes) != 1 || routes[0].Slug != "one" {
		t.Fatalf("registry: %v %v", routes, err)
	}
}

func TestRunReportsCompensationFailure(t *testing.T) {
	d, e, _ := fixture(t)
	e.fail = "start"
	e.stopError = true
	if code, _, err := invoke(d, "up", "--port", "3000"); code == 0 || !strings.Contains(err, "startup failed") || !strings.Contains(err, "compensation failed") || !strings.Contains(err, "cannot remove") {
		t.Fatalf("missing failure details: %d %q", code, err)
	}
}

func TestRunStatusAndMissingDownWithoutEngine(t *testing.T) {
	d, _, _ := fixture(t)
	d.Engine = func(context.Context) (LifecycleEngine, error) { return nil, errors.New("engine should not be called") }
	if code, _, err := invoke(d, "status"); code != 0 {
		t.Fatalf("status: %s", err)
	}
	if code, _, err := invoke(d, "down", "--slug", "missing"); code != 0 {
		t.Fatalf("down: %s", err)
	}
	if _, err := os.Stat(d.Paths.Registry); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing down wrote registry: %v", err)
	}
}
