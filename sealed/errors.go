package sealed

import "errors"

// ErrNoKey is the answer of every Sealer call that needs a key from a
// Sealer holding none, and of Rotate and Retire on a KeyFile holding
// none. A KeyFile opened where no file exists holds none until Create,
// and a closed KeyFile's Sealer holds none.
var ErrNoKey = errors.New("sealed: no key")

// ErrInvalid is the answer for a value or MAC tag that does not
// authenticate: malformed, too long, made for another purpose, label or
// kind, under no key the Sealer holds, or changed.
var ErrInvalid = errors.New("sealed: does not authenticate")

// ErrExpired is Open's answer for an authentic value whose expiry has
// passed.
var ErrExpired = errors.New("sealed: value expired")

// ErrTooLarge is the refusal of a payload over MaxPayload by Seal, or
// over MaxAtRestPayload by SealAtRest.
var ErrTooLarge = errors.New("sealed: payload too large")

// ErrShortTTL is Seal's refusal of a ttl under one second.
var ErrShortTTL = errors.New("sealed: ttl under one second")

// ErrKeyExists is Create's answer when the KeyFile holds a key or
// anything is at its path other than a file this KeyFile wrote and did
// not install (see ErrNotDurable). Create then writes nothing.
var ErrKeyExists = errors.New("sealed: key file exists")

// ErrRotationPending is Rotate's answer while the file holds a previous
// key: Retire it once every value under it is sealed again.
var ErrRotationPending = errors.New("sealed: previous key not retired")

// ErrKeyFileChanged is the answer of Rotate and Retire when the file is
// missing or holds keys this KeyFile neither installed nor wrote, or the
// lock file beside it is not the one its lease locked: a person or
// another tool changed them. Nothing is written and the KeyFile keeps its
// keys. Once a person decides which file is right, restart the app, or
// close the KeyFile, open the path again and hand every holder of the old
// Sealer the new KeyFile's Sealer: the closed one holds no key.
var ErrKeyFileChanged = errors.New("sealed: key file changed")

// ErrNotDurable is the answer of Create, Rotate and Retire when filesafe
// could not report a write durable. When the new file reached the path,
// nothing is installed: the KeyFile keeps its keys and the lease, every
// file a crash can leave holds the key its Sealer seals under, and a retry
// through it writes a new file; fix the disk and reboot before the app
// restarts, since the next OpenKeyFile adopts what is at the path. A Create
// that could not sync the key directory wrote no file and took no lease; one
// held for an earlier such file stays while its lock file is at the path.
var ErrNotDurable = errors.New("sealed: key file write not durable")

// ErrKeyFileInUse is the answer of OpenKeyFile and Create when another
// KeyFile, in this process or another, holds the key file's lease and
// ctx ended while waiting for it. It wraps filesafe.ErrLeaseHeld and the
// context's error.
var ErrKeyFileInUse = errors.New("sealed: key file in use")

// ErrKeyFileFormat is the refusal of a file that is not a key file this
// release reads: another first line, a newer version, a malformed line,
// an id that does not match its key, or over 512 bytes.
var ErrKeyFileFormat = errors.New("sealed: not a key file this release reads")

// ErrUnsafeKeyFile is the refusal of a key file another user could read
// or replace: a symlink, not a regular file, owned by another user, a
// mode with a bit outside 0600, in a directory that is not private, or
// beside a lock file that is a symlink, not a regular file, another
// user's or open to group or other. It wraps filesafe's error where
// filesafe made the check, and on a system where the module cannot check
// owners and modes or take the lease (Windows, AIX and Solaris) it wraps
// filesafe.ErrModeUnsupported.
var ErrUnsafeKeyFile = errors.New("sealed: key file not private")
