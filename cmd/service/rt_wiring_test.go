// Phase 13 (13-06): TLS + /tutor/ + /rt wiring tests for run(). These boot
// the REAL service binary path (run closure) against a self-authored
// temporary config yaml — the repo has no existing temp-yaml precedent, so
// the skeleton below is the reference shape: server.addr + server.staticDir
// + database.path + the three hard-required python keys (interpreter,
// scriptsDir, dataDir — Validate demands them and DefaultConfig supplies no
// values; vocabScript/pregenScript fall back to defaults).
//
// The baseline test (M-4) proves the yaml skeleton itself boots BEFORE the
// TLS tests assert anything, so "missing python section returns 1" can
// never masquerade as "missing certificate returns 1".
package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TLS test listener addresses — fixed high ports per the plan; if either is
// occupied by the environment the affected test skips (environment problem,
// not a code failure).
const (
	tlsTestAddr  = "127.0.0.1:18443"
	httpTestAddr = "127.0.0.1:18444"
)

// writeRTWiringYaml writes the shared config skeleton. tlsBlock is inserted
// verbatim inside the server section when non-empty. staticDir/scriptsDir
// are computed as absolute paths from the test CWD (go test runs with CWD =
// cmd/service, two levels below the repo root) — a literal relative path is
// fragile against future package moves, so the yaml carries absolute
// forward-slash paths.
func writeRTWiringYaml(t *testing.T, addr, dbPath string, tlsBlock string) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("os.Getwd: %v", err)
	}
	root := filepath.Dir(filepath.Dir(wd)) // cmd/service -> cmd -> repo root
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	content := fmt.Sprintf(`server:
  addr: %s
  staticDir: %s
%sdatabase:
  path: %s
python:
  interpreter: python3
  scriptsDir: %s
  dataDir: %s
`,
		addr,
		filepath.ToSlash(filepath.Join(root, "pkg", "web", "static")),
		tlsYamlServerSection(tlsBlock),
		filepath.ToSlash(dbPath),
		filepath.ToSlash(filepath.Join(root, "third_party", "python")),
		filepath.ToSlash(dir))
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile(%s): %v", path, err)
	}
	return path
}

// tlsYamlServerSection renders the tls block (with two-space indentation)
// or an empty string when TLS is not under test.
func tlsYamlServerSection(tlsBlock string) string {
	if tlsBlock == "" {
		return ""
	}
	return "  tls:\n" + tlsBlock
}

// requireFreePort skips the test when addr cannot be briefly bound (fixed
// high ports can be taken by another environment process — an environment
// issue, not a code failure).
func requireFreePort(t *testing.T, addr string) {
	t.Helper()
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		t.Skipf("test port %s unavailable in this environment: %v", addr, err)
	}
	_ = ln.Close()
}

// generateSelfSignedCert creates a throwaway ECDSA P-256 certificate + key
// pair (SAN 127.0.0.1) in dir and returns their paths. Test-only — the
// production certificate is the owner's existing asset and never enters
// git or .planning (T-13-06).
func generateSelfSignedCert(t *testing.T, dir string) (certPath, keyPath string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("ecdsa.GenerateKey: %v", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		t.Fatalf("serial: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "127.0.0.1"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("x509.CreateCertificate: %v", err)
	}
	certPath = filepath.Join(dir, "cert.pem")
	keyPath = filepath.Join(dir, "key.pem")
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatalf("write cert: %v", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	return certPath, keyPath
}

// insecureClient trusts the throwaway self-signed certificate.
func insecureClient() *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
		Timeout: 5 * time.Second,
	}
}

// waitForStatus polls url until it answers with ANY HTTP response whose
// status satisfies want, returning the final response. Returns nil on
// timeout.
func waitForStatus(t *testing.T, client *http.Client, url string, timeout time.Duration, want func(int) bool) *http.Response {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := client.Get(url)
		if err == nil {
			if want(resp.StatusCode) {
				return resp
			}
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
		}
		time.Sleep(50 * time.Millisecond)
	}
	return nil
}

// TestRunNoTLSHTTPBaseline is the M-4 control: the SAME yaml skeleton as
// the TLS tests but with NO server.tls section boots and serves plain HTTP.
// It isolates skeleton problems (a missing python key returns 1) from the
// TLS assertions so TestRunTLSMissingCertFails's exit code can only mean
// "certificate missing".
func TestRunNoTLSHTTPBaseline(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test, skipped in -short mode")
	}
	runMu.Lock()
	defer runMu.Unlock()
	clearHTAEnv(t)
	requireFreePort(t, httpTestAddr)

	dbPath := filepath.Join(t.TempDir(), "baseline.db")
	cfgPath := writeRTWiringYaml(t, httpTestAddr, dbPath, "")
	healthAddr, err := freePortAddr()
	if err != nil {
		t.Fatalf("freePortAddr: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan int, 1)
	go func() { done <- run(ctx, cfgPath, healthAddr) }()
	defer func() {
		cancel()
		<-done
	}()

	client := &http.Client{Timeout: 5 * time.Second}
	resp := waitForStatus(t, client, "http://"+httpTestAddr+"/", 10*time.Second, func(c int) bool { return c == http.StatusOK })
	if resp == nil {
		t.Fatal("HTTP root never responded 200 — yaml skeleton failed to boot (control test for the TLS cases)")
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
}

// TestRunTLSHTTPSServesTutorAndRT proves the RT-01 server half: with
// server.tls configured the public listener serves HTTPS, /tutor/ answers
// 200 carrying data-page="tutor", /rt is mounted (a plain GET without
// upgrade headers gets 400/403 — never 404), and the legacy student page at
// "/" keeps working.
func TestRunTLSHTTPSServesTutorAndRT(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test, skipped in -short mode")
	}
	runMu.Lock()
	defer runMu.Unlock()
	clearHTAEnv(t)
	requireFreePort(t, tlsTestAddr)

	certDir := t.TempDir()
	certPath, keyPath := generateSelfSignedCert(t, certDir)
	dbPath := filepath.Join(t.TempDir(), "tls.db")
	tlsBlock := fmt.Sprintf("    certFile: %s\n    keyFile: %s\n",
		filepath.ToSlash(certPath), filepath.ToSlash(keyPath))
	cfgPath := writeRTWiringYaml(t, tlsTestAddr, dbPath, tlsBlock)
	healthAddr, err := freePortAddr()
	if err != nil {
		t.Fatalf("freePortAddr: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan int, 1)
	go func() { done <- run(ctx, cfgPath, healthAddr) }()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(15 * time.Second):
			t.Error("run did not return within 15s of ctx cancel")
		}
	}()

	client := insecureClient()
	base := "https://" + tlsTestAddr

	// /tutor/ serves the natural-voice tutor page (13-05) over TLS.
	resp := waitForStatus(t, client, base+"/tutor/", 10*time.Second, func(c int) bool { return c == http.StatusOK })
	if resp == nil {
		t.Fatal("https /tutor/ never responded 200")
	}
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		t.Fatalf("read /tutor/ body: %v", err)
	}
	if !strings.Contains(string(body), `data-page="tutor"`) {
		t.Errorf("/tutor/ body missing data-page=\"tutor\" marker (got %d bytes)", len(body))
	}

	// /rt is mounted: a plain GET without upgrade headers or Origin gets
	// gorilla's upgrade rejection (400) or origin rejection (403) — 404
	// would mean the route never got wired.
	rtResp := waitForStatus(t, client, base+"/rt", 10*time.Second, func(c int) bool { return c != http.StatusNotFound })
	if rtResp == nil {
		t.Fatal("https /rt never answered (route not mounted?)")
	}
	if rtResp.StatusCode != http.StatusBadRequest && rtResp.StatusCode != http.StatusForbidden {
		t.Errorf("/rt plain GET status = %d, want 400 or 403 (mounted, upgrade rejected)", rtResp.StatusCode)
	}
	_, _ = io.Copy(io.Discard, rtResp.Body)
	_ = rtResp.Body.Close()

	// The legacy student page at "/" still serves (RT-10 fallback entrance).
	rootResp := waitForStatus(t, client, base+"/", 10*time.Second, func(c int) bool { return c == http.StatusOK })
	if rootResp == nil {
		t.Fatal("https / never responded 200 (legacy student page)")
	}
	_, _ = io.Copy(io.Discard, rootResp.Body)
	_ = rootResp.Body.Close()
}

// TestRunTLSMissingCertFails proves the trusted entrance never silently
// degrades: certFile pointing at a nonexistent file aborts startup with
// exit code 1. The Baseline test already proved the yaml skeleton boots,
// so the non-zero code here can only come from the missing certificate.
func TestRunTLSMissingCertFails(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test, skipped in -short mode")
	}
	runMu.Lock()
	defer runMu.Unlock()
	clearHTAEnv(t)

	missing := filepath.Join(t.TempDir(), "does-not-exist.pem")
	dbPath := filepath.Join(t.TempDir(), "missing-cert.db")
	// Pair the missing cert with a REAL key file so the failure is
	// specifically the missing cert file at the boot Stat check, not the
	// config pair-validation rule.
	_, realKey := generateSelfSignedCert(t, filepath.Dir(missing))
	tlsBlock := fmt.Sprintf("    certFile: %s\n    keyFile: %s\n",
		filepath.ToSlash(missing), filepath.ToSlash(realKey))
	cfgPath := writeRTWiringYaml(t, tlsTestAddr, dbPath, tlsBlock)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan int, 1)
	go func() { done <- run(ctx, cfgPath, "") }()

	select {
	case code := <-done:
		if code != 1 {
			t.Errorf("run exit code = %d, want 1 (missing cert file must abort startup)", code)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("run did not return within 15s for missing cert file")
	}
}
