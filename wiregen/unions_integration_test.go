//go:build integration

package wiregen_test

import (
	"strings"
	"testing"

	"github.com/cplieger/go/wiregen/v2"
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

// A case's required fields are non-optional members under tsc, and node
// refuses each one absent or null, while the case that does not list stored
// reads it as optional; a required json.RawMessage passes null as data.
func TestUnionRequired_DecoderRuns(t *testing.T) {
	out := runTS(t, checkedReg(), map[string]string{"main.ts": `import { decodeChecked } from "./decoders.gen.js";
import type { Checked } from "./types.gen.js";
const bodies = [
  '{"state":"gone","stored":{"path":"p"}}',
  '{"state":"gone"}',
  '{"state":"updated","stored":null}',
  '{"state":"unchanged"}',
  '{"state":"unchanged","stored":null}',
  '{"state":"error","failure":"f","detail":null}',
  '{"state":"error","detail":1}',
  '{"state":"error","failure":"f"}',
];
for (const body of bodies) {
  try {
    const c: Checked = decodeChecked(JSON.parse(body));
    if (c.state === "gone" || c.state === "updated") {
      const path: string = c.stored.path;
      void path;
    }
    console.log("ok " + JSON.stringify(c));
  } catch (e) {
    console.log("refused " + (e as Error).message);
  }
}
`})
	want := strings.Join([]string{
		`ok {"state":"gone","stored":{"path":"p"}}`,
		`refused $.checked.stored: expected a JSON value, got undefined`,
		`refused $.stored: expected object, got null`,
		`ok {"state":"unchanged"}`,
		`ok {"state":"unchanged"}`,
		`ok {"state":"error","failure":"f","detail":null}`,
		`refused $.checked.failure: expected string, got undefined`,
		`refused $.checked.detail: expected a JSON value, got undefined`,
		"",
	}, "\n")
	if out != want {
		t.Errorf("node output:\n%s\nwant:\n%s", out, want)
	}
}

// A required payload is a non-optional member under tsc, and node refuses
// it absent or null in its case while another case's payload stays optional.
func TestUnionRequired_PayloadDecoderRuns(t *testing.T) {
	r := newTestRegistry()
	r.Unions = []wiregen.Union{requiredPayloadCommand()}
	out := runTS(t, r, map[string]string{"main.ts": `import { decodeCommand } from "./decoders.gen.js";
import type { Command } from "./types.gen.js";
const bodies = [
  '{"type":"create","payload":{"title":"a"}}',
  '{"type":"create"}',
  '{"type":"create","payload":null}',
  '{"type":"rename"}',
];
for (const body of bodies) {
  try {
    const c: Command = decodeCommand(JSON.parse(body));
    if (c.type === "create") {
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
		`refused $.command.payload: expected a JSON value, got undefined`,
		`refused $.create_chat: expected object, got null`,
		`ok {"type":"rename"}`,
		"",
	}, "\n")
	if out != want {
		t.Errorf("node output:\n%s\nwant:\n%s", out, want)
	}
}

// In the case that lists them, node refuses a []byte, slice or named map
// null or absent and a mapped field with no Decoder absent, while the case
// that does not list them reads null as absent; tsc checks the listing
// case's members need no undefined guard.
func TestUnionRequired_NullableFieldsDecoderRuns(t *testing.T) {
	out := runTS(t, parcelReg(), map[string]string{"main.ts": `import { decodeParcel } from "./decoders.gen.js";
import type { Parcel } from "./types.gen.js";
const bodies = [
  '{"kind":"full","data":"","tags":[],"labels":{},"at":1}',
  '{"kind":"full","data":null,"tags":[],"labels":{},"at":1}',
  '{"kind":"full","tags":[],"labels":{},"at":1}',
  '{"kind":"full","data":"","tags":null,"labels":{},"at":1}',
  '{"kind":"full","data":"","labels":{},"at":1}',
  '{"kind":"full","data":"","tags":[],"labels":null,"at":1}',
  '{"kind":"full","data":"","tags":[],"at":1}',
  '{"kind":"full","data":"","tags":[],"labels":{}}',
  '{"kind":"empty","data":null,"tags":null,"labels":null,"at":null}',
  '{"kind":"empty"}',
];
for (const body of bodies) {
  try {
    const p: Parcel = decodeParcel(JSON.parse(body));
    if (p.kind === "full") {
      const n: number = p.data.length + p.tags.length + Object.keys(p.labels).length + p.at;
      void n;
    }
    console.log("ok " + JSON.stringify(p));
  } catch (e) {
    console.log("refused " + (e as Error).message);
  }
}
`})
	want := strings.Join([]string{
		`ok {"kind":"full","data":"","tags":[],"labels":{},"at":1}`,
		`refused $.parcel.data: expected string, got null`,
		`refused $.parcel.data: expected string, got undefined`,
		`refused $.parcel.tags: expected array, got null`,
		`refused $.parcel.tags: expected array, got undefined`,
		`refused $.parcel.labels: expected object, got null`,
		`refused $.parcel.labels: expected object, got undefined`,
		`refused $.parcel.at: expected a JSON value, got undefined`,
		`ok {"kind":"empty"}`,
		`ok {"kind":"empty"}`,
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
