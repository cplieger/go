# Non-goals

This page explains why envx leaves out each feature in the README's unsupported list, for a developer who expected one of them. They are deliberate choices, and none of them is planned.

## Struct tags and reflection-based loading

envx is a getter library, not a config framework. An app builds its config struct from explicit calls, so every default and every key name can be found with a text search.

## Loading `.env` files

The container runtime, such as compose or Kubernetes, sets the environment. A second loader would raise the question of which source wins, and no app using envx needs one.

## Float, slice and map getters

No app using envx reads these types from the environment. A getter is added when a real app needs it.

## Prefix namespacing

A helper such as `WithPrefix("APP_")` saves a few characters per call and makes key names harder to find. Without it, every key name appears in the code exactly as the operator writes it.

## Panic on a missing variable

envx has no `MustX` variants. `Require` returns an error, so startup can report every missing variable at once instead of stopping at the first. A value never causes a panic. The one panic in the package is the key name check below.

## Accepting malformed key names

A `Key` outside `[A-Za-z_][A-Za-z0-9_]*` panics at its first read. That is usually at startup, and later for a key the app reads lazily. Key names are literals written in the code, and the panic turns a typo into a failure that happens every time. A warning instead would let a mistyped key read as unset forever and return a default nobody chose.
