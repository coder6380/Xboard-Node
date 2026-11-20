package dnsproviders

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/caddyserver/certmagic"
	"github.com/libdns/googleclouddns"
)

func init() {
	Register(&Provider{
		Names:   []string{"googleclouddns", "gcp", "gcloud"},
		EnvVars: []string{"GCE_PROJECT", "GCE_SERVICE_ACCOUNT_FILE"},
		Build: func(env map[string]string) (certmagic.DNSProvider, error) {
			project := firstOf(env, "GCE_PROJECT", "GOOGLE_PROJECT_ID", "GCP_PROJECT")
			if project == "" {
				return nil, missingEnvErr("googleclouddns", "GCE_PROJECT")
			}
			// libdns/googleclouddns@v1.2.0 expects a file path in ServiceAccountJSON.
			// When operators push raw JSON via panel dns_env, materialize it to a
			// private 0600 file so both file paths and raw JSON blobs work.
			saPath, err := resolveGCPServiceAccountPath(firstOf(env, "GCE_SERVICE_ACCOUNT_FILE", "GOOGLE_APPLICATION_CREDENTIALS"))
			if err != nil {
				return nil, err
			}
			return &googleclouddns.Provider{
				Project:            project,
				ServiceAccountJSON: saPath,
			}, nil
		},
	})
}

func resolveGCPServiceAccountPath(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || !strings.HasPrefix(trimmed, "{") {
		return trimmed, nil
	}
	sum := sha256.Sum256([]byte(trimmed))
	dir := filepath.Join(os.TempDir(), "xboard-node-gcp-creds")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("googleclouddns: create credentials dir: %w", err)
	}
	target := filepath.Join(dir, hex.EncodeToString(sum[:16])+".json")
	if err := os.WriteFile(target, []byte(trimmed), 0o600); err != nil {
		return "", fmt.Errorf("googleclouddns: write service account file: %w", err)
	}
	return target, nil
}
