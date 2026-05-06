package managementasset

import (
	"encoding/json"
	"path/filepath"
	"testing"
)

func TestManagementStaticPathSupportsHTMLOverride(t *testing.T) {
	htmlPath := filepath.Join(t.TempDir(), "index.html")
	t.Setenv("MANAGEMENT_STATIC_PATH", htmlPath)

	if got := FilePath(""); got != htmlPath {
		t.Fatalf("FilePath() = %q, want %q", got, htmlPath)
	}
	if got := StaticDir(""); got != filepath.Dir(htmlPath) {
		t.Fatalf("StaticDir() = %q, want %q", got, filepath.Dir(htmlPath))
	}
}

func TestResolveReleaseURLSupportsGitLabRepository(t *testing.T) {
	got := resolveReleaseURL("https://192.168.19.70/mirrors/Cli-Proxy-API-Management-Center.git")
	want := "https://192.168.19.70/api/v4/projects/mirrors%2FCli-Proxy-API-Management-Center/releases/permalink/latest"
	if got != want {
		t.Fatalf("resolveReleaseURL() = %q, want %q", got, want)
	}
}

func TestReleaseAssetFromResponseSupportsGitLabLinks(t *testing.T) {
	raw := []byte(`{
		"assets": {
			"links": [
				{
					"name": "management.html",
					"url": "https://example.test/releases/download/management.html",
					"direct_asset_url": "https://example.test/-/releases/latest/downloads/management.html"
				}
			]
		}
	}`)

	var release releaseResponse
	if err := json.Unmarshal(raw, &release); err != nil {
		t.Fatalf("failed to unmarshal release response: %v", err)
	}

	asset, remoteHash, err := releaseAssetFromResponse(release)
	if err != nil {
		t.Fatalf("releaseAssetFromResponse() error = %v", err)
	}
	if remoteHash != "" {
		t.Fatalf("remoteHash = %q, want empty", remoteHash)
	}
	if asset == nil {
		t.Fatal("asset is nil")
	}
	if asset.BrowserDownloadURL != "https://example.test/-/releases/latest/downloads/management.html" {
		t.Fatalf("download URL = %q", asset.BrowserDownloadURL)
	}
}
