//go:build integration

package wiregen_test

import (
	"strings"
	"testing"
)

// The union decoders type-check under tsc and, under node, decode each case,
// refusing a field another case owns and an unknown tag.
func TestUnion_DecoderRuns(t *testing.T) {
	out := runTS(t, unionReg(), map[string]string{"main.ts": `import { decodeHolder } from "./decoders.gen.js";
import type { Holder } from "./types.gen.js";
const bodies = [
  '{"timing":{"kind":"identity"},"shapes":[]}',
  '{"timing":{"kind":"shift","ms":5},"shapes":[{"type":"circle","radius":1,"tags":null}]}',
  '{"timing":{"kind":"linear","scale":1.5,"offset_ms":-3},"shapes":[{"type":"square","side":2,"tags":["a"]}]}',
  '{"timing":{"kind":"identity","ms":5},"shapes":[]}',
  '{"timing":{"kind":"shift","ms":null,"scale":2},"shapes":[]}',
  '{"timing":{"kind":"other"},"shapes":[]}',
  '{"timing":{"kind":"shift"},"shapes":[{"type":"circle","side":1,"tags":[]}]}',
];
for (const body of bodies) {
  try {
    const h: Holder = decodeHolder(JSON.parse(body));
    console.log("ok " + JSON.stringify(h));
  } catch (e) {
    console.log("refused " + (e as Error).message);
  }
}
`})
	want := strings.Join([]string{
		`ok {"timing":{"kind":"identity"},"shapes":[]}`,
		`ok {"timing":{"kind":"shift","ms":5},"shapes":[{"type":"circle","tags":[],"radius":1}]}`,
		`ok {"timing":{"kind":"linear","scale":1.5,"offset_ms":-3},"shapes":[{"type":"square","tags":["a"],"side":2}]}`,
		`refused $.timing.ms: not allowed in this case`,
		`refused $.timing.scale: not allowed in this case`,
		`refused $.timing.kind: expected one of identity|shift|linear, got "other"`,
		`refused $.holder.shapes[0]: $.shape.side: not allowed in this case`,
		"",
	}, "\n")
	if out != want {
		t.Errorf("node output:\n%s\nwant:\n%s", out, want)
	}
}

// A payload-typed union type-checks under tsc and, under node, decodes each
// case's payload with that case's decoder, refusing a payload in a case with
// none and a payload of another case's shape; a null payload is absent.
func TestUnion_PayloadDecoderRuns(t *testing.T) {
	out := runTS(t, commandReg(), map[string]string{"main.ts": `import { decodeCommand } from "./decoders.gen.js";
import type { Command } from "./types.gen.js";
const bodies = [
  '{"type":"create","payload":{"title":"a"}}',
  '{"type":"rename","chat_id":"c","payload":{"name":"n"}}',
  '{"type":"close","chat_id":"c"}',
  '{"type":"create"}',
  '{"type":"close","payload":{"name":"n"}}',
  '{"type":"create","payload":{"name":"n"}}',
  '{"type":"rename","payload":null}',
];
for (const body of bodies) {
  try {
    const c: Command = decodeCommand(JSON.parse(body));
    if (c.type === "create" && c.payload !== undefined) {
      const title: string = c.payload.title;
      void title;
    }
    console.log("ok " + JSON.stringify(c));
  } catch (e) {
    console.log("refused " + (e as Error).message);
  }
}
`})
	want := strings.Join([]string{
		`ok {"type":"create","payload":{"title":"a"}}`,
		`ok {"type":"rename","chat_id":"c","payload":{"name":"n"}}`,
		`ok {"type":"close","chat_id":"c"}`,
		`ok {"type":"create"}`,
		`refused $.command.payload: not allowed in this case`,
		`refused $.create_chat.title: expected string, got undefined`,
		`ok {"type":"rename"}`,
		"",
	}, "\n")
	if out != want {
		t.Errorf("node output:\n%s\nwant:\n%s", out, want)
	}
}

// The bounded decoders of the full document's registry type-check, and
// node runs them.
func TestBoundedDecoders_typeCheck(t *testing.T) {
	out := runTS(t, fullReg(), map[string]string{
		"api-client.ts": transportTS(t),
		"main.ts": `import { decodeCreate } from "./decoders.gen.js";
try {
  decodeCreate({ labels: {}, files: [], shared: {}, limit: 1, level: "low" });
} catch (e) {
  console.log((e as Error).message);
}
`,
	})
	if want := "$.create.files: expected 1 to 3 items, got 0\n"; out != want {
		t.Errorf("node output %q, want %q", out, want)
	}
}

// TestUnion_PathOverrideWithTemplateSyntaxStaysData pins that a backtick or
// "${" in a PathNameOverride reaches the union decoder's unknown-tag error as
// text: the file still type-checks and the message carries the characters.
func TestUnion_PathOverrideWithTemplateSyntaxStaysData(t *testing.T) {
	r := unionReg()
	r.PathNameOverride = map[string]string{"Shape": "sh`ape${1}"}
	out := runTS(t, r, map[string]string{"main.ts": `import { decodeShape } from "./decoders.gen.js";
try {
  decodeShape(JSON.parse('{"type":"hexagon"}'));
} catch (e) {
  console.log((e as Error).message);
}
`})
	if !strings.Contains(out, "$.sh`ape${1}") {
		t.Errorf("decoder message = %q, want the override's characters verbatim", out)
	}
}
