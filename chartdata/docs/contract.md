# The generated contract

This page is for a developer whose TypeScript reads chartdata's frames, in a chart library or in an app's own client. It covers the generated types, the golden fixtures, how both are kept current and how an app generates its own decoders for the same types.

## What is generated

The Go wire types are the one source of the frames' shape. [wiregen](../../wiregen) turns them into TypeScript, and the constructors write a golden JSON fixture of each frame type, event kind and refusal rule. Both are committed in this module:

| Path | Holds |
| --- | --- |
| `contract/frame.ts` | The TypeScript interfaces of every type `WireTypes` returns, the twelve frames and their parts, `Read` and `Count`, the union of each string enum `WireEnums` returns, `FrameType`, `FrameAxis`, `FrameEventKind` and `ReadState`, and `FrameEvent` as the discriminated union `WireUnions` declares: one member per kind, holding only that kind's fields. Types only, with no decoder |
| `contract/fixtures/frame-<type>.json` | One frame of each type as the constructors encode it: `series`, `bins`, `categories`, `boxes`, `points`, `grid`, `calendar`, `tree` and `tree-groups`, `flow` and `flow-stages`, `events`, `states` and `value` |
| `contract/fixtures/frame-series-read.json` | A series frame built from a partial read, carrying `read` |
| `contract/fixtures/count-<state>.json` | One `Count` per read state: `complete`, `partial`, `blind`, `unread`, `unsupported` and `excluded` |
| `contract/fixtures/read-<state>.json` | One `Read` per read state, as `ReadOf` writes it beside a figure that is not a `Count` |
| `contract/fixtures/event-<kind>.json` | One event of each kind and shape: `append-column`, `append-bin`, `replace-column`, `replace-bin`, `reset` and `set` |
| `contract/fixtures/refusal-<rule>.json` | `{"frame": ..., "rule": ..., "field": ...}`: a frame as the wire would carry it, which the constructors refuse by `rule` at `field`, one per rule a wire frame can break |
| `contract/fixtures/refusal-read-<case>.json` | One case of rule `read` each: on a frame `{"frame": ..., "rule": "read", "field": ...}`, on a count `{"count": ..., "rule": "read", "field": ...}`, on a read alone `{"read": ..., "rule": "read", "field": ...}`. `refusal-read-complete` and the two `-as-of-year` cases carry what no constructor writes (a frame `read` naming `complete`, a year past 9999), so their `field` is the field a reader refuses |
| `contract/fixtures/limits.json` | The limits table: `maxValues`, `maxRows`, `maxSubRows`, `maxMagnitude`, `maxCalendarDays`, `minEveryMs`, `maxPoints`, `maxLabelSets` and `maxBaselines` |
| `contract/fixtures/duration-edges.json` | `DurationEdges`, in seconds |
| `deadset-edges.json` | The edges pairing each generated field with its Go field, so a dead-code analysis of this module reads the wire fields as used |

A chart library vendors `contract/` at a pinned chartdata tag. Its tests can decode every frame and event fixture and draw it, refuse every refusal fixture by the same rule, and read its ceilings and duration ladder from `limits.json` and `duration-edges.json` instead of keeping copies.

The refusal fixtures cover the rules from `length` to `read`. `declaration` and `option` concern the Go call, not the data, so no wire frame can break them.

## Keeping it current

Two tests are the drift check, and CI runs both on every change:

- `TestTheFixturesAreCurrent`, in this module, rebuilds every fixture through the constructors and fails when a committed file differs, is missing or is not one the constructors produce.
- `TestTheContractIsGenerated`, in the nested `internal/gen` module, runs wiregen over `WireTypes`, `WireEnums` and `WireUnions` and fails when `contract/frame.ts` or `deadset-edges.json` differs.

`TestDecodersReadEveryFixture`, in `internal/gen` too, runs every frame, count and read fixture through the decoders an app's wiregen run writes, under node, and checks that a read's `as_of` decodes as a string, or `null` before the first whole read, and that a state outside the six is refused.

After changing a wire type or a fixture, regenerate both and commit the result with the change:

```sh
cd chartdata && UPDATE_GOLDEN=1 go test ./ -run TestTheFixturesAreCurrent
cd internal/gen && UPDATE_GOLDEN=1 go test ./...
```

The generator lives in a module of its own, `chartdata/internal/gen`, so wiregen and its dependencies never enter the module graph of an app that imports chartdata. It is under `internal/`, so it is never published as a module of its own.

## Generating an app's own decoders

An app that serves frames from its API generates its own client with wiregen, registering chartdata's types from the dependency beside its own:

```go
r := wiregen.Registry{
	Types:  []wiregen.Type{wiregen.TypeRef[Dashboard]()},
	Events: []wiregen.Event{{Name: "frame", Data: wiregen.TypeRef[chartdata.FrameEvent]()}},
}
for _, t := range chartdata.WireTypes() {
	r.Types = append(r.Types, wiregen.TypeOf(t))
}
for _, t := range chartdata.WireEnums() {
	r.Enums = append(r.Enums, wiregen.EnumRef{Type: wiregen.TypeOf(t)})
}
for _, u := range chartdata.WireUnions() {
	r.Unions = append(r.Unions, wiregen.Union{Type: wiregen.TypeOf(u.Type), Tag: u.Tag, Cases: u.Cases})
}
err := r.Generate(ctx, wiregen.WithTSDir("web/src/wire"))
```

Each chartdata interface, enum and union the app's run writes is the same as `frame.ts`'s, which a test in `internal/gen` checks, so a frame the app's decoder reads passes to the chart unchanged. The `FrameEvent` decoder wiregen writes for the union refuses a field another kind owns, as wiregen [documents for every union](../../wiregen/docs/type-mapping.md#unions). wiregen keys types by their bare name and refuses two of one name, so no chartdata type is named `Event` or another bare name an app is likely to use: a frame's name ends in `Frame`, and every other wire type's starts with `Frame`, but `Count` and `Read`, which an app's own rows carry, and `ReadState`, the enum of `measure`'s read states. An app type of the same name, such as its own `FrameRef`, `Count` or `Read`, is refused by its run and needs another name.
