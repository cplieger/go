package masked_test

import (
	"fmt"

	"github.com/cplieger/go/masked"
)

func ExampleSecret() {
	token := masked.NewSecret("s3cr3t-token")
	fmt.Println(token)
	fmt.Printf("%#v %x\n", token, token)
	fmt.Println(token.Reveal())
	fmt.Println(token.Equal(masked.NewSecret("s3cr3t-token")))
	// Output:
	// [redacted]
	// [redacted] [redacted]
	// s3cr3t-token
	// true
}

func ExampleSecret_Key() {
	seen := map[[32]byte]bool{}
	for _, text := range []string{"alpha-token", "beta-token", "alpha-token"} {
		k := masked.NewSecret(text).Key()
		fmt.Println(seen[k])
		seen[k] = true
	}
	// Output:
	// false
	// false
	// true
}
