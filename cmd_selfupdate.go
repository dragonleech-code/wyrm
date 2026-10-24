package main

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/jskoll/wyrm/internal/selfupdate"
)

// selfupdate checks GitHub for a wyrm release, and — unless -check is given
// — downloads it, verifies it against the release's published checksums,
// and replaces the running binary in place.
//
// It refuses on a binary that looks package-managed (Homebrew, dpkg, rpm,
// pacman/AUR): those already have their own upgrade path, and silently
// overwriting the file they track would leave their database out of sync
// with what's actually on disk.
func (a *app) selfupdate(args []string) error {
	fs := a.newFlagSet("selfupdate")
	check := fs.Bool("check", false, "report whether an update is available, without installing it")
	pin := fs.String("version", "", "install this version instead of the latest, e.g. 0.6.2")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if err := requireNoArgs(fs); err != nil {
		return err
	}

	client := a.httpClient
	if client == nil {
		client = &http.Client{
			Timeout: 30 * time.Second,
		}
	}

	rel, err := fetchRelease(client, *pin)
	if err != nil {
		return err
	}

	current := versionString()
	// An unstamped "dev"/"dev+<rev>" build has no version to compare
	// against, so it's always treated as eligible for updating.
	versionKnown := !strings.HasPrefix(current, "dev")
	atOrAboveTarget := versionKnown && selfupdate.CompareVersions(current, rel.Version) >= 0

	if *check {
		if atOrAboveTarget {
			_, _ = fmt.Fprintf(a.stdout, "wyrm %s is up to date\n", current)
		} else {
			_, _ = fmt.Fprintf(a.stdout, "wyrm %s is available (currently %s)\n", rel.Version, current)
		}
		return nil
	}

	// Only the default (unpinned) "latest" flow treats "already there" as a
	// no-op; an explicit -version is a request to install exactly that
	// build, even if it means reinstalling or downgrading.
	if *pin == "" && atOrAboveTarget {
		_, _ = fmt.Fprintf(a.stdout, "wyrm %s is already up to date\n", current)
		return nil
	}

	exePath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locating the running binary: %w", err)
	}
	realPath, err := filepath.EvalSymlinks(exePath)
	if err != nil {
		return fmt.Errorf("resolving %s: %w", exePath, err)
	}
	if manager, hint, ok := selfupdate.Managed(realPath); ok {
		return fmt.Errorf("wyrm was installed via %s; run `%s` to update instead", manager, hint)
	}
	info, err := os.Stat(realPath)
	if err != nil {
		return fmt.Errorf("checking %s: %w", realPath, err)
	}

	return a.installRelease(client, rel, realPath, info.Mode(), current)
}

// signatureAssets are the names goreleaser may publish the checksums
// signature under, in preference order.
var signatureAssets = []string{"checksums.txt.minisig", "checksums.txt.sig"}

// verifyChecksumsSignature checks the release's signature over checksums.txt,
// and says plainly what it did.
//
// checksums.txt is fetched from the same place as the archive it vouches for,
// so on its own it proves the download was not corrupted — not that it came
// from the wyrm maintainers. The signature is what separates those two, so
// every outcome — verified, unsigned, no key compiled in — is stated out loud
// rather than quietly falling back to checksums alone.
func (a *app) verifyChecksumsSignature(client selfupdate.HTTPDoer, rel selfupdate.Release, checksums []byte) error {
	key := selfupdate.DefaultSigningKey

	var sigURL string
	for _, name := range signatureAssets {
		if u, ok := rel.Assets[name]; ok {
			sigURL = u
			break
		}
	}

	if sigURL == "" {
		if key.Valid() {
			return fmt.Errorf("release %s has no signature for checksums.txt, but this build requires one", rel.Version)
		}
		_, _ = fmt.Fprintf(a.stderr,
			"wyrm: warning: release %s is not signed; verifying checksums only, "+
				"which detects a corrupted download but not a tampered release\n", rel.Version)
		return nil
	}

	if !key.Valid() {
		// Refusing would strand users on a build that shipped without a key;
		// verifying silently is what got us here. Say it instead.
		_, _ = fmt.Fprintf(a.stderr,
			"wyrm: warning: release %s is signed, but this build has no signing key "+
				"compiled in and cannot check it; verifying checksums only\n", rel.Version)
		return nil
	}

	sig, err := selfupdate.Get(client, sigURL)
	if err != nil {
		return fmt.Errorf("downloading signature: %w", err)
	}
	if err := selfupdate.VerifyChecksumsSignature(checksums, sig, key); err != nil {
		return fmt.Errorf("verifying checksums signature: %w", err)
	}
	_, _ = fmt.Fprintf(a.stderr, "wyrm: verified the release signature for checksums.txt\n")
	return nil
}

// fetchRelease looks up the release selfupdate should install: the exact
// version when pinned, otherwise the latest.
func fetchRelease(client selfupdate.HTTPDoer, pin string) (selfupdate.Release, error) {
	if pin != "" {
		return selfupdate.Tag(client, pin)
	}
	return selfupdate.Latest(client)
}

// installRelease downloads rel's archive for the running OS/architecture,
// verifies it against the release's checksums.txt, and replaces the binary
// at path.
func (a *app) installRelease(client selfupdate.HTTPDoer, rel selfupdate.Release, path string, mode os.FileMode, current string) error {
	assetName := selfupdate.AssetName(rel.Version, runtime.GOOS, runtime.GOARCH)
	assetURL, ok := rel.Assets[assetName]
	if !ok {
		return fmt.Errorf("release %s has no build for %s/%s (looked for %s)", rel.Version, runtime.GOOS, runtime.GOARCH, assetName)
	}
	checksumsURL, ok := rel.Assets["checksums.txt"]
	if !ok {
		return fmt.Errorf("release %s has no checksums.txt", rel.Version)
	}

	archive, err := selfupdate.Get(client, assetURL)
	if err != nil {
		return err
	}
	checksums, err := selfupdate.Get(client, checksumsURL)
	if err != nil {
		return err
	}

	if err := a.verifyChecksumsSignature(client, rel, checksums); err != nil {
		return err
	}

	if err := selfupdate.VerifyChecksum(checksums, assetName, archive); err != nil {
		return err
	}
	binary, err := selfupdate.ExtractFile(archive, "wyrm")
	if err != nil {
		return err
	}
	if err := selfupdate.Install(path, binary, mode); err != nil {
		return err
	}

	_, _ = fmt.Fprintf(a.stdout, "updated wyrm %s -> %s (%s)\n", current, rel.Version, path)
	return nil
}
