module github.com/cplieger/go/sse/v2/internal/ssefixture

go 1.27.4

require (
	github.com/cplieger/go/httpserver/v2 v2.0.0-dev.6
	github.com/cplieger/go/sse/v2 v2.0.0-00010101000000-000000000000
)

require (
	github.com/cplieger/go/ssrf v1.1.0 // indirect
	github.com/cplieger/go/textsafe v1.1.0 // indirect
	github.com/cplieger/go/urlform v1.2.0 // indirect
	golang.org/x/net v0.63.0 // indirect
)

replace github.com/cplieger/go/sse/v2 => ../..
