# Security model

This page says what sealed protects, what it assumes and where its limits are, for a developer deciding what to seal with it.

## What it protects

- Without the key file, nobody can read a sealed value's payload or change the value unnoticed. Every value is AES-256-GCM, as NIST SP 800-38D defines it, with a 128-bit tag.
- A value opens only for the purpose and the kind it was sealed for, because its key is derived from both with HKDF-SHA256 (RFC 5869). The purpose is not written into the value.
- A failed open answers `ErrInvalid` with no detail. The value may have been malformed, too long, changed, sealed for another purpose or under a key the `Sealer` does not hold. `ErrExpired` comes only after a value authenticates, so a forged value never reads as expired.
- The expiry is inside the ciphertext, so a client holding a cookie or a cursor cannot read when it was made. No value carries a key id, so the values a client holds do not link to a key.
- The key file is refused when another user could read or replace it, and it is written at 0600 in a private directory.
- Keys are held in memory as `masked` secrets, so no `fmt` verb, `log/slog` handler or JSON encoder shows one. Derived keys and ciphers exist only within one call.
- A key-file error names the path and the line number, never a key, a line's text or a purpose.
- Key ids and MAC tags are not secret. An id is an HKDF output of its key and reveals nothing of it.

## What it assumes

- Only the app's user and root can read the key file and its directory. Anyone who runs as that user, or as root, or reads the app's memory holds the keys.
- The clock is right. `Seal` and `Open` read `time.Now` themselves. Setting the clock back by an hour lengthens every live value's life by an hour, as it does for any expiring cookie.
- One process on one host holds the key file, as [The key file](key-file.md) describes.

## Limits

- Seal at most 2^32 values of one purpose under one key. Each seal draws a fresh random 96-bit nonce, and NIST SP 800-38D §8.3 allows at most 2^32 seals per key with random nonces. Past that, a repeated nonce reveals the XOR of two payloads and that purpose's authentication key. RFC 5116 §5.1.1 states that forgeries under that key are then trivial.
- Each purpose has a key of its own, so the count is per purpose. One purpose sealed 100 times a second without pause reaches 2^32 in about 16 months. A purpose that could reach the count carries a variable part, or moves to a new key first. For the key file's `Sealer` that is `Rotate`. For a sealer built over a `Subkey` it is a new `Subkey`, since a rotation does not reset it.
- A `Subkey`, and a sealer `New` builds over it, keep the old key after `Rotate` and `Retire` until the caller takes a new `Subkey`. After a leaked key, rotate, then rebuild what holds a `Subkey` or restart.
- AES-GCM is not key-committing. Someone who holds two keys can make one value that opens under both, which matters only to someone who already holds the keys.
- An expiry is a whole second, rounded down. A value lives at least its ttl less one second, and never longer than its ttl.
- `Seal` takes at most 3,035 bytes and `SealAtRest` at most 64 KiB. Each open refuses a longer value before it decodes anything.

## Go's crypto modes

The module runs in Go's default mode and under Go's FIPS 140-3 module, with `GODEBUG=fips140=on` or `GODEBUG=fips140=only`. It seals through `cipher.NewGCMWithRandomNonce` and derives through `crypto/hkdf`, which FIPS 140-only mode permits. A build with `GOEXPERIMENT=boringcrypto` cannot seal or open, and each such call answers an error naming the cause, never a wrong value.
