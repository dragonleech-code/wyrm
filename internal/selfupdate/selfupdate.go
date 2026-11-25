// Package selfupdate implements the machinery behind `wyrm selfupdate`:
// looking up wyrm's GitHub releases, downloading the right platform archive,
// verifying it against the release's published checksums, and replacing the
// running binary in place.
package selfupdate

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
)

const (
	owner = "jskoll"
	repo  = "wyrm"

	apiBase = "https://api.github.com/repos/" + owner + "/" + repo

	// maxReleaseMetadataSize bounds the release API response before JSON decoding.
	maxReleaseMetadataSize = 4 * 1024 * 1024
)

// HTTPDoer is the subset of *http.Client selfupdate needs. Tests substitute
// a fake (or an httptest server's client) to drive the release-fetch and
// download flow without touching the network.
type HTTPDoer interface {
	Do(req *http.Request) (*http.Response, error)
}

// Release is the subset of a GitHub release wyrm's selfupdate cares about:
// its version and the download URL for each asset attached to it.
type Release struct {
	Version string            // release tag with any leading "v" stripped, e.g. "0.6.2"
	Assets  map[string]string // asset file name -> browser_download_url
}

// ghRelease mirrors the fields used from GitHub's release API response.
type ghRelease struct {
	TagName string `json:"tag_name"`
	Assets  []struct {
		Name               string `json:"name"`
		BrowserDownloadURL string `json:"browser_download_url"`
	} `json:"assets"`
}

// Latest fetches the newest published (non-draft, non-prerelease) release.
func Latest(client HTTPDoer) (Release, error) {
	return fetch(client, apiBase+"/releases/latest")
}

// Tag fetches the release for an exact version, accepting it with or
// without a leading "v" — GitHub's tags carry one, wyrm's own -X
// main.version doesn't.
func Tag(client HTTPDoer, version string) (Release, error) {
	return fetch(client, apiBase+"/releases/tags/v"+strings.TrimPrefix(version, "v"))
}

func fetch(client HTTPDoer, url string) (Release, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return Release{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := client.Do(req)
	if err != nil {
		return Release{}, fmt.Errorf("fetching %s: %w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return Release{}, fmt.Errorf("fetching %s: %s", url, resp.Status)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxReleaseMetadataSize+1))
	if err != nil {
		return Release{}, fmt.Errorf("reading release metadata: %w", err)
	}
	if len(data) > maxReleaseMetadataSize {
		return Release{}, fmt.Errorf("release metadata from %s exceeds maximum size of %d bytes", url, maxReleaseMetadataSize)
	}

	var gr ghRelease
	if err := json.Unmarshal(data, &gr); err != nil {
		return Release{}, fmt.Errorf("decoding release: %w", err)
	}
	rel := Release{
		Version: strings.TrimPrefix(gr.TagName, "v"),
		Assets:  make(map[string]string, len(gr.Assets)),
	}
	for _, a := range gr.Assets {
		rel.Assets[a.Name] = a.BrowserDownloadURL
	}
	return rel, nil
}

// AssetName is the archive name goreleaser publishes for a given version,
// OS, and architecture — e.g. "wyrm_0.6.2_linux_amd64.tar.gz" — mirrored
// from .goreleaser.yaml's archives.name_template.
func AssetName(version, goos, goarch string) string {
	return fmt.Sprintf("%s_%s_%s_%s.tar.gz", repo, version, goos, goarch)
}

// MaxDownloadSize is the maximum size (100 MB) permitted for a downloaded release asset.
const MaxDownloadSize = 100 * 1024 * 1024

// Get downloads a URL's full body up to MaxDownloadSize. Release archives are a few MB, small
// enough to hold in memory rather than streaming to a temp file.
func Get(client HTTPDoer, url string) ([]byte, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("downloading %s: %w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("downloading %s: %s", url, resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, MaxDownloadSize+1))
	if err != nil {
		return nil, fmt.Errorf("reading response from %s: %w", url, err)
	}
	if len(data) > MaxDownloadSize {
		return nil, fmt.Errorf("download from %s exceeds maximum size of %d bytes", url, MaxDownloadSize)
	}
	return data, nil
}

// CompareVersions orders two dotted-numeric versions (a leading "v" and any
// "-prerelease"/"+build" suffix are ignored), returning -1, 0, or 1 the way
// strings.Compare does. Missing trailing components compare as zero, so
// "1.2" == "1.2.0".
func CompareVersions(a, b string) int {
	pa, pb := versionParts(a), versionParts(b)
	for i := 0; i < len(pa) || i < len(pb); i++ {
		var x, y int
		if i < len(pa) {
			x = pa[i]
		}
		if i < len(pb) {
			y = pb[i]
		}
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	return 0
}

func versionParts(v string) []int {
	v = strings.TrimPrefix(v, "v")
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v = v[:i]
	}
	fields := strings.Split(v, ".")
	parts := make([]int, len(fields))
	for i, f := range fields {
		n, _ := strconv.Atoi(f) // a non-numeric component (shouldn't occur) just compares as 0
		parts[i] = n
	}
	return parts
}
