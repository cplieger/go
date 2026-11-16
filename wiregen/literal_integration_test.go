//go:build integration

package wiregen_test

import "testing"

// A union of types whose const fields differ narrows on that field.
func TestConst_unionNarrows(t *testing.T) {
	runTS(t, literalReg(), map[string]string{"main.ts": `import type { A, B } from "./types.gen.js";
function size(u: A | B): number {
  if (u.kind === "a") {
    return u.n;
  }
  return u.m.length;
}
console.log(size({ kind: "b", m: "xy" }));
`})
}
