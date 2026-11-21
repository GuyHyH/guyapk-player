package android

import (
	"context"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// TestProvisionLive checks the real Google index, confirms every planned URL
// exists, and installs the smallest package. It needs GUYAPK_LIVE_PROVISION=1.
func TestProvisionLive(t *testing.T) {
	if os.Getenv("GUYAPK_LIVE_PROVISION") == "" {
		t.Skip("GUYAPK_LIVE_PROVISION not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	sdk := SDK{Root: t.TempDir()}
	plan, err := BuildPlan(ctx, sdk)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Packages) != 3 || !strings.Contains(plan.License, "Android") {
		t.Fatalf("unexpected plan: %d packages, licence %d bytes", len(plan.Packages), len(plan.License))
	}
	var small Package
	for _, p := range plan.Packages {
		req, _ := http.NewRequestWithContext(ctx, http.MethodHead, p.URL, nil)
		res, err := httpClient.Do(req)
		if err != nil {
			t.Fatalf("%s: %v", p.Path, err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusOK || res.ContentLength != p.Size {
			t.Fatalf("%s: HTTP %d, %d bytes, want %d", p.Path, res.StatusCode, res.ContentLength, p.Size)
		}
		t.Logf("%-46s %7.1f MB", p.Path, float64(p.Size)/1e6)
		if p.Path == "platform-tools" {
			small = p
		}
	}
	t.Logf("total %.2f GB", float64(plan.Total)/1e9)

	one := &Plan{Packages: []Package{small}, Total: small.Size}
	if err := Provision(ctx, sdk, one, func(Progress) {}); err != nil {
		t.Fatal(err)
	}
	out, err := run(ctx, 15*time.Second, sdk.ADB(), "version")
	if err != nil || !strings.Contains(out, "Android Debug Bridge") {
		t.Fatalf("installed adb does not run: %v %s", err, out)
	}
}
