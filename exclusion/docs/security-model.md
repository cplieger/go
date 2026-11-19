# Security model

This page covers what exclusion guarantees about the patterns and messages it handles, for a developer exposing the rules to the person running the app or checking what the matches row exposes.

## No pattern can stall a count

A pattern is RE2, compiled with Go's `regexp`, whose matching runs in time linear in the length of the value it reads. There is no backtracking, so no pattern, however written, takes exponential time on a crafted value. A list holds at most 100 rules of at most 8 conditions, and a pattern at most 200 characters, so the work of one match is bounded by the list's bounds times the length of the item's values.

The matches row runs on every draft change, so this bound is what keeps a person typing a pattern from holding the server.

## What reaches the page from a refused pattern

The message of a refused pattern quotes the pattern, which the person typed. It is passed through textsafe before it is answered: each control, formatting or line-break character becomes a space, so the message is one line, and it is cut to at most 256 bytes. The page receives it as text in a JSON field error, never as markup.

## Who can count

The matches row admits whoever your settings read admits, through the `Access` you pass to `Endpoint`. A count tells its caller how many stored items match a rule, which is as much as the settings page already shows that person.
