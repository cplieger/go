package envx_test

import (
	"cmp"
	"fmt"
	"os"
	"time"

	"github.com/cplieger/go/envx"
)

func Example() {
	os.Setenv("APP_LISTEN", ":9090")
	os.Setenv("APP_DEBUG", "yes")
	defer os.Unsetenv("APP_LISTEN")
	defer os.Unsetenv("APP_DEBUG")

	// String takes no fallback, so there is no argument order to get wrong;
	// cmp.Or composes the default, and treats an empty value as absent
	// exactly as the parsing getters do.
	addr := cmp.Or(envx.String("APP_LISTEN"), ":8080")
	debug := envx.Bool("APP_DEBUG", false)
	interval := envx.Duration("APP_INTERVAL", 6*time.Hour)

	fmt.Println(addr, debug, interval)
	// Output: :9090 true 6h0m0s
}

func ExampleRequire() {
	// Collect every missing variable, then fail once.
	var missing []error
	if _, err := envx.Require("APP_TOKEN_UNSET_A"); err != nil {
		missing = append(missing, err)
	}
	if _, err := envx.Require("APP_TOKEN_UNSET_B"); err != nil {
		missing = append(missing, err)
	}
	fmt.Println(len(missing), "missing")
	// Output: 2 missing
}

func ExampleSource() {
	// The testable-main shape: main hands run its real environment
	// (run(os.Args, os.Getenv)), tests hand it a fake. Source adopts the
	// injected getter without giving that seam up; the zero Source reads the
	// process environment.
	run := func(getenv func(string) string) (int, time.Duration) {
		env := envx.Source{Get: getenv}
		return env.Int("APP_RETRIES", 3), env.Duration("APP_INTERVAL", 6*time.Hour)
	}

	fake := map[string]string{"APP_RETRIES": "5"}
	retries, interval := run(func(name string) string { return fake[name] })
	fmt.Println(retries, interval)
	// Output: 5 6h0m0s
}

func ExampleIsBlankSecretFilePath() {
	// The shape compose interpolation of an undefined variable produces: the
	// pointer is present, but it names no file.
	os.Setenv("APP_API_KEY_FILE", "")
	os.Setenv("APP_API_KEY", "inline-key")
	defer os.Unsetenv("APP_API_KEY_FILE")
	defer os.Unsetenv("APP_API_KEY")

	// Resolution cannot see it: the inline value wins as if the operator had
	// never written APP_API_KEY_FILE.
	_, source, _ := envx.SecretWithSource("APP_API_KEY")
	fmt.Println("resolved from:", source)
	fmt.Println("blank secret pointer:", envx.IsBlankSecretFilePath("APP_API_KEY"))
	// Output:
	// resolved from: env
	// blank secret pointer: true
}
