// Command style prints one CSS file bundled exactly as a webbundle Style
// entry is, with webbundle.BuildStyle, and writes nothing. It exits 0 with the
// stylesheet on standard output, 1 when the build fails, and 2 on a usage
// error:
//
//	go run github.com/cplieger/go/webbundle/cmd/style -src web node_modules/@cplieger/ui/css/system.css
//
// The path is a slash path under -src, which defaults to the working
// directory.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/cplieger/go/webbundle"
)

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("style", flag.ContinueOnError)
	flags.SetOutput(stderr)
	src := flags.String("src", ".", "the static-src root the path is under")
	flags.Usage = func() {
		fmt.Fprintln(stderr, "usage: style [-src dir] path")
		flags.PrintDefaults()
	}
	switch err := flags.Parse(args); {
	case errors.Is(err, flag.ErrHelp):
		return 0
	case err != nil:
		return 2
	}
	if flags.NArg() != 1 {
		flags.Usage()
		return 2
	}
	css, err := webbundle.BuildStyle(webbundle.Stylesheet{Src: *src, Path: flags.Arg(0)})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if _, err := stdout.Write(css); err != nil {
		fmt.Fprintln(stderr, "style:", err)
		return 1
	}
	return 0
}
