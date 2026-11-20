// Package webbundle builds a single-page app's web bundle with esbuild, at
// build time: the tree httpserver.SPA serves.
//
// An app's bundle command fills one [Config] and calls [Build]. It runs from
// the Dockerfile's builder stage and from go generate, never in the app
// binary. Build writes every built file as assets/<name>-<hash>.<ext> (a
// Classic entry with a URL at that root path instead), each shell at the
// tree's root with its logical script and stylesheet paths replaced by the
// built ones, the licence notices of every bundled npm package and own file
// under licenses/, and bundle.json, the manifest listing every written file
// with its SHA-256 and every entry with its logical name, kind and path.
// The tree is built beside Out and swapped in whole, so a failed build
// leaves the last good one, and two builds of the same inputs write the
// same bytes.
//
// [RenderPages] and [RenderPageCorpus] give the bytes of the TypeScript page
// table and the page corpus go generate commits; Build refuses either file
// when it is stale. [BuildStyle] returns one stylesheet built exactly as a
// Style entry is, for a test that compares CSS with the built bundle.
package webbundle
