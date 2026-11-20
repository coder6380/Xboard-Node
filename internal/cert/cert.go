package cert

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/caddyserver/certmagic"
	"golang.org/x/net/idna"

	"github.com/cedar2025/xboard-node/internal/cert/dnsproviders"
	"github.com/cedar2025/xboard-node/internal/config"
	"github.com/cedar2025/xboard-node/internal/kernel"
	"github.com/cedar2025/xboard-node/internal/nlog"
)

const sharedStorageSyncInterval = 30 * time.Second

// Manager handles TLS certificate lifecycle.
//
// Supported modes (CertConfig.CertMode):
//   - "http"    — ACME HTTP-01 challenge via certmagic (needs port 80).
//   - "dns"     — ACME DNS-01 challenge via certmagic + libdns provider.
//   - "self"    — Self-signed certificate generated in memory.
//   - "file"    — User-provided certificate and key file paths (CertFile/KeyFile).
//   - "content" — Certificate and key PEM content pushed from the panel.
//   - "none"    — No TLS. The node will handle plain connections.
//
// All modes normalise to in-memory PEM. Kernels never see file paths —
// they always receive PEM bytes via TLSCert().
//
// Priority Logic (when CertMode is empty):
//
//  1. "http" (if auto_tls is true)
//  2. "content" (if both CertContent and KeyContent are provided)
//  3. "file" (if both CertFile and KeyFile paths are provided)
//  4. "none" (default)
type Manager struct {
	mu  sync.Mutex
	cfg config.CertConfig

	// Atomic so the ACME renewal goroutine can swap without racing readers.
	mat atomic.Pointer[certMaterial]

	magic           *certmagic.Config
	acmeCache       *certmagic.Cache
	acmeStorageRef  certmagic.Storage
	acmeIssuerKey   string
	acmeDomain      string
	acmeGen         uint64
	lastStorageSync time.Time

	renewed         atomic.Bool
	acmeStarted     bool
	acmeFingerprint string
	acmeCancel      context.CancelFunc
}

// certMaterial is an immutable snapshot of PEM-encoded cert + key.
type certMaterial struct {
	certPEM []byte
	keyPEM  []byte
}

// NewManager creates a certificate manager.
func NewManager(cfg config.CertConfig) *Manager {
	return &Manager{cfg: cfg}
}

// storePEM atomically swaps the in-memory cert material and persists to m.cfg.CertDir.
func (m *Manager) storePEM(cert, key []byte) {
	m.mu.Lock()
	dir := m.cfg.CertDir
	m.mu.Unlock()
	m.storePEMTo(dir, cert, key)
}

// storePEMTo atomically swaps the in-memory cert material and persists to certDir
// so that restarts can reload without re-generating or re-requesting.
func (m *Manager) storePEMTo(certDir string, cert, key []byte) {
	m.mat.Store(&certMaterial{certPEM: cert, keyPEM: key})
	persistPEMTo(certDir, cert, key)
}

// persistPEMTo writes cert/key to cert_dir for restart recovery.
// Errors are logged but not returned — persistence is best-effort.
func persistPEMTo(dir string, cert, key []byte) {
	if dir == "" {
		return
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		nlog.Core().Warn("cert: failed to create cert_dir", "dir", dir, "error", err)
		return
	}
	certPath := filepath.Join(dir, "cert.pem")
	keyPath := filepath.Join(dir, "key.pem")
	if err := atomicWriteFile(certPath, cert, 0o644); err != nil {
		nlog.Core().Warn("cert: failed to persist cert", "path", certPath, "error", err)
	}
	if err := atomicWriteFile(keyPath, key, 0o600); err != nil {
		nlog.Core().Warn("cert: failed to persist key", "path", keyPath, "error", err)
	}
}

// atomicWriteFile writes data to a unique temp file and renames it into place,
// preventing partial reads and concurrent temp-file collisions.
func atomicWriteFile(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, ".cert-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := f.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpPath)
		}
	}()
	if err := f.Chmod(perm); err != nil {
		_ = f.Close()
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	cleanup = false
	return nil
}

// loadPersistedPEM loads previously persisted cert material from cert_dir.
// Returns true if valid material was loaded.
func (m *Manager) loadPersistedPEM(dir string) bool {
	if dir == "" {
		return false
	}
	certPEM, err := os.ReadFile(filepath.Join(dir, "cert.pem"))
	if err != nil {
		return false
	}
	keyPEM, err := os.ReadFile(filepath.Join(dir, "key.pem"))
	if err != nil {
		return false
	}
	if err := validateKeyPair(certPEM, keyPEM); err != nil {
		nlog.Core().Warn("cert: persisted cert invalid, will regenerate", "error", err)
		return false
	}
	// Store in memory only (don't re-persist what we just read).
	m.mat.Store(&certMaterial{certPEM: certPEM, keyPEM: keyPEM})
	nlog.Core().Info("cert: loaded persisted certificate from disk", "dir", dir)
	return true
}

// loadReusableSelfSignedPEM loads persisted cert material only when it is an
// unexpired self-signed certificate matching domain.
func (m *Manager) loadReusableSelfSignedPEM(dir, domain string) bool {
	if dir == "" {
		return false
	}
	certPEM, err := os.ReadFile(filepath.Join(dir, "cert.pem"))
	if err != nil {
		return false
	}
	keyPEM, err := os.ReadFile(filepath.Join(dir, "key.pem"))
	if err != nil {
		return false
	}
	if !isReusableSelfSignedPEM(certPEM, keyPEM, domain, time.Now()) {
		return false
	}
	m.mat.Store(&certMaterial{certPEM: certPEM, keyPEM: keyPEM})
	nlog.Core().Info("cert: loaded persisted self-signed certificate from disk", "dir", dir, "domain", domain)
	return true
}

func isReusableSelfSignedPEM(certPEM, keyPEM []byte, domain string, now time.Time) bool {
	tlsCert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil || len(tlsCert.Certificate) == 0 {
		return false
	}
	leaf, err := x509.ParseCertificate(tlsCert.Certificate[0])
	if err != nil {
		return false
	}
	// Require at least 24h validity remaining and already active.
	if now.Before(leaf.NotBefore) || !now.Before(leaf.NotAfter.Add(-24*time.Hour)) {
		return false
	}
	// Must be self-signed (not an ACME/CA cert left over from another mode).
	if err := leaf.CheckSignatureFrom(leaf); err != nil {
		return false
	}
	// Must match the requested domain / IP.
	if err := leaf.VerifyHostname(domain); err != nil {
		return false
	}
	return true
}

// Reconfigure applies a new cert configuration at runtime (e.g. from panel push).
// Returns true when the PEM material changed (caller should restart kernel).
func (m *Manager) Reconfigure(ctx context.Context, newCfg config.CertConfig) (bool, error) {
	m.mu.Lock()
	if newCfg.CertDir == "" {
		newCfg.CertDir = m.cfg.CertDir
	}
	if newCfg.ACMEStorageDir == "" {
		newCfg.ACMEStorageDir = m.cfg.ACMEStorageDir
	}
	if newCfg.HTTPPort == 0 {
		newCfg.HTTPPort = m.cfg.HTTPPort
	}

	// Snapshot current PEM and config BEFORE tearing down ACME, otherwise
	// switching to "none" or failing Start loses rollback state.
	oldMat := m.mat.Load()
	oldTLS := m.TLSCert()
	prevCfg := m.cfg

	needTearDown := false
	if m.acmeStarted {
		newFp := acmeFingerprint(newCfg)
		newMode := resolveModeFor(newCfg)
		if (newMode != "http" && newMode != "dns") || newFp != m.acmeFingerprint {
			needTearDown = true
		}
	}
	m.mu.Unlock()

	if needTearDown {
		m.tearDownACME()
	}

	m.mu.Lock()
	m.cfg = newCfg
	m.mu.Unlock()

	if err := m.Start(ctx); err != nil {
		m.mu.Lock()
		m.cfg = prevCfg
		m.mu.Unlock()
		m.mat.Store(oldMat)
		return false, fmt.Errorf("cert reconfigure: %w", err)
	}

	newTLS := m.TLSCert()
	return !pemEqual(oldTLS, newTLS), nil
}

// tearDownACME cancels the running certmagic background goroutines and stops
// the certmagic cache without wiping active in-memory cert material.
func (m *Manager) tearDownACME() {
	m.mu.Lock()
	m.acmeGen++
	cancel := m.acmeCancel
	cache := m.acmeCache
	m.acmeCancel = nil
	m.acmeCache = nil
	m.magic = nil
	m.acmeStorageRef = nil
	m.acmeIssuerKey = ""
	m.acmeDomain = ""
	m.acmeStarted = false
	m.acmeFingerprint = ""
	m.lastStorageSync = time.Time{}
	m.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if cache != nil {
		cache.Stop()
	}
}

// acmeFingerprint produces a stable string capturing every config field that
// requires re-running ACME when changed.
func acmeFingerprint(cfg config.CertConfig) string {
	keys := make([]string, 0, len(cfg.DNSEnv))
	for k := range cfg.DNSEnv {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	httpPort := cfg.HTTPPort
	if httpPort == 0 {
		httpPort = 80
	}
	var b strings.Builder
	b.WriteString(resolveModeFor(cfg))
	b.WriteByte('|')
	b.WriteString(strings.TrimSpace(cfg.Domain))
	b.WriteByte('|')
	b.WriteString(strings.TrimSpace(cfg.Email))
	b.WriteByte('|')
	b.WriteString(strings.TrimSpace(cfg.DNSProvider))
	b.WriteByte('|')
	b.WriteString(strconv.Itoa(httpPort))
	b.WriteByte('|')
	b.WriteString(cfg.ACMEStorageDir)
	b.WriteByte('|')
	b.WriteString(cfg.CertDir)
	b.WriteByte('|')
	for _, k := range keys {
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(cfg.DNSEnv[k])
		b.WriteByte(';')
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}

// resolveModeFor mirrors (*Manager).resolveMode() for an arbitrary cfg, used
// by Reconfigure to decide tear-down without mutating manager state.
func resolveModeFor(cfg config.CertConfig) string {
	mode := strings.ToLower(strings.TrimSpace(cfg.CertMode))
	if mode != "" {
		return mode
	}
	if cfg.AutoTLS {
		return "http"
	}
	if cfg.CertContent != "" && cfg.KeyContent != "" {
		return "content"
	}
	if cfg.CertFile != "" && cfg.KeyFile != "" {
		return "file"
	}
	return "none"
}

// resolveMode returns the effective cert mode, handling backward compat for auto_tls.
func (m *Manager) resolveMode() string {
	m.mu.Lock()
	cfg := m.cfg
	m.mu.Unlock()
	return resolveModeFor(cfg)
}

func (m *Manager) HasCert() bool {
	mat := m.mat.Load()
	return mat != nil && len(mat.certPEM) > 0 && len(mat.keyPEM) > 0
}

// CertRenewed reports whether the certificate was renewed since the last check.
// When sharing ACMEStorageDir across multiple nodes, it also periodically checks
// storage so peer nodes pick up renewals completed by another node.
func (m *Manager) CertRenewed() bool {
	if m.renewed.Swap(false) {
		return true
	}
	if m.syncSharedACMEStorage(context.Background(), false) {
		m.renewed.Store(false)
		return true
	}
	return false
}

// syncSharedACMEStorage checks whether ACME storage contains newer cert/key
// bytes than the currently loaded material (e.g. renewed by another node sharing
// ACMEStorageDir). When force is false, checks are throttled.
func (m *Manager) syncSharedACMEStorage(ctx context.Context, force bool) bool {
	m.mu.Lock()
	if !m.acmeStarted || m.acmeStorageRef == nil || m.acmeIssuerKey == "" || m.acmeDomain == "" {
		m.mu.Unlock()
		return false
	}
	if !force && !m.lastStorageSync.IsZero() && time.Since(m.lastStorageSync) < sharedStorageSyncInterval {
		m.mu.Unlock()
		return false
	}
	m.lastStorageSync = time.Now()
	storage := m.acmeStorageRef
	issuerKey := m.acmeIssuerKey
	domain := m.acmeDomain
	certDir := m.cfg.CertDir
	gen := m.acmeGen
	m.mu.Unlock()

	certPEM, keyPEM, err := readPEMFromStorage(ctx, storage, issuerKey, domain)
	if err != nil {
		return false
	}
	cur := m.mat.Load()
	if cur != nil && bytes.Equal(cur.certPEM, certPEM) && bytes.Equal(cur.keyPEM, keyPEM) {
		return false
	}

	m.mu.Lock()
	if m.acmeGen != gen {
		m.mu.Unlock()
		return false
	}
	m.mu.Unlock()

	m.storePEMTo(certDir, certPEM, keyPEM)
	nlog.Core().Info("TLS certificate reloaded from shared ACME storage", "domain", domain)
	return true
}

// TLSCert returns the current PEM material as a kernel.TLSCert.
func (m *Manager) TLSCert() kernel.TLSCert {
	mat := m.mat.Load()
	if mat == nil {
		return kernel.TLSCert{}
	}
	return kernel.TLSCert{CertPEM: mat.certPEM, KeyPEM: mat.keyPEM}
}

// pemEqual reports whether two TLSCert values carry identical PEM content.
func pemEqual(a, b kernel.TLSCert) bool {
	return string(a.CertPEM) == string(b.CertPEM) && string(a.KeyPEM) == string(b.KeyPEM)
}

// Start initializes the cert manager based on the resolved mode.
func (m *Manager) Start(ctx context.Context) error {
	m.mu.Lock()
	cfg := m.cfg
	m.mu.Unlock()

	mode := resolveModeFor(cfg)

	switch mode {
	case "none", "":
		m.mat.Store(nil)
		return nil
	case "file":
		return m.startFile(cfg)
	case "self":
		return m.startSelfSigned(cfg)
	case "content":
		return m.startContent(cfg)
	case "http":
		return m.startACME(ctx, cfg, nil)
	case "dns":
		solver, err := buildDNSSolver(cfg)
		if err != nil {
			return fmt.Errorf("build dns solver: %w", err)
		}
		return m.startACME(ctx, cfg, solver)
	default:
		return fmt.Errorf("unknown cert_mode: %q (supported: http, dns, self, file, content, none)", mode)
	}
}

// Stop cancels any active ACME background maintenance and stops the certmagic cache.
func (m *Manager) Stop() {
	m.tearDownACME()
}

// ─── Mode: file ────────────────────────────────────────────────────────────

func (m *Manager) startFile(cfg config.CertConfig) error {
	certPEM, err := os.ReadFile(cfg.CertFile)
	if err != nil {
		return fmt.Errorf("cert file: %w", err)
	}
	keyPEM, err := os.ReadFile(cfg.KeyFile)
	if err != nil {
		return fmt.Errorf("key file: %w", err)
	}
	if err := validateKeyPair(certPEM, keyPEM); err != nil {
		return fmt.Errorf("invalid certificate pair: %w", err)
	}
	m.storePEMTo(cfg.CertDir, certPEM, keyPEM)
	nlog.Core().Debug("TLS certificate loaded from files", "cert", cfg.CertFile, "key", cfg.KeyFile)
	return nil
}

// ─── Mode: self ────────────────────────────────────────────────────────────

func (m *Manager) startSelfSigned(cfg config.CertConfig) error {
	domain := strings.TrimSpace(cfg.Domain)
	if domain == "" {
		domain = "localhost"
	}

	// Reuse persisted self-signed cert only if unexpired, self-signed, and matching domain.
	if m.loadReusableSelfSignedPEM(cfg.CertDir, domain) {
		return nil
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return fmt.Errorf("generate key: %w", err)
	}

	serialNumber, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return fmt.Errorf("generate serial: %w", err)
	}

	template := &x509.Certificate{
		SerialNumber: serialNumber,
		Subject:      pkix.Name{CommonName: domain},
		NotBefore:    time.Now().Add(-1 * time.Hour),
		NotAfter:     time.Now().Add(10 * 365 * 24 * time.Hour), // 10 years
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}

	// Add SANs
	if ip := net.ParseIP(domain); ip != nil {
		template.IPAddresses = []net.IP{ip}
	} else {
		template.DNSNames = []string{domain}
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return fmt.Errorf("create certificate: %w", err)
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return fmt.Errorf("marshal key: %w", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

	m.storePEMTo(cfg.CertDir, certPEM, keyPEM)
	nlog.Core().Info("self-signed certificate generated (in-memory)", "domain", domain, "valid_years", 10)
	return nil
}

// ─── Mode: content ────────────────────────────────────────────────────────

// startContent stores panel-supplied PEM strings in memory for inline use.
func (m *Manager) startContent(cfg config.CertConfig) error {
	if cfg.CertContent == "" || cfg.KeyContent == "" {
		// No fresh content from panel — try previously persisted material.
		if m.loadPersistedPEM(cfg.CertDir) {
			return nil
		}
		return fmt.Errorf("cert_mode 'content' requires both cert_content and key_content")
	}
	certPEM := []byte(cfg.CertContent)
	keyPEM := []byte(cfg.KeyContent)
	if err := validateKeyPair(certPEM, keyPEM); err != nil {
		return fmt.Errorf("invalid certificate content: %w", err)
	}
	m.storePEMTo(cfg.CertDir, certPEM, keyPEM)
	nlog.Core().Info("TLS certificate loaded from panel content (in-memory)")
	return nil
}

// ─── Mode: http / dns (ACME) ──────────────────────────────────────────────

// acmeStorageFor 将 ACME 状态与节点各自持久化的 PEM 分开。
func acmeStorageFor(cfg config.CertConfig) *certmagic.FileStorage {
	dir := cfg.ACMEStorageDir
	if dir == "" {
		dir = cfg.CertDir
	}
	return &certmagic.FileStorage{Path: dir}
}

// acmeStorage retains package-internal compatibility for tests.
func (m *Manager) acmeStorage() *certmagic.FileStorage {
	m.mu.Lock()
	cfg := m.cfg
	m.mu.Unlock()
	return acmeStorageFor(cfg)
}

func (m *Manager) startACME(ctx context.Context, cfg config.CertConfig, dnsSolver *certmagic.DNS01Solver) error {
	m.mu.Lock()
	// Idempotent for unchanged config: Reconfigure already tore down stale ACME state.
	if m.acmeStarted {
		m.mu.Unlock()
		return nil
	}
	m.acmeGen++
	gen := m.acmeGen
	m.mu.Unlock()

	domain := strings.TrimSpace(cfg.Domain)
	if domain == "" {
		return fmt.Errorf("cert.domain is required for ACME modes (http/dns)")
	}

	if cfg.CertDir != "" {
		if err := os.MkdirAll(cfg.CertDir, 0o755); err != nil {
			return fmt.Errorf("create cert dir: %w", err)
		}
	}

	storage := acmeStorageFor(cfg)
	if storage.Path != "" {
		if err := os.MkdirAll(storage.Path, 0o755); err != nil {
			return fmt.Errorf("create acme storage dir: %w", err)
		}
	}

	// Derived ctx so Reconfigure/Stop can cancel ACME background goroutines.
	// Cap initial obtainCtx at 5m as a safety net in case a pre-issuer error
	// returns without emitting "cert_failed".
	acmeCtx, acmeCancel := context.WithCancel(ctx)
	obtainCtx, obtainCancel := context.WithTimeout(acmeCtx, 5*time.Minute)
	var initialErrMu sync.Mutex
	var initialErr error

	var magic *certmagic.Config
	cache := certmagic.NewCache(certmagic.CacheOptions{
		GetConfigForCert: func(_ certmagic.Certificate) (*certmagic.Config, error) {
			return magic, nil
		},
	})
	cleanup := func() {
		obtainCancel()
		acmeCancel()
		cache.Stop()
	}

	certDir := cfg.CertDir
	magic = certmagic.New(cache, certmagic.Config{
		Storage: storage,
		OnEvent: func(evtCtx context.Context, event string, data map[string]any) error {
			if event == "cert_failed" {
				if renewed, _ := data["renewal"].(bool); !renewed {
					if e, ok := data["error"].(error); ok && e != nil {
						initialErrMu.Lock()
						if initialErr == nil {
							initialErr = e
						}
						initialErrMu.Unlock()
					}
					// Abort doWithRetry after the first failed attempt so initial
					// startup fails fast instead of hanging in backoff.
					obtainCancel()
				}
				return nil
			}
			// Only react to renewals; initial load happens explicitly after obtain.
			if event != "cert_obtained" {
				return nil
			}
			if renewed, _ := data["renewal"].(bool); !renewed {
				return nil
			}
			issuerKey, _ := data["issuer"].(string)
			if issuerKey == "" {
				return nil
			}
			m.mu.Lock()
			active := m.acmeGen == gen
			m.mu.Unlock()
			if !active {
				return nil
			}
			if err := m.loadPEMFromStorageWithDir(evtCtx, storage, issuerKey, domain, certDir, gen); err != nil {
				nlog.Core().Error("failed to reload cert after renewal", "error", err)
				return nil
			}
			m.renewed.Store(true)
			nlog.Core().Info("TLS certificate reloaded after renewal", "domain", domain)
			return nil
		},
	})

	issuer := certmagic.ACMEIssuer{
		CA:     certmagic.LetsEncryptProductionCA,
		Email:  strings.TrimSpace(cfg.Email),
		Agreed: true,
	}

	if dnsSolver != nil {
		// DNS-01 mode: no HTTP port needed, supports wildcards.
		issuer.DNS01Solver = dnsSolver
		issuer.DisableHTTPChallenge = true
		issuer.DisableTLSALPNChallenge = true
	} else {
		// HTTP-01 mode.
		httpPort := cfg.HTTPPort
		if httpPort == 0 {
			httpPort = 80
		}
		issuer.AltHTTPPort = httpPort
		issuer.DisableTLSALPNChallenge = true
	}

	magic.Issuers = []certmagic.Issuer{
		certmagic.NewACMEIssuer(magic, issuer),
	}

	// Use non-interactive ObtainCertAsync so empty Email never blocks on os.Stdin
	// or mutates global certmagic.DefaultACME.Agreed; cert_failed cancels obtainCtx
	// immediately after the first failed attempt.
	if err := magic.ObtainCertAsync(obtainCtx, domain); err != nil {
		initialErrMu.Lock()
		cause := initialErr
		initialErrMu.Unlock()
		cleanup()
		if cause != nil {
			return fmt.Errorf("obtain certificate: %w", cause)
		}
		return fmt.Errorf("obtain certificate: %w", err)
	}
	obtainCancel()

	issuerKey := magic.Issuers[0].IssuerKey()
	if err := m.loadPEMFromStorageWithDir(acmeCtx, storage, issuerKey, domain, certDir, gen); err != nil {
		cleanup()
		return fmt.Errorf("load cert from storage: %w", err)
	}

	if err := magic.ManageAsync(acmeCtx, []string{domain}); err != nil {
		cleanup()
		return fmt.Errorf("start cert manager: %w", err)
	}

	m.mu.Lock()
	if m.acmeGen != gen {
		m.mu.Unlock()
		cleanup()
		return context.Canceled
	}
	m.magic = magic
	m.acmeCache = cache
	m.acmeStorageRef = storage
	m.acmeIssuerKey = issuerKey
	m.acmeDomain = domain
	m.acmeCancel = acmeCancel
	m.acmeFingerprint = acmeFingerprint(cfg)
	m.acmeStarted = true
	m.lastStorageSync = time.Now()
	m.mu.Unlock()
	return nil
}

func validateKeyPair(certPEM, keyPEM []byte) error {
	if _, err := tls.X509KeyPair(certPEM, keyPEM); err != nil {
		return err
	}
	return nil
}

// ─── DNS Provider Factory ──────────────────────────────────────────────────

// buildDNSSolver builds a certmagic DNS01Solver from the configured provider.
func buildDNSSolver(cfg config.CertConfig) (*certmagic.DNS01Solver, error) {
	provider, err := newDNSProviderFor(cfg)
	if err != nil {
		return nil, err
	}
	return &certmagic.DNS01Solver{DNSManager: certmagic.DNSManager{DNSProvider: provider}}, nil
}

func (m *Manager) newDNSProvider() (certmagic.DNSProvider, error) {
	m.mu.Lock()
	cfg := m.cfg
	m.mu.Unlock()
	return newDNSProviderFor(cfg)
}

func newDNSProviderFor(cfg config.CertConfig) (certmagic.DNSProvider, error) {
	name := strings.TrimSpace(cfg.DNSProvider)
	if name == "" {
		return nil, fmt.Errorf("dns_provider is required for cert_mode=dns")
	}
	env := cfg.DNSEnv
	if env == nil {
		env = map[string]string{}
	}
	p, ok := dnsproviders.Get(name)
	if !ok {
		return nil, fmt.Errorf("unsupported dns_provider: %q (supported: %s)",
			name, strings.Join(dnsproviders.CanonicalNames(), ", "))
	}
	return p.Build(env)
}

// ─── Helpers ───────────────────────────────────────────────────────────────

func normalizeACMEDomain(domain string) string {
	d := strings.TrimSpace(domain)
	if d == "" {
		return ""
	}
	prefix := ""
	rest := d
	if strings.HasPrefix(rest, "*.") {
		prefix = "*."
		rest = rest[2:]
	}
	if ascii, err := idna.ToASCII(rest); err == nil && ascii != "" {
		return prefix + strings.ToLower(ascii)
	}
	return strings.ToLower(d)
}

func readPEMFromStorage(ctx context.Context, storage certmagic.Storage, issuerKey, domain string) ([]byte, []byte, error) {
	if issuerKey == "" || strings.TrimSpace(domain) == "" {
		return nil, nil, fmt.Errorf("loadPEMFromStorage: issuerKey and domain are required")
	}
	normDomain := normalizeACMEDomain(domain)
	certKey := certmagic.StorageKeys.SiteCert(issuerKey, normDomain)
	keyKey := certmagic.StorageKeys.SitePrivateKey(issuerKey, normDomain)

	certPEM, err := storage.Load(ctx, certKey)
	if err != nil {
		return nil, nil, fmt.Errorf("load cert %q: %w", certKey, err)
	}
	keyPEM, err := storage.Load(ctx, keyKey)
	if err != nil {
		return nil, nil, fmt.Errorf("load key %q: %w", keyKey, err)
	}
	if err := validateKeyPair(certPEM, keyPEM); err != nil {
		return nil, nil, fmt.Errorf("invalid cert/key pair on disk: %w", err)
	}
	return certPEM, keyPEM, nil
}

// loadPEMFromStorage reads cert + key for domain from certmagic storage into
// the in-memory cache using m.cfg.CertDir.
func (m *Manager) loadPEMFromStorage(ctx context.Context, storage certmagic.Storage, issuerKey, domain string) error {
	m.mu.Lock()
	certDir := m.cfg.CertDir
	m.mu.Unlock()
	return m.loadPEMFromStorageWithDir(ctx, storage, issuerKey, domain, certDir, 0)
}

func (m *Manager) loadPEMFromStorageWithDir(ctx context.Context, storage certmagic.Storage, issuerKey, domain, certDir string, gen uint64) error {
	certPEM, keyPEM, err := readPEMFromStorage(ctx, storage, issuerKey, domain)
	if err != nil {
		return err
	}
	m.mu.Lock()
	active := gen == 0 || m.acmeGen == gen
	m.mu.Unlock()
	if !active {
		return nil
	}
	m.storePEMTo(certDir, certPEM, keyPEM)
	return nil
}
