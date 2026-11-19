# Non-goals

This page lists what exclusion leaves out on purpose and why, for a developer deciding whether it fits or planning a change. These are design decisions rather than missing features, and a pull request that adds one is declined. To argue for a change, open an issue first.

| Left out | Reason |
| --- | --- |
| Cleaning item values | Only your app knows its source's canonical form (a trimmed tag, a normalized language, a bot login with and without its suffix). `Item` answers canonical values, so matching stays one comparison |
| Checks that know your domain or read two conditions together | A repository's `owner/name` shape or `draft` beside `kind: issue` are your fields' rules. Your settings validator appends them beside `Validate`'s entries |
| Keep lists | A rule only leaves items out. A list that keeps only matching items is a filter, a different setting |
| Storing which items are left out | Your store marks them and leaves them out of every list and count, so a rule change applies when it is saved. `CountFunc` counts over that store |
| Default rules | A default list, such as a tag that opts an item out, is your settings document's default |
| Refusing a repeated rule | Each copy counts the same items, and leaving out an item twice changes nothing |
| Case-insensitive patterns by default | A pattern is case-sensitive like Go's `regexp`. Write `(?i)` to ignore case |
| The rules page | It is `@cplieger/ui`'s `.cp-rules`, which builds the targets from your fields and shows each rule's count |
