package dnsproviders

import (
	"os"
	"testing"

	"github.com/libdns/googleclouddns"
)

func TestGoogleCloudDNSInlineJSONMaterialized(t *testing.T) {
	p, ok := Get("gcp")
	if !ok {
		t.Fatal("gcp provider not registered")
	}
	rawJSON := `{"type":"service_account","project_id":"demo-proj"}`
	prov, err := p.Build(map[string]string{
		"GCE_PROJECT":              "demo-proj",
		"GCE_SERVICE_ACCOUNT_FILE": rawJSON,
	})
	if err != nil {
		t.Fatalf("Build gcp inline JSON: %v", err)
	}
	gcpProv, ok := prov.(*googleclouddns.Provider)
	if !ok {
		t.Fatalf("unexpected provider type %T", prov)
	}
	if gcpProv.ServiceAccountJSON == rawJSON {
		t.Fatal("expected inline JSON to be materialized to a file path")
	}
	got, err := os.ReadFile(gcpProv.ServiceAccountJSON)
	if err != nil {
		t.Fatalf("read materialized file %q: %v", gcpProv.ServiceAccountJSON, err)
	}
	if string(got) != rawJSON {
		t.Fatalf("materialized JSON = %q, want %q", string(got), rawJSON)
	}
}
