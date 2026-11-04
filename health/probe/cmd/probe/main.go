// Command probe is a standalone HTTP liveness probe for containers
// without a shell. It GETs each URL argument and exits 0 when all
// answer 2xx within the shared timeout, 1 when any fails, 2 on usage
// errors. Install it into a distroless/scratch image and wire it as
// the Docker HEALTHCHECK:
//
//	HEALTHCHECK CMD ["/probe", "http://127.0.0.1:2019/config/"]
//
// Multiple URLs probe multiple surfaces in one run (all must pass):
//
//	["/probe", "http://127.0.0.1:80/health", "http://127.0.0.1:2019/config/"]
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/cplieger/go/health/probe"
)

func main() {
	timeout := flag.Duration("timeout", probe.DefaultTimeout,
		"total wall-clock budget for all probes")
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(),
			"usage: %s [-timeout d] url [url ...]\n", os.Args[0])
		flag.PrintDefaults()
	}
	flag.Parse()
	if flag.NArg() == 0 {
		flag.Usage()
		os.Exit(2)
	}
	probe.Run(*timeout, flag.Args()...)
}
