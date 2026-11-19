# sealed

[![Go Reference](https://pkg.go.dev/badge/github.com/cplieger/go/sealed.svg)](https://pkg.go.dev/github.com/cplieger/go/sealed) [![Go version](https://img.shields.io/github/go-mod/go-version/cplieger/go?filename=sealed%2Fgo.mod)](https://github.com/cplieger/go/blob/main/sealed/go.mod)

sealed keeps your Go app's key file and seals its cookies, cursors and stored secrets under it with AES-256-GCM, with key rotation that tells you which values to seal again.

It replaces the key file and the AES-GCM code each app would otherwise write for itself. It uses the standard library and two modules of this repository, [filesafe](https://github.com/cplieger/go/tree/main/filesafe) and [masked](https://github.com/cplieger/go/tree/main/masked), and is licensed under Apache-2.0.

## Why use it

sealed is built for Go services that keep secrets in a store or hand out cookies and page cursors, and that must rotate their key without losing either.

- One key file per app, written atomically at 0600 in a private directory, and refused when another user could read or replace it.
- A rotation keeps the previous key for opening, and every open reports `current`, false for a value you should seal again.
- Each value is bound to a purpose you name, so it never opens for another purpose or as the other kind of value.
- Expiring values fit in a cookie, and stored values take up to 64 KiB.
- A missing key file is an answer, `ErrNoKey`, so your app can show that state instead of failing to start.

Consider [Tink](https://developers.google.com/tink) if you need several algorithms, key types or one keyset that many processes share.

## Install

```sh
go get github.com/cplieger/go/sealed@latest
```

## Usage

Open the key file at start, create it at the first start, and hand its `Sealer` to every store:

```go
const tokenPurpose sealed.Purpose = "example/github-token"

keys, err := sealed.OpenKeyFile(ctx, cfg.KeyPath) // your app's own setting, an absolute path
if err != nil {
	return err // ErrUnsafeKeyFile, ErrKeyFileFormat, ErrKeyFileInUse: a key outage, never a new key
}
defer keys.Close() // last, after the stores that use its Sealer

s := keys.Sealer() // the one Sealer for the process; it answers ErrNoKey until a key exists
if s.KeyIDs().Current == "" && store.HoldsNoSealedValue() {
	if err := keys.Create(ctx); err != nil {
		return err
	}
}

v, err := s.SealAtRest(tokenPurpose, token)
payload, current, err := s.OpenAtRest(tokenPurpose, v) // !current: seal it again
```

Rotate in the process that holds the key file, from your app's own admin command:

```go
func rotateKeys(ctx context.Context, keys *sealed.KeyFile) error {
	if err := keys.Rotate(ctx); err != nil {
		return err // ErrRotationPending: retire the previous key first
	}
	// Seal again every value that opens with current == false, take a new
	// Subkey, then call keys.Retire(ctx) behind a person's step.
	return nil
}
```

The package examples on pkg.go.dev show a first start, a rotation and a derived key, and `go test` keeps them true.

## API

- Key file: `OpenKeyFile`, and on `KeyFile`, `Sealer`, `Create`, `Rotate`, `Retire` and `Close`.
- Sealers: `New` and `NewEphemeral`, and on `Sealer`, `Seal`, `Open`, `SealAtRest`, `OpenAtRest` and `KeyIDs`, with the `Purpose` and `KeyIDs` types.
- Derived keys: `Subkey`, `MAC` and `VerifyMAC` on `Sealer`, by `Label`.
- Sizes: `MaxPayload`, `MaxAtRestPayload`, `ExpiringLen` and `AtRestLen`.
- Errors for `errors.Is`: `ErrNoKey`, `ErrInvalid`, `ErrExpired`, `ErrTooLarge`, `ErrShortTTL`, `ErrKeyExists`, `ErrRotationPending`, `ErrKeyFileChanged`, `ErrNotDurable`, `ErrKeyFileInUse`, `ErrKeyFileFormat` and `ErrUnsafeKeyFile`.

The full reference is on [pkg.go.dev](https://pkg.go.dev/github.com/cplieger/go/sealed).

## The key file

Put the key file in a directory only your app's user can enter, and pass its absolute path. `Create` makes that directory, one level, at 0700, and needs its parent to be readable by the app's user. An open never makes a directory or a file.

Beside the key file, the module keeps an empty lock file, the key file's name with `.lock` added, at 0600 and never removes it. Leave it in place at that mode, since a lock file group or other can open is refused. One process holds the key file at a time, from `OpenKeyFile` to `Close`, and a second one waits until its context ends, then gets `ErrKeyFileInUse`. So `Rotate` and `Retire` run in the serving process. Close the `KeyFile` last at shutdown.

Back up the key file by copying it, and restore it only while the app is stopped. Losing it loses every value sealed under it, the secrets in an export included.

A key-file write the filesystem does not report durable installs nothing and answers `ErrNotDurable`. Fix the disk and reboot the host before the app starts again, or retry once the cause is fixed. The key file needs a writable filesystem on one host. Windows, AIX and Solaris refuse a key file, and `New` and `NewEphemeral` work everywhere. [The key file](docs/key-file.md) covers the lease, rotation, every error, and the filesystems and systems that work.

## Values

Each value is sealed for a `Purpose`, bytes you declare once as a constant, and its key is derived from it. Join a purpose's variable parts with an encoding no two part lists share, such as a length before each part.

- `Seal` makes an expiring value for a cookie, URL or cursor, from at most `MaxPayload`, 3,035 bytes. Its value is 4,096 characters, the most a browser keeps for a cookie's name and value together by [section 5.6 of the cookie draft](https://datatracker.ietf.org/doc/html/draft-ietf-httpbis-rfc6265bis-22#section-5.6), so leave room for the name.
- `SealAtRest` makes a value with no expiry for a store, from at most `MaxAtRestPayload`, 64 KiB.
- `Seal` and `Open` read the clock. A value opens until its ttl, rounded down to the second, so a clock set back lengthens every live value's life.

`Subkey` derives a 32-byte key by a `Label` for a library that takes one key, and `MAC` and `VerifyMAC` sign and check bytes under the current or previous key. A `Subkey` keeps the old key after `Rotate` and `Retire`, so take a new one and rebuild what holds it, or restart. Keep stored values on the key file's `Sealer`.

Seal at most 2^32 values of one purpose under one key. Past that, a repeated nonce can reveal two payloads and let anyone forge that purpose's values. A purpose that could reach the count carries a variable part or moves to a new key first. [The security model](docs/security-model.md) lists every limit, and [the formats](docs/format.md) give every byte.

## Unsupported by design

These are deliberate non-goals.

- More than two keys. A rotation needs a current and a previous key, and `Rotate` refuses a third until `Retire`.
- Automatic retirement. Only you know when every value under the previous key is sealed again.
- Several processes or hosts on one key file. The lease admits one holder, so a new key has nothing to roll out to.
- An open that creates the key file. A missing file is a state your app must see.
- Importing a key. Restoring is copying the file back, so no call can install a wrong or stolen key unseen.
- Key files on Windows. Its access rules are ACLs, which the standard library cannot check.
- A choice of algorithm. Every value is AES-256-GCM with a random 96-bit nonce.
- A key id in each value. It would link the values a client holds to a key.

## Documentation

- [The key file](docs/key-file.md) covers where to put it, the lease, rotation, durability and every error.
- [Formats](docs/format.md) gives the key file and both kinds of value byte by byte, with test vectors.
- [Security model](docs/security-model.md) covers what the module protects, what it assumes and its limits.

## Credits

The key ring follows the keysets of [Tink](https://developers.google.com/tink/design/keysets), in which the primary key seals and every key opens. The key file's version line, a key derived with HKDF-SHA-256 for each use and the strict base64 follow the [age format](https://github.com/C2SP/C2SP/blob/main/age.md). The single failure answer and the size check before decrypting follow [libsodium](https://doc.libsodium.org/secret-key_cryptography/secretbox).

## Contributing

Issues and pull requests are welcome. The [shared contributing rules](https://github.com/cplieger/.github/blob/main/CONTRIBUTING.md) apply.

## Disclaimer

This project is built with care and follows security best practices, but it is intended for personal / self-hosted use. No guarantees of fitness for production environments. Use at your own risk.

This project was built with AI assistance. The human maintainer defines architecture, supervises implementation, and makes all final decisions.

## License

Apache-2.0. See [LICENSE](LICENSE).
