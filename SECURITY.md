# Security Policy

## Reporting a vulnerability

Please report vulnerabilities privately via
[GitHub security advisories](https://github.com/jskoll/wyrm/security/advisories/new)
rather than public issues.

## Scope note

wyrm config files **execute shell commands by design** (lifecycle hooks and
pane commands). "A config file can run commands" is the documented trust
model — the same as a Makefile — not a vulnerability. Reports about wyrm
executing commands from a config the user chose to run are out of scope;
anything that makes wyrm execute code the *config author* didn't write is
very much in scope.

## Reading a config before running it

`wyrm up -n` (and `wyrm restart -n`, `wyrm kill -n`) print the tmux commands
and the lifecycle hooks a config would run, and execute neither. That is the
supported way to inspect an unfamiliar config.

When cloning untrusted remote repositories, pass `wyrm clone -no-start <repo>`
(or `wyrm clone -n <repo>`) to clone the repository without automatically executing
lifecycle hooks or launching a session, allowing you to review `.wyrm.toml` first.

Before 0.6.0, `-n` printed the hooks' tmux commands but ran `on_project_start`
for real, which defeated the point. If you are on an older build, read the
config itself rather than relying on `-n`.

## Release signing

Release archives are verified by `wyrm selfupdate` in two steps: the SHA-256 in
`checksums.txt` proves the download was not corrupted, and a minisign signature
over `checksums.txt` proves the release came from the maintainers. The second
step is the one that matters if the release channel is ever compromised.

The public key lives in the repository at `internal/selfupdate/signing.pub` and
is compiled into every binary; only the secret key is a repository secret.

The committed public key is active and has been used for signed releases since
1.2.0. Keep the matching private key in the release secrets; do not generate a
replacement key as part of an ordinary release. The workflow uses:

| Secret | Value |
| --- | --- |
| `MINISIGN_SECRET_KEY` | the full contents of `minisign.key` |
| `MINISIGN_PASSWORD` | the password chosen at generation (empty if `-W` was used) |

The release workflow verifies that the committed public key matches the signing
key before goreleaser runs, so a mismatched pair fails the tag rather than
publishing a release whose own `selfupdate` would reject it. CI signs every pull
request with a throwaway key and asserts `checksums.txt.minisig` is produced, so
the signing path cannot silently stop working between releases.

Rotating the key requires a transition that keeps already shipped binaries able
to verify updates. Plan and document that migration before changing the
compiled public key.

### Verifying a release by hand

```sh
minisign -Vm checksums.txt -p internal/selfupdate/signing.pub
sha256sum -c --ignore-missing checksums.txt
```
