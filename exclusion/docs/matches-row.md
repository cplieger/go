# The matches row

This page covers the API row that counts a rule list's matches while the person running your app edits it, for a developer mounting it in an httpwire table and writing its count.

## The row

`Endpoint(name, access)` declares the row and `Binding(name, fields, count)` binds it. Name it `exclusion.matches`, and give it your settings read's `Access`, since it reads what the settings page reads. The row's path is spelled from its name, `/exclusion/matches`, under your table's prefix, so a table with the prefix `/api` serves `POST /api/exclusion/matches`.

`Endpoint` needs no store, so your table and the wire document generated from it build without one. Add `CodeSpecs()` to your catalog with `AddOwned`, so the catalog knows the row's code.

## Request and answer

The body is a rule list, a draft or the saved one:

```json
{"rules": [{"conditions": [{"field": "author", "value": "renovate"}]}]}
```

The answer is one count per rule, in the order of the body's rules:

```json
{"counts": [12]}
```

A body with no rule answers `{"counts": []}`.

The row is a `POST` because a draft list is a body, but it changes nothing. It is not keyed and names no changed resource, since repeating it is harmless. As on every `POST` row, httpwire refuses a cross-origin request, and answers `maintenance` while the API is quieted for writes.

## The count

`CountFunc` is yours: it answers, for the compiled `Set`, how many of the items your store holds each rule matches, one count per rule in order. Count over the store rather than over a fresh read of the source, so that the count the page shows is the count of items a saved rule would leave out. An app whose items fit in memory can answer `s.Count(items)`.

A `CountFunc` error, or a count of another length than the rules, answers `internal_error` and no counts, so the page never shows a short or partial list as if it were exact.

## Refusals

| Code | Status | When |
| --- | --- | --- |
| `exclusion_invalid` | 422 | `Validate` refuses the body's list. The error's fields are the same entries a settings save of that list answers, under `rules` |
| `bad_body` | 400 | The body is not a rule list in the row's schema |
| `unsupported_media_type` | 415 | The body is not sent as `application/json` |
| `body_too_large` | 413 | The body is larger than the row's cap |

`exclusion_invalid` is marked as having had no effect, so a client may correct the list and send it again.

## Body cap

The row accepts a body of up to `MaxBody`, 2 MiB, above the table's 64 KiB default. A list at every bound (100 rules of 8 conditions, each value 200 bytes) can take about 1 MiB once JSON escapes its characters, and the row takes every list a settings save takes.

The settings row that saves the section holding the rules needs the same room: set its `MaxBody` to at least `exclusion.MaxBody`, or it refuses a list at every bound with `body_too_large` before your validator runs.
