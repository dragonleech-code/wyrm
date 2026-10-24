package selfupdate

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/ed25519"
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"

	"golang.org/x/crypto/blake2b"
)

// signingPubFile is the release-signing public key compiled into this binary.
//
// The key is public, so it lives in the repository rather than in a build
// secret; only the matching secret key is held by the release workflow. When
// the file carries no key, no key is embedded and selfupdate says so out loud
// rather than quietly verifying nothing.
//
//go:embed signing.pub
var signingPubFile []byte

// DefaultSigningKey is the key `wyrm selfupdate` verifies release checksums
// against. Tests replace it.
var DefaultSigningKey = parseEmbeddedKey()

func parseEmbeddedKey() SigningKey {
	key, err := ParseMinisignPublicKey(signingPubFile)
	if err != nil {
		return SigningKey{}
	}
	return key
}

// SigningKey is a release-signing public key, optionally carrying the minisign
// key ID that signatures are expected to name.
type SigningKey struct {
	key   ed25519.PublicKey
	id    [8]byte
	hasID bool
}

// Valid reports whether this key can actually verify anything.
func (k SigningKey) Valid() bool { return len(k.key) == ed25519.PublicKeySize }

// SigningKeyFromEd25519 wraps a bare Ed25519 public key. It carries no key ID,
// so signatures are accepted whatever ID they name.
func SigningKeyFromEd25519(pub ed25519.PublicKey) SigningKey {
	return SigningKey{key: pub}
}

// minisign algorithm tags. "Ed" signs the message itself; "ED" signs its
// BLAKE2b-512 hash. Current minisign (0.12) emits "ED" by default, so both
// have to be accepted.
const (
	algoLegacy  = "Ed"
	algoPrehash = "ED"
)

// minisignBlob decodes the base64 payload on the given line of a minisign
// file: a 2-byte algorithm tag, an 8-byte key ID, then the key or signature.
func minisignBlob(lines []string, n, wantLen int) (algo string, keyID [8]byte, rest []byte, err error) {
	if len(lines) <= n {
		return "", keyID, nil, errors.New("truncated minisign file")
	}
	raw, derr := base64.StdEncoding.DecodeString(strings.TrimSpace(lines[n]))
	if derr != nil {
		return "", keyID, nil, fmt.Errorf("decoding minisign payload: %w", derr)
	}
	if len(raw) != 10+wantLen {
		return "", keyID, nil, fmt.Errorf("minisign payload is %d bytes, want %d", len(raw), 10+wantLen)
	}
	copy(keyID[:], raw[2:10])
	return string(raw[:2]), keyID, raw[10:], nil
}

// ParseMinisignPublicKey reads a minisign .pub file: a comment line followed
// by base64 of "Ed" + 8-byte key ID + the 32-byte Ed25519 key.
func ParseMinisignPublicKey(data []byte) (SigningKey, error) {
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	algo, id, key, err := minisignBlob(lines, 1, ed25519.PublicKeySize)
	if err != nil {
		return SigningKey{}, err
	}
	if algo != algoLegacy {
		return SigningKey{}, fmt.Errorf("unsupported minisign public key algorithm %q", algo)
	}
	return SigningKey{key: ed25519.PublicKey(key), id: id, hasID: true}, nil
}

// Signature is a parsed release signature.
type Signature struct {
	// Prehashed reports whether Sig covers BLAKE2b-512(message) rather than
	// the message itself.
	Prehashed bool
	KeyID     [8]byte
	HasKeyID  bool
	Sig       []byte
}

// VerifyChecksumsSignature validates the Ed25519 signature of checksumsTxt
// against key. It accepts minisign files (both algorithms) as well as a bare
// 64-byte signature written raw, hex-encoded, or base64-encoded.
func VerifyChecksumsSignature(checksumsTxt, signature []byte, key SigningKey) error {
	if !key.Valid() {
		return errors.New("no public key provided for signature verification")
	}
	sig, err := ParseSignature(signature)
	if err != nil {
		return fmt.Errorf("parsing signature: %w", err)
	}
	if key.hasID && sig.HasKeyID && key.id != sig.KeyID {
		return fmt.Errorf("signature was made by key %x, want %x", sig.KeyID, key.id)
	}
	signed := checksumsTxt
	if sig.Prehashed {
		sum := blake2b.Sum512(checksumsTxt)
		signed = sum[:]
	}
	if !ed25519.Verify(key.key, signed, sig.Sig) {
		return errors.New("cryptographic signature verification failed for checksums.txt")
	}
	return nil
}

// ParseSignature extracts the Ed25519 signature from a minisign file, or from
// a bare 64-byte signature in raw, hex, or base64 form.
func ParseSignature(sig []byte) (Signature, error) {
	trimmed := strings.TrimSpace(string(sig))

	// Minisign:
	//   untrusted comment: ...
	//   <base64: algo | key id | signature>
	//   trusted comment: ...
	//   <base64: global signature>
	//
	// The global signature covers the trusted comment, which wyrm does not
	// read, so it is not checked here.
	if strings.HasPrefix(trimmed, "untrusted comment:") {
		lines := strings.Split(trimmed, "\n")
		algo, id, raw, err := minisignBlob(lines, 1, ed25519.SignatureSize)
		if err != nil {
			return Signature{}, err
		}
		switch algo {
		case algoLegacy, algoPrehash:
		default:
			return Signature{}, fmt.Errorf("unsupported minisign signature algorithm %q", algo)
		}
		return Signature{Prehashed: algo == algoPrehash, KeyID: id, HasKeyID: true, Sig: raw}, nil
	}

	// A bare Ed25519 signature over the message, in whichever encoding.
	if len(sig) == ed25519.SignatureSize {
		return Signature{Sig: sig}, nil
	}
	if len(trimmed) == ed25519.SignatureSize*2 {
		if decoded, err := hex.DecodeString(trimmed); err == nil {
			return Signature{Sig: decoded}, nil
		}
	}
	if decoded, err := base64.StdEncoding.DecodeString(trimmed); err == nil && len(decoded) == ed25519.SignatureSize {
		return Signature{Sig: decoded}, nil
	}

	return Signature{}, errors.New("unsupported or invalid signature format")
}

// VerifyChecksum checks that data's SHA-256 matches the entry for filename
// in a checksums.txt file, in the "<hex>  <filename>" format both sha256sum
// and goreleaser's checksum block produce.
func VerifyChecksum(checksumsTxt []byte, filename string, data []byte) error {
	want, err := checksumFor(checksumsTxt, filename)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(data)
	got := hex.EncodeToString(sum[:])
	if got != want {
		return fmt.Errorf("checksum mismatch for %s: got %s, want %s", filename, got, want)
	}
	return nil
}

func checksumFor(checksumsTxt []byte, filename string) (string, error) {
	for _, line := range strings.Split(string(checksumsTxt), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[1] == filename {
			return fields[0], nil
		}
	}
	return "", fmt.Errorf("%s not listed in checksums.txt", filename)
}

// MaxBinarySize is the maximum permitted size (64 MB) for a single extracted binary.
const MaxBinarySize = 64 * 1024 * 1024

// ExtractFile pulls a single named regular file's contents out of a .tar.gz
// archive, bounded by MaxBinarySize to prevent decompression bombs and OOM crashes.
func ExtractFile(archive []byte, name string) ([]byte, error) {
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, fmt.Errorf("opening archive: %w", err)
	}
	defer func() { _ = gz.Close() }()

	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("reading archive: %w", err)
		}
		if hdr.Typeflag == tar.TypeReg && hdr.Name == name {
			data, err := io.ReadAll(io.LimitReader(tr, MaxBinarySize+1))
			if err != nil {
				return nil, fmt.Errorf("reading %s from archive: %w", name, err)
			}
			if len(data) > MaxBinarySize {
				return nil, fmt.Errorf("%s exceeds maximum permitted binary size of %d bytes", name, MaxBinarySize)
			}
			return data, nil
		}
	}
	return nil, fmt.Errorf("%s not found in archive", name)
}
