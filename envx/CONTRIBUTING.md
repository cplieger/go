# Contributing to envx

The [shared rules](https://github.com/cplieger/.github/blob/main/CONTRIBUTING.md) for commits, releases, synced files and checks apply here.

## Releases

The root module and `yamlenv` are versioned separately, each from the commits that change its files. A breaking change in a commit that touches both raises both major versions.

Keep a breaking change to the files of the module it breaks. Otherwise the other module also needs a new `/vN` module path before it can release.
