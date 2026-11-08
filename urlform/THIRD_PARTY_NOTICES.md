# Third-party notices

One file here carries third-party content: the conformance corpus `testdata/whatwg-fixtures.json`, 17 of whose 29 rows derive from the web-platform-tests URL test data. That project's license is reproduced below.

The classifier itself is written against the WHATWG URL Standard and Go's `net/url`. A specification and the standard library owe no attribution of this kind, so no other upstream is named here.

## web-platform-tests

<https://github.com/web-platform-tests/wpt>

The rows marked `"source": "wpt"` take their input string and their `wpt` field, the browser truth each row pins against, from `url/resources/urltestdata.json` at commit `181476aa16e8`. The corpus records that pin in its own `provenance` field (`testdata/whatwg-fixtures.json:2`), and `fixtures_test.go:64` is the test that reads it. The urlform facts beside each input are this library's own, vetted by hand against the WHATWG reading rather than regenerated from the upstream corpus; the rows marked `"source": "hand"` carry no upstream content at all.

```text
# The 3-Clause BSD License

Copyright © web-platform-tests contributors

Redistribution and use in source and binary forms, with or without modification, are permitted provided that the following conditions are met:

1. Redistributions of source code must retain the above copyright notice, this list of conditions and the following disclaimer.
2. Redistributions in binary form must reproduce the above copyright notice, this list of conditions and the following disclaimer in the documentation and/or other materials provided with the distribution.
3. Neither the name of the copyright holder nor the names of its contributors may be used to endorse or promote products derived from this software without specific prior written permission.

THIS SOFTWARE IS PROVIDED BY THE COPYRIGHT HOLDERS AND CONTRIBUTORS "AS IS" AND ANY EXPRESS OR IMPLIED WARRANTIES, INCLUDING, BUT NOT LIMITED TO, THE IMPLIED WARRANTIES OF MERCHANTABILITY AND FITNESS FOR A PARTICULAR PURPOSE ARE DISCLAIMED. IN NO EVENT SHALL THE COPYRIGHT HOLDER OR CONTRIBUTORS BE LIABLE FOR ANY DIRECT, INDIRECT, INCIDENTAL, SPECIAL, EXEMPLARY, OR CONSEQUENTIAL DAMAGES (INCLUDING, BUT NOT LIMITED TO, PROCUREMENT OF SUBSTITUTE GOODS OR SERVICES; LOSS OF USE, DATA, OR PROFITS; OR BUSINESS INTERRUPTION) HOWEVER CAUSED AND ON ANY THEORY OF LIABILITY, WHETHER IN CONTRACT, STRICT LIABILITY, OR TORT (INCLUDING NEGLIGENCE OR OTHERWISE) ARISING IN ANY WAY OUT OF THE USE OF THIS SOFTWARE, EVEN IF ADVISED OF THE POSSIBILITY OF SUCH DAMAGE.
```
