// Package keylock is a set of mutexes, one per key, for work that must not
// interleave with itself on one key while other keys proceed. A wait ends
// when its context does, and the caller then holds nothing. A key's entry is
// reference-counted and lives only while a caller holds or waits on it, so
// memory follows in-flight work, not the key space. [Set.LockAll] takes
// several keys in the one order the [Set] was built with, so callers naming
// the same keys in any order never deadlock.
package keylock
