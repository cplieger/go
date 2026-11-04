package yamlenv_test

import (
	"fmt"
	"strings"

	"github.com/cplieger/go/yamlenv"
	"go.yaml.in/yaml/v3"
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
