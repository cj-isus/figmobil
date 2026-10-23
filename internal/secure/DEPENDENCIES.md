# Secure component dependencies

This component uses the following pinned Go modules. The Go module and checksum
files at the repository root are authoritative; there are no downloaded runtime
executables or target-device installations.

| Module | Version | License | Module checksum |
|---|---|---|---|
| filippo.io/age | v1.3.2 | BSD-3-Clause | `h1:r6RSZLFSMm6rzKepZ7ZAYkKCu14f3/Me8c7uKYh7C8c=` |
| filippo.io/hpke | v0.4.0 | BSD-3-Clause | `h1:p575VVQ6ted4pL+it6M00V/f2qTZITO0zgmdKCkd5+A=` |
| golang.org/x/crypto | v0.55.0 | BSD-3-Clause | `h1:+KWHjbgOaAQ66dh/YlkZKHlz9ZUlq61AFirAR9ntP8M=` |
| golang.org/x/sys | v0.47.0 | BSD-3-Clause | `h1:o7XGOvZQCADBQQ4Y7VNq2dRWQR7JmOUW8Kxx4ZsNgWs=` |

Sources inspected on 2026-10-22: module-cache source and LICENSE files downloaded
through Go modules, [age v1.3.2 release](https://github.com/FiloSottile/age/releases/tag/v1.3.2),
[age package documentation](https://pkg.go.dev/filippo.io/age@v1.3.2), and the
Microsoft documentation for [DPAPI CurrentUser](https://learn.microsoft.com/en-us/windows/win32/api/dpapi/nf-dpapi-cryptprotectdata)
and [named mutex creation](https://learn.microsoft.com/en-us/windows/win32/api/synchapi/nf-synchapi-createmutexw).
The release rejects excessive age headers/recipient counts. Only X25519 recipients
and separate Ed25519 signing keys are accepted by this component; it does not run
age compatibility plugins, commands, passwords or imported code.

Redistributed binaries must include [THIRD-PARTY-NOTICES.txt](THIRD-PARTY-NOTICES.txt)
in their accompanying documentation or materials. Test-only module checksums in
go.sum are not a statement that those packages are linked into the application.
The host's existing global checksum-database setting is disabled. Local module
hash verification passed, but a separate authenticated public checksum-database
audit could not finish because of network failures. The distinct results and
their scope are recorded in [dependency-verification.json](dependency-verification.json).

## API and protocol boundary

- `PublicKeys` contains only canonical public encodings. `ValidatePublic` rejects
  malformed/noncanonical values. Initial trust and key rotation belong to pairing.
- `Sign`/`Verify` bind the complete signer key bundle to a separate signature
  domain. A caller's typed payload must bind its kind/version, project, recipient,
  transcript, package/base and acknowledgement identities as applicable.
- `Seal` encrypts a typed envelope containing sender, recipient, payload and a
  domain-separated Ed25519 signature over both complete key bundles and payload.
- `Open` returns no plaintext on authentication, sender/recipient or size failure.
  It reads the complete bounded age stream before verifying the envelope. Replay
  prevention, package path rules and confirmed shared-base advancement are outside
  this cryptographic primitive and require workspace validation.
  Payload allowance is at most 64 MiB and ciphertext at most 96 MiB; JSON/base64
  and transport framing still require separate upper-layer size checks. These
  are development safety limits, not Figma text-layer capacity claims.
- `Protect`/`Restore` serialize private identity material only in memory and protect
  it with DPAPI CurrentUser. `ProtectData`/`UnprotectData` support opaque local state
  up to 128 MiB; no file I/O occurs in this package. DPAPI is local at-rest protection,
  not protection against another process running as the same Windows user. Memory
  clearing is best effort and is not a promise to erase every Go/runtime copy.
- `AcquireOwner` uses a fixed `Global\Figmobil.DeviceOwner` object across versions,
  projects and Windows sessions. Existing/inaccessible objects fail closed. The
  non-thread-owned handle lifetime avoids goroutine/OS-thread ownership mistakes.
  Release only after active handoffs stop; instantiate the shared rate gate after
  acquiring this owner. It is an application coordination boundary, not protection
  against a user deliberately modifying or bypassing the application.

Non-Windows local protection and device ownership fail closed. No source files,
private keys or plaintext state are persisted by this package. Private identities
reject JSON serialization and redact ordinary formatting.
