package yamlenv_test

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/cplieger/go/yamlenv"
	"go.yaml.in/yaml/v4"
)

func ExampleLoad() {
	type config struct {
		APIKey string `yaml:"api_key"`
		Region string `yaml:"region"`
	}
	data := []byte("api_key: ${APP_KEY}\nregion: ${APP_REGION}\n")
	allow := func(name string) bool { return strings.HasPrefix(name, "APP_") }
	// A test or a sandboxed caller supplies its own variables; without the
	// option, Load reads the process environment.
	vars := map[string]string{"APP_KEY": "s3cret"}
	lookup := func(name string) (string, bool) {
		v, ok := vars[name]
		return v, ok
	}

	var cfg config
	unresolved, err := yamlenv.Load(data, &cfg, allow, yamlenv.WithLookupEnv(lookup))
	if err != nil {
		fmt.Println("error:", err)
		return
	}
	fmt.Println(cfg.APIKey, cfg.Region)
	fmt.Println("unresolved:", unresolved)
	// Output:
	// s3cret ${APP_REGION}
	// unresolved: [APP_REGION]
}

func ExampleExpandWith() {
	var root yaml.Node
	if err := yaml.Unmarshal([]byte("password: ${APP_PASS}\nhost: ${APP_HOST}\n"), &root); err != nil {
		fmt.Println("error:", err)
		return
	}
	allow := func(name string) bool { return strings.HasPrefix(name, "APP_") }
	// A ${...} inside a substituted value is neither expanded nor reported.
	lookup := func(name string) (string, bool) {
		if name == "APP_PASS" {
			return "pa${ss", true
		}
		return "", false
	}

	unresolved := yamlenv.ExpandWith(&root, allow, lookup)
	var out struct {
		Password string `yaml:"password"`
		Host     string `yaml:"host"`
	}
	if err := root.Decode(&out); err != nil {
		fmt.Println("error:", err)
		return
	}
	fmt.Println(out.Password, out.Host)
	fmt.Println("unresolved:", unresolved)
	// Output:
	// pa${ss ${APP_HOST}
	// unresolved: [APP_HOST]
}

// portError is an app's own config error; its text never repeats the value.
type portError struct{ field string }

func (e *portError) Error() string { return e.field + ": must be a port number from 1 to 65535" }

// port is a config field type that validates itself.
type port struct{ n int }

func (p *port) UnmarshalYAML(value *yaml.Node) error {
	n, err := strconv.Atoi(value.Value)
	if err != nil || n < 1 || n > 65535 {
		return &portError{field: "port"}
	}
	p.n = n
	return nil
}

func ExampleWithErrorPassthrough() {
	var cfg struct {
		Port    port `yaml:"port"`
		Workers int  `yaml:"workers"`
	}
	allow := func(name string) bool { return strings.HasPrefix(name, "APP_") }
	lookup := func(string) (string, bool) { return "hunter2", true }
	// Claim the app's own error type, whose text the app controls.
	claim := func(err error) bool {
		_, ok := errors.AsType[*portError](err)
		return ok
	}

	_, err := yamlenv.Load([]byte("port: ${APP_PORT}\n"), &cfg, allow,
		yamlenv.WithLookupEnv(lookup), yamlenv.WithErrorPassthrough(claim))
	fmt.Println(err)
	// go-yaml's own error is not claimed, so it is sanitized.
	_, err = yamlenv.Load([]byte("port: 8080\nworkers: ${APP_WORKERS}\n"), &cfg, allow,
		yamlenv.WithLookupEnv(lookup), yamlenv.WithErrorPassthrough(claim))
	fmt.Println(err)
	fmt.Println(cfg.Port.n, cfg.Workers)
	// Output:
	// port: must be a port number from 1 to 65535
	// decode errors: line 2, column 10: cannot construct !!str <redacted> into int
	// 8080 0
}
