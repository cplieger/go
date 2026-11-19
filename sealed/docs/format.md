# Key file and sealed value formats

This page gives the key file and both kinds of sealed value byte by byte, with every key derivation and the test vectors, for a developer who writes another reader or checks this one.

## The key file

The grammar is ABNF, as RFC 5234 defines it, with the case-sensitive `%s` strings of RFC 7405:

```abnf
key-file      = version-line current-line [previous-line]
version-line  = %s"sealed-key/v1" LF
current-line  = %s"current" SP id SP key LF
previous-line = %s"previous" SP id SP key LF
id            = 32lhex                 ; the key's id, see Key derivations
key           = 64lhex                 ; the key's 32 bytes
lhex          = %x30-39 / %x61-66      ; 0-9 a-f, lowercase only
SP            = %x20
LF            = %x0A
```

| Line | Bytes |
| --- | --- |
| `sealed-key/v1` and LF | 14 |
| `current`, SP, id, SP, key, LF | 8 + 32 + 1 + 64 + 1 = 106 |
| `previous`, SP, id, SP, key, LF | 9 + 32 + 1 + 64 + 1 = 107 |
| Whole file | 120 with one key, 227 with two |

A reader enforces these rules, and refuses a file that breaks one with `ErrKeyFileFormat`, naming the line number and never the line's text:

1. The file is opened without following a symlink, its owner and mode are checked on the open descriptor, and at most 512 bytes are read from that same descriptor. A larger file is refused before it is read.
2. The bytes match the grammar exactly. Nothing comes before the first line or after the last LF, and there is no CR, no other whitespace and no uppercase hex.
3. Each `id` equals the id derived from the `key` on its line.
4. A `previous` line's id differs from the `current` line's.
5. The reader encodes what it read again and compares the result with the file byte for byte, so one key state has one spelling.

The file is written only as a whole, through filesafe's atomic write, so it is never partly written. Its mode is 0600, and an open also accepts 0400. Its owner is the app's effective user, and its directory admits no group or other access.

Beside it, the lock file `<path>.lock` holds the lease. filesafe creates it at 0600 in the key file's directory, which must exist. It is empty, nothing reads or writes its content, and it is not part of the format. A backup copies the key file alone, and a restored key file without a lock file gets a new one at its first open.

## Sealed values

An expiring value, made by `Seal`:

| Offset | Bytes | Field |
| --- | --- | --- |
| 0 | 1 | Version, `0x01` |
| 1 | 12 | GCM nonce N, 96 bits from `crypto/rand` |
| 13 | 8 + n | GCM ciphertext C of P, the expiry (8) then the payload (n) |
| 21 + n | 16 | GCM tag T |
| | 37 + n | Total |

- The expiry is Unix seconds, a signed 64-bit integer in big-endian two's complement, `floor(seal time + ttl)`. `Open` accepts the value while the time is before that second.
- The GCM key is the expiring value key for the value's purpose under one ring key, as [Key derivations](#key-derivations) defines it. The GCM additional data A is the one byte `0x01`.

An at-rest value, made by `SealAtRest`:

| Offset | Bytes | Field |
| --- | --- | --- |
| 0 | 1 | Version, `0x01` |
| 1 | 12 | GCM nonce N |
| 13 | n | GCM ciphertext C of P, the payload (n) |
| 13 + n | 16 | GCM tag T |
| | 29 + n | Total |

- The GCM key is the at-rest value key for the value's purpose, and A is `0x01`.

Both kinds are written as base64url, RFC 4648 §5, without the padding of §3.2 and with the canonical trailing bits of §3.5. `Open` and `OpenAtRest` take these steps in order:

1. Refuse a value longer than `ExpiringLen(MaxPayload)`, 4,096 characters, for `Open`, or `AtRestLen(MaxAtRestPayload)`, 87,420 characters, for `OpenAtRest`, before decoding anything.
2. Decode with `base64.RawURLEncoding.Strict()`, and refuse the value unless encoding the decoded bytes again gives its own length. The decoder skips `\r` and `\n`, as RFC 4648 §3.3 allows, and this check refuses them.
3. Refuse fewer than 37 bytes for an expiring value or 29 for an at-rest one, and a first byte other than `0x01`.
4. For the current key, then the previous key, derive the GCM key and open N, C and T with A = `0x01`. The first key that authenticates wins, and `current` reports whether it was the current key. In Go this is `aead.Open(nil, nil, raw[1:], []byte{0x01})` on `cipher.NewGCMWithRandomNonce`, which reads N from the front of `raw[1:]` and panics on a non-empty nonce argument.
5. Refuse the value when no key authenticates it (`ErrInvalid`), and refuse an expiring value whose expiry is at or before now (`ErrExpired`).

Every refusal of steps 1 to 4 is `ErrInvalid`, with no detail and with `current` false. A caller learns only that the value did not authenticate, as libsodium's single `-1` tells its caller.

## Key derivations

Every derived key is the HKDF-SHA256 of RFC 5869 §2, with IKM one 32-byte ring key and an empty salt, which §2.2 defines as HashLen zero bytes and Go's `hkdf.Key` takes as a nil salt. Its `info` and length L are these:

| Use | `info` | L, in bytes | Bound to the value format |
| --- | --- | --- | --- |
| Key id | `sealed/key-id` | 16, written as 32 lowercase hex | No, since an id never changes |
| Expiring value key | `sealed/v1/expiring` NUL purpose | 32, an AES-256 key | Yes |
| At-rest value key | `sealed/v1/at-rest` NUL purpose | 32, an AES-256 key | Yes |
| `Subkey` | `sealed/subkey` NUL label | 32 | No |
| `MAC` key | `sealed/mac` NUL label | 32, an HMAC-SHA256 key | No |

NUL is the byte `0x00`. The map from a use and its purpose or label to `info` is injective, which RFC 5869 §3.2 needs for its separation. The key-id `info` has no NUL. Every other `info` is a fixed word, a NUL, then the caller's bytes, and none of the four words contains a NUL or equals another.

So reading up to the first NUL recovers the use, and the rest is exactly the caller's purpose or label. A purpose may itself hold NUL bytes, and only the first NUL separates. RFC 5116 §3.3 asks the same injectivity of additional data. Here it is asked of `info`, where the binding happens.

The value key `info` strings carry `v1`, so a future value format gets keys of its own. The key id, `Subkey` and `MAC` derivations carry no version. What they make is recorded outside the module, as an app's recorded key id, a library's key or an archive's MAC, so it must stay the same across releases while the key does.

A `MAC` tag is the 32-byte HMAC-SHA256 of RFC 2104 §2, over the message under the MAC key. `VerifyMAC` refuses a tag of any other length.

## Versioning

- The key file's first line names its version. A release reads every version it ever wrote and writes the newest it needs. This release reads and writes `v1` only.
- A value's first byte names its version. A release opens every version it ever sealed. This release seals and opens `0x01` only, and a future format takes `0x02`, with derivation words of its own.
- The key id, `Subkey` and `MAC` derivations never change. A change to any of them would be a new derivation with a new word, beside the old one.

## Test vectors

These vectors are in `testdata/vectors.json`, computed with Python 3.15.2's `cryptography` 50.0.2 over OpenSSL 4.0.3 from the definitions above, never from this module's output. The module's tests check it against them with Go 1.27.4's standard library. `testdata/v1-fixture.txt` keeps one value of each kind this release sealed, and the tests open both, so a later release still opens what `0x01` sealed.

- Key K1 is `000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f`, with id `70603bd36c8291d779397aa614e66b28`.
- Key K2 is `202122232425262728292a2b2c2d2e2f303132333435363738393a3b3c3d3e3f`, with id `0681d8a548d4df142f5e9a3ab863e9ba`.
- A key file holding K2 as current and K1 as previous:

  ```text
  sealed-key/v1
  current 0681d8a548d4df142f5e9a3ab863e9ba 202122232425262728292a2b2c2d2e2f303132333435363738393a3b3c3d3e3f
  previous 70603bd36c8291d779397aa614e66b28 000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f
  ```

- Under K1, with purpose `example`, nonce `a0a1a2a3a4a5a6a7a8a9aaab` and payload `hello`:
  - the expiring value key is `b28c9a34b25fc3e55a413239acef74ef998cb35e9d49acccbbbe3998e800097c`, and with expiry 4102444800, 2100-01-01T00:00:00Z, the value is `AaChoqOkpaanqKmqq89Rc9CQ2lWXBo36gugw72WLmhj7BOGqgdRSRzsp`, 42 bytes in 56 characters.
  - the at-rest value key is `a3664333faa15ab2e4bb3a2acad133bf1b54feb5bf7948d9f1ad1c725b8fa194`, and the value is `AaChoqOkpaanqKmqq_nCUFgeYVjJApnL6jlNSQwfJWtLNw`, 34 bytes in 46 characters.
- Under K1, `Subkey("example/cursor")` is `06a613c5bc49e3e6efd70c3b5f8bf403c9fb981197d71bfaca44446225db35f8`, and `MAC("example/manifest", "example manifest")` is `90c989e51170bc63130b9888d84f37a7e962ede9ba867393c5b177351f10fa09` with key id `70603bd36c8291d779397aa614e66b28`.
- RFC 5869 Appendix A.3, SHA-256 with a zero-length salt and info, gives OKM `8da4e775a563c18f715f802a063c5a31b8a11f5c5ee1879ec3454e5f3c738d2d9d201395faa4b61a96c8` through the same `hkdf.Key` call with a nil salt, which pins that a nil salt is the RFC's empty salt.
- Lengths: `ExpiringLen(0)` is 50, `ExpiringLen(1024)` 1,415, `ExpiringLen(1025)` 1,416, `ExpiringLen(3035)` 4,096 and `ExpiringLen(3036)` 0. `AtRestLen(0)` is 39, `AtRestLen(3035)` 4,086, `AtRestLen(65536)` 87,420 and `AtRestLen(65537)` 0.
