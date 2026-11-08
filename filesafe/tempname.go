package filesafe

// IsPackageTemp reports whether name is a temp file this package created:
// ".atomicfile-<digits>.tmp", with a non-empty all-digit middle. The prefix
// names the module this package grew from and is kept, so the sweeps still
// reclaim the temps that module left and an alert matching the shape still
// fires. It and TempName exist so a caller, or its tests, can plant or
// recognise a temp the sweeps reclaim without spelling the shape out.
//
// A file merely sharing the prefix and suffix (".atomicfile-notes.tmp") is
// NOT a package temp and no sweep will ever delete it. name is a single
// directory-entry name, not a path; pass filepath.Base(path) if you hold a
// path.
func IsPackageTemp(name string) bool { return isStaleTempName(name) }

// TempName returns a fresh temp base name of the exact shape this package
// creates and IsPackageTemp recognises, drawn from crypto/rand.
//
// Use it when a caller must create its own file in a directory this package
// sweeps and wants the sweep to reclaim it. Nothing is created; two calls can
// be used for two files.
//
// A collision with an existing file is possible in principle and not handled
// here: a caller creating the file with O_EXCL learns about it and can ask
// again, as this package's own writes do.
func TempName() string { return randomTempName() }
