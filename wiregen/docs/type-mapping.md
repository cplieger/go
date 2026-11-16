# Type mapping

This page states how wiregen turns each Go type into TypeScript, a decoder and an OpenAPI schema. It is for a developer checking that the generated code matches what their server sends. wiregen reads the registered packages from source with `go/packages`, `go/types` and `go/ast`, and its field walk follows `encoding/json`.

## Field rules

- A registered type is a struct or a named map (see [Named maps](#named-maps)), or an instantiation of a generic one (see [Generic types](#generic-types)). Register an enum with `StringEnum` or `TextEnum`, and a type wiregen cannot read with a mapping.
- Doc comments on registered structs and their fields become `/** ... */` JSDoc on the generated interfaces and union cases. The text is go/ast's (`CommentGroup.Text`), so tool directive lines are left out by Go's own rule (`//go:generate`, `//nolint:...`, `//deadset:ignore ...`, `//line`, `//export`), and so are the space forms `// nolint` and `// #nosec` that linters also read. A `//` paragraph break becomes a blank JSDoc line, and a field's JSDoc is indented with the field.
- Unexported fields are skipped, as `encoding/json` skips them. An embedded struct, exported or not, is flattened with the promotion rules of `encoding/json`: the shallowest field wins, a tagged field beats an untagged one at the same depth, and a field reached through two sibling embeds at the same depth is dropped as ambiguous.
- A field with no `json` tag takes the name `encoding/json` gives it, its Go name. With `WithRequireJSONTags(true)` such a field is a generation error naming its path. A tag name may hold letters, digits, spaces and the punctuation `!#$%&()*+-./:;<=>?@[]^_{|}~`.
- A field `encoding/json` may leave out is optional in TypeScript, `key?: T`: a field tagged `omitzero`, a field tagged `omitempty` whose type `omitempty` can find empty (never a struct), and a field promoted through an embedded pointer, which is skipped when nil. Every other field is required.
- A required pointer field and a pointer element of a slice or map are `T | null`, as `encoding/json` writes a nil one as `null`. An optional pointer field stays `key?: T`, its `null` read as absent. A nil slice, map or `[]byte` with no pointer above it reads as empty, so its type admits no `null`.
- A string or string enum field tagged `wiregen:"const=<value>"` is typed as that one value, so a TypeScript union of types that each pin their own value on one field narrows on that field. A field `Type FrameType` tagged `json:"type" wiregen:"const=bins"` becomes `type: "bins"`; its decoder accepts `"bins"` alone, its schema is `{"type": "string", "const": "bins"}` and its arbitrary draws `"bins"`. The tag states what the field holds, and the Go code that builds the value must set it, so test that it does.
- A map key is typed `string`. An integer key, one that is not an `encoding.TextMarshaler`, is decimal text on the wire, so the schema gives it `propertyNames` with a decimal pattern, which does not carry the key kind's range.
- `time.Time` maps to `string`, and `json.Number` to `number`. A slice of `byte`, or of any other `uint8` type without a marshaler, maps to `string`, because JSON encodes it as base64.
- `json.RawMessage` (and `jsontext.Value`, which it aliases from Go 1.27) maps to `unknown`. It is the one opaque type wiregen lets cross the wire.
- A type implementing `encoding.TextMarshaler` with a value receiver maps to `string`, a fixed-size array included. Marshalers are found by their method signatures, promoted methods included, as `encoding/json` finds them.
- `json:",string"` types the field as `string`, as `encoding/json` writes the value's JSON encoding as text. It applies to a boolean, number or string with no marshaler, under at most one pointer. The decoder and the schema's `pattern` hold the text to that encoding's grammar: `"5"` for an integer, `"true"` for a boolean, `"\"a\""` for a string.
- A map is `{ [key: string]: V }` in the TypeScript, because JSON object keys are strings.
- Nested collections are checked at every level, and a malformed inner array or map throws with the element's path.

## What Generate refuses

`Generate` refuses a field whose JSON shape it cannot check, so nothing typed `unknown` reaches the client by accident. It collects every such field and reports them together, each as `importpath.Type.Field: reason`, before writing anything:

- a `time.Duration`, which `encoding/json` writes as integer nanoseconds. Send an `int64` of milliseconds instead, named with an `_ms` suffix.
- an interface or `any`, named or not. Use `json.RawMessage`.
- an inline anonymous struct. Declare and register a named struct.
- a named struct that is neither registered nor mapped.
- a type implementing `json.Marshaler`, whose output can take any shape, unless it is mapped.
- a type with a pointer-receiver `MarshalText`, which `encoding/json` calls only for addressable values, so one field encodes two ways.
- a registered struct implementing `json.Marshaler` or `encoding.TextMarshaler`, through its own methods or promoted ones such as an embedded `time.Time`'s, since `encoding/json` writes the marshaler's output and not its fields. Map it instead.
- the `,string` option on a field it does not apply to, which `encoding/json` ignores, on a mapped type, whose mapping states a wire form the option rewrites, or on a bounded type, whose bounds do not describe the text.
- a tag name with a character outside the set above, such as a backslash, a quote or `€`, which the Go releases' `encoding/json` implementations read differently.
- a fixed-size array, a channel, a function, a complex number or an `unsafe.Pointer`.
- a map whose key is not a string, an integer or an `encoding.TextMarshaler`.
- a bound method wiregen cannot read, or one that does not fit the field (see below).
- a `wiregen` tag that is not `const=<value>` or names no value; a const on a field that is not a string or string enum, on a pointer, on a field `encoding/json` can leave out or on a bounded type; a const its enum has no member for; a `wiregen` tag on a flattened embed, which pins no field; and a const on a union's tag, whose value varies by case.

In a type an endpoint's `Request`, `Params` or `Query` carries, or one those reach, it also refuses a field `encoding/json` cannot decode into:

- a field promoted through an embedded pointer to an unexported struct, which `encoding/json` cannot allocate.
- a type implementing `json.Marshaler` without `json.Unmarshaler`, or `encoding.TextMarshaler` without `encoding.TextUnmarshaler`, mapped or not. A `TextEnum` in a request needs `UnmarshalText` on its pointer.
- a map key implementing `encoding.TextMarshaler` without `encoding.TextUnmarshaler`, unless it is a string, which is written and read as its string either way.
- a map key written in one form and read back in another: an integer key with `MarshalText` and no `UnmarshalText`, an integer key with only a pointer `UnmarshalText`, and a string key with both, which is written as its string but read through `UnmarshalText`.
- a type with no `MarshalJSON` or `MarshalText` whose pointer has `UnmarshalText` and no `UnmarshalJSON`, unless its underlying type is a string or a mapping replaces its wire form: `encoding/json` reads it only from a JSON string, through `UnmarshalText`, which need not read what it writes, whether an object, array, number, boolean, a `[]byte`'s base64 or a `,string` field's text.

The rules apply at any depth of slices and maps.

## Enums

An enum is registered by its type, and its members are the constants of exactly that type declared in the type's own package, in source order. The package is loaded whether or not a registered struct lives there.

- `StringEnum[T]()` registers a string type, whose members are the constants' values.
- `TextEnum[T]()` registers a `uint8` type with a `MarshalText` method, whose members are the names `MarshalText` gives the constants. A constant `MarshalText` refuses, such as an unnamed zero, is no member.

A string enum with a marshaler, or an integer enum with `MarshalJSON`, is refused, since `encoding/json` would write something other than its members.

Repeated values count once, two values with one wire name are refused, and an enum with no members is refused. Enum and struct names share one namespace. A field of an enum type that is not registered is a plain `string`. The TypeScript is a string union, the decoder checks membership with `reqOneOf`, and the OpenAPI component is `{"type": "string", "enum": [...]}`.

## Bounds

A field type can declare its own limits through methods with a value receiver whose body is one `return` of constants. wiregen reads the constants from source without running the code, and the server's binder can call the same methods, so each limit has one source.

| Method | On | Schema | Generated check |
| --- | --- | --- | --- |
| `Range() (lo, hi int64)` | an integer type | `minimum`, `maximum` | `checkRange`: an integer from lo to hi |
| `MaxBytes() int` | a string type | `x-max-bytes`, plus `maxLength` at the same number | `checkBytes`: at most that many UTF-8 bytes |
| `Pattern() string` | a string type | `pattern` | `checkPattern`: an ECMA-262 match, unanchored like JSON Schema's |
| `Count() (lo, hi int)` | a slice type | `minItems`, `maxItems` | `checkCount`: lo to hi items |

A pattern is checked by Go's `regexp` in the binder and by ECMA-262 in the decoder, so write it in the syntax both share: no inline flags such as `(?i)`, no POSIX classes such as `[[:alpha:]]`, and `\p{L}` with braces. `Generate` refuses a pattern Go's `regexp` does not compile.

`maxLength` counts code points, so a generic JSON Schema validator checks only a coarse form of `MaxBytes`. The generated decoder checks the exact byte count. A slice whose `Count` lower bound is 1 or more refuses `null`, in the decoder and in the schema, because `encoding/json` decodes `null` to an empty slice below the bound. Under a pointer, `null` is the nil pointer and stays allowed.

A pointer-receiver bound method, one whose body is not a single return of constants, an empty range, or a method on the wrong kind of type, such as `Range` on a string, is refused with the field's path.

## Null and presence

The decoders follow what `encoding/json` writes:

- An optional field reads an absent key or `null` as absent. A mapped decoder is never called on either.
- A required field is refused when its key is absent, and the error names the field's path. A field mapped with a `Decoder` leaves that check to the `Decoder`.
- A required pointer field reads `null` as `null`, its key still refused absent.
- A required slice or map with no pointer above it reads `null` as `[]` or `{}`, and a required `[]byte` reads it as `""`, unless a `Count` lower bound refuses it.
- An array response, an endpoint or extra success with `Array` set, reads a `null` body as `[]`, as `encoding/json` writes a handler's nil slice.
- A pointer element of a slice or map reads `null` as `null`. Any other nil element reads as empty, as a required field does.
- A `json.RawMessage` field, required or optional, passes `null` through as data. A required field mapped with no `Decoder` passes it through too.
- A decoder reads only the object's own properties: a key such as `constructor` that every JavaScript object inherits is absent unless the JSON sends it, and a `__proto__` key, in a map or as a field, is kept as an own property.

The OpenAPI schemas say the same thing in each direction:

- A response field is required unless `encoding/json` can leave it out: `omitzero`, `omitempty` on a type it can find empty, or a field promoted through an embedded pointer. A request field is also optional when it is a pointer, as the decoder leaves it nil. The TypeScript interface is one type for both directions and follows the response rule, so a request body built in TypeScript sets each pointer key with no omit option, to `null` for none.
- A response field is nullable, `{"type": [T, "null"]}` or an `anyOf` with `null` around a `$ref`, when `encoding/json` can write `null` for it: a nil pointer or collection no omit option drops, or anything nil under a non-nil pointer, such as a `*[]string` holding a nil slice or a `**T` holding a nil `*T`. A request field is nullable when it is a pointer, slice, map or `[]byte`, since `null` decodes to nil. A `Count` lower bound of 1 or more makes a collection with no pointer above it never nullable.
- An array response's schema admits `null` beside the array, for the same nil slice.
- A type whose two schemas differ gets two components, the request one suffixed `Input`, and the document references each in its own direction. A registered type no endpoint reaches is documented in its response form, and so is every type its fields name.

## Unions

`Registry.Unions` declares a registered struct a union tagged by one of its fields:

```go
wiregen.Union{
    Type: wiregen.TypeRef[wire.Timing](),
    Tag:  "kind",
    Cases: map[string][]string{
        "identity": {},
        "shift":    {"ms"},
        "linear":   {"scale", "offset_ms"},
    },
}
```

`Tag` and the field lists are wire names. The tag is a required string or enum field that is never null and carries no `wiregen` const, and an enum tag's cases are exactly its members. Each case lists the other fields it allows, every field belongs to some case, and a listed field keeps its own presence rule unless `Required` lists it for that case (below). A field some case leaves out must be tagged `omitempty` or `omitzero`, since otherwise `encoding/json` writes it in every case and the decoder and the case schema would refuse that output. Cases are emitted in the enum's order, or by value for a string tag.

- The types file carries one object type per case, one member per line under its field's JSDoc, the tag's included:

  ```ts
  export type Timing =
    | {
        /** Kind names the case. */
        kind: "identity";
      }
    | {
        /** Kind names the case. */
        kind: "shift";
        ms?: number;
      }
    | ...
  ```

- `decodeTiming` reads the tag, refuses a present field another case owns, and decodes the case's fields.
- The document carries a `oneOf` with a `discriminator` over one closed component per case, `Timing.shift` say, each with `additionalProperties: false`.

A union whose tag also selects the type of a `json.RawMessage` field names it in `Payload`, and maps every case to that field's type in `PayloadTypes`, the zero `Type` for a case with no payload:

```go
wiregen.Union{
    Type:  wiregen.TypeRef[wire.Command](),
    Tag:   "type",
    Cases: map[string][]string{"create": {}, "rename": {"chat_id"}, "close": {"chat_id"}},
    Payload: "payload",
    PayloadTypes: map[string]wiregen.Type{
        "create": wiregen.TypeRef[wire.CreateChat](),
        "rename": wiregen.TypeRef[wire.Rename](),
        "close":  {},
    },
}
```

The payload field is listed in no case. It belongs to each case with a payload type, with its own omit options, and is refused in the others. A `json.RawMessage` can be `null` on the wire in two ways: encoding/json writes a nil one as `null` unless an omit option drops it, and writes one holding the bytes `null`, such as a request `null` decoded into it, whatever the options. So the payload is a pointer field: optional with an omit option, `null` read as absent, and required and nullable with none. Each payload type is a struct or a named map, as every registered type is, and joins the registry's types. `Generate` refuses a `Payload` that names no field, a payload field that is not a `json.RawMessage`, `PayloadTypes` set without `Payload`, `PayloadTypes` keys that are not exactly the case values, a case that lists the payload field, and a union where no case has a payload type.

- The types file types `payload` per case, `payload?: CreateChat` in the create case, or `payload: CreateChat | null` with no omit option.
- The decoder decodes `payload` with that case's decoder, reads `null` as absent, or as `null` with no omit option, and refuses a present payload in a case that has none.
- In each case's component, `payload` is that type's `$ref` or `null`, or absent.

A field several cases share has one presence rule, so when one case always sends it and another may not, the field is tagged `omitempty` or `omitzero` and `Required` names the cases that always send it, keyed by case value:

```go
wiregen.Union{
    Type:  wiregen.TypeRef[wire.Checked](),
    Tag:   "state",
    Cases: map[string][]string{"updated": {"stored"}, "unchanged": {"stored"}, "error": {"failure"}},
    Required: map[string][]string{
        "updated": {"stored"},
        "error":   {"failure"},
    },
}
```

In a case that lists it, the field is present and not `null`, in both directions. A `json.RawMessage` other than the payload is only present, since its `null` is data, while a listed payload is decoded as its case's type and so is never `null`. The case's TS member has no `?`, the decoder refuses the field absent, and the case's component lists it in `required`. The decoder also refuses `null` and the schema drops it, except for that `json.RawMessage`, whose schema stays the open schema. A field whose type has a `Mapping` with a `Decoder` hands absence and `null` to that decoder; one with no `Decoder` is only checked present. Every other case keeps the field's own rule, `stored?:` in the unchanged case above. The Go side does not change, so the server must write the field in every case that lists it. `Generate` refuses a `Required` key that is no case, a field the case does not carry, the payload in a case with no payload type, a field listed twice, the tag or a field every case already sends and never as `null`, and a union whose every case requires a field that leads back to it, which no finite value fills.

## Named maps

A registered named map type, `type Settings map[string]json.RawMessage`, is a body of its own: an open JSON object whose keys are not known in advance. It can be listed in `Types`, or named as an endpoint's `Request` or `Response`, an extra success's `Response`, an event's data, a union case's payload or the `ErrorEnvelope`. Its key and value follow the rules of a map field, so the value is any type wiregen reads, `json.RawMessage` included, and the key is a string, an integer or an `encoding.TextMarshaler`.

- The types file carries `export type Settings = { [key: string]: unknown };`, with the type's doc comment.
- The decoder refuses a body that is not an object and decodes each value with the value's decoder. `null` reads as `{}`, as for a map field, because encoding/json writes a nil map as `null`.
- The OpenAPI component is `{"type": "object", "additionalProperties": <the value's schema>}`, with a separate request form when the value's schema differs by direction. A body, field or element of the map admits `null` where encoding/json writes or reads a nil map as `null`. An `omitempty` field of the map admits it only in a request form, since encoding/json never writes it as `null`; an `omitempty` pointer to the map admits it in a response too, because a non-nil pointer to a nil map is written as `null`.
- A field whose type is a registered named map references it by name, as a field of a registered struct does: its TypeScript alias, its decoder, its `$ref` and its arbitrary, with a map field's null rule. That is also how a named map reaches itself, `type Tree map[string]Tree`. An unregistered named map or slice that reaches itself is refused, since it would be planned inline without end: register the map, or route a slice's cycle through a registered struct or named map. Two nested instances of one unregistered generic map or slice type, such as `G[G[int]]`, are two types and plan inline.

A union or an endpoint's `Params` or `Query` cannot be a named map, because both need a struct's fields.

## Generic types

An instantiation of a generic struct or named map is registered as any other type is, `wiregen.TypeRef[wire.List[wire.Peer]]()`, and an endpoint, event, union or `ErrorEnvelope` can name it the same way. A hand-written `Type` spells it as `TypeRef` does, `Type{PkgPath: "example.com/wire", Name: "List[example.com/wire.Peer]"}`. The generic declaration alone, `List`, is refused, since only an instantiation has a JSON shape. Each instantiation generates one TypeScript type, one decoder and one OpenAPI component, however many fields and endpoints name it.

Its name is the generic type's name followed by each type argument's name, in order, joined by `_`:

| Go type | Name |
| --- | --- |
| `List[Peer]` | `List_Peer` |
| `List[int]` | `List_int` |
| `Pair[string, Peer]` | `Pair_string_Peer` |
| `List[List[Peer]]` | `List_List_Peer` |

A registered type argument contributes its bare Go name, a builtin its predeclared name, with `byte` and `rune` spelled `uint8` and `int32` as Go's `reflect` spells them, and an instantiation its own name. That name stands wherever a type's bare Go name does: the TypeScript type, the decoder `decodeList_Peer`, the components `List_Peer` and `List_PeerInput`, the decoder's error path `$.list_peer`, the deadset edge `wiregen/List_Peer`, and the keys of `TSNameOverride` and `PathNameOverride`. `TSNameOverride["List_Peer"]` gives the TypeScript type another name.

The name joins Go names with `_`, so it is a valid TypeScript identifier and edge identifier. An OpenAPI component name must also hold only ASCII letters, digits, `.`, `-` and `_`, so a non-ASCII Go name in it, as in `List[Maß]`, is refused when OpenAPI files are written ([Identifiers](#identifiers)). The name depends only on Go names, so every run derives the same one. The `_` separator keeps it apart from an ordinary Go name such as `ListPeer`, and since Go names rarely hold `_`, two types rarely derive one name. When they do, `Generate` refuses both, naming each Go type: two instantiations such as `Pair[A_B, C]` and `Pair[A, B_C]`, or an instantiation and a registered type or enum whose name it takes, such as a struct named `List_Peer` in any package.

- Each type argument is a builtin (`string`, `bool`, an integer or a float type) or a registered type: a struct, a named map, an enum or a registered instantiation. Any other argument, such as `*Peer`, `[]Peer`, `any`, `time.Time`, an unregistered struct or an unregistered instantiation, is refused with an error naming it; declare and register a named type in its place.
- An instantiation's fields follow the [field rules](#field-rules) with each type parameter read as its argument: in `Slot[Peer]`, a `Val *T` field with no omit option is `val: Peer | null`, and a `wiregen:"const=slot"` field is `kind: "slot"`.
- A field whose type is a registered instantiation references it by name, as a field of a registered struct does. A field whose type is an unregistered instantiation of a generic struct is refused, and one of a generic named map or slice is planned inline, as [Named maps](#named-maps) describes.
- A `Mappings` key names one instantiation or every instantiation of a generic type ([Mappings](#mappings)).
- An instantiation that reaches itself, such as `Tree[int]` with a `Kids []Tree[T]` field, references its own type, `kids: Tree_int[]`, as a registered struct that reaches itself does.
- The type's and fields' doc comments come from the generic declaration, and its deadset edges pair each instantiation with that declaration: `wiregen/List_Peer` provides `List` and `wiregen/List_Peer/Items` provides `List.Items`, as deadset records a generic type's fields on its declaration.

A struct that embeds an instantiation, `type Page struct { List[Peer]; Total int }`, flattens the instantiation's fields as it flattens any embedded struct, and the instantiation itself needs no registration.

Register the instantiation itself when it is the whole body, as a cursor page `List[T]` is for an endpoint that returns one page, and declare a struct that embeds it only to add fields beside it. The generic type then stays the one contract every list shares, no wrapper struct exists only to give a generated type its name, and, for a generic declared in the app's own tree, every list's edges pair with the one generic declaration. A generic from another module, such as httpwire's `List`, gets no edges, as [Deadset edges file](generated-files.md#deadset-edges-file) describes.

## Mappings

`Registry.Mappings` gives a Go type the consumer's own wire form, in place of wiregen's reading of it. It is keyed by full `importpath.Type`, such as `".../uuid.UUID"`:

```go
r.Mappings = map[string]wiregen.Mapping{
    "example.com/app/id.UUID": {
        TS:      "string",
        Decoder: "reqStr",
        Schema:  map[string]any{"type": "string", "format": "uuid"},
    },
}
```

- `TS` is the TypeScript type expression.
- `Decoder` is a TypeScript expression called as `(obj, key, path)` that returns the field's value or throws. An empty one casts the value unchecked, `null` included, once a required field is checked present. The validators module's helpers, such as `reqStr`, are in scope.
- `Schema` is the value's JSON Schema, required when an OpenAPI document is written. A `$ref` in it must be a string whose JSON pointer ([RFC 6901](https://www.rfc-editor.org/rfc/rfc6901)) names something in the document, such as `#/components/schemas/Item`. `Generate` refuses one that does not, or that points into another document.

A key may name a type alias or the type it resolves to, and both work. `go/types` resolves an alias past its own name, and the standard library re-homes types behind aliases: `json.RawMessage` is an alias for `encoding/json/jsontext.Value` from Go 1.27, so either key keeps a mapping in force across a toolchain upgrade. Set one of the two: when both are set, the alias's key applies and the other, reached by no other field, is refused.

A key may name one instantiation of a generic type, spelled as `Generate`'s errors spell it, `"example.com/wire.List[example.com/wire.Peer]"`, or the generic type, `"example.com/wire.List"`, which maps every instantiation no key of the first form names.

## Identifiers

Every generated identifier is valid TypeScript. The struct and enum name overrides are cleaned to a valid identifier, with a safe fallback when nothing valid is left.

The generated modules import one another, so every name they bind shares one namespace: each type's and enum's TypeScript name, each decoder (`decode<Name>`), each enum's member list (`<NAME>S`), each arbitrary, each endpoint's bindings, the validators module's exports, and the fixed exports of the events, client and arbitraries modules. `Generate` refuses two of them with one name before writing anything. Rename one through `TSNameOverride` or `EnumTSNameOverride`. The generated code names every global it uses through `globalThis`, such as `globalThis.Object`, so a type may take a global's name. A keyword, a predefined type name such as `string`, `undefined` or `globalThis` takes the suffix `Val` as a type name and is refused as an endpoint name.

An OpenAPI component name holds only ASCII letters, digits, `.`, `-` and `_`, as [OpenAPI's Components Object](https://spec.openapis.org/oas/v3.1.0#components-object) requires, so when OpenAPI files are written `Generate` refuses a type or enum whose TypeScript name holds any other character, such as `Maß`. Rename it through `TSNameOverride` or `EnumTSNameOverride`.

A JSON key that is not a valid identifier, such as `content-type`, is emitted as a quoted property and read with bracket access, `out["content-type"]`.

## Unsupported by design

| Feature | Reason |
| --- | --- |
| A generic type itself | Only an instantiation has a JSON shape, so wiregen generates one type per registered instantiation, never a generic TypeScript type. See [Generic types](#generic-types). |
| `tstype` struct tag hints | `Mappings` does the same job at the registry level. |
| Interface-typed unions | Strict mode refuses an interface field. Declare a tagged struct and register it in `Unions`. |
