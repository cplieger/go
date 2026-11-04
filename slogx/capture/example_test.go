package capture_test

import (
	"fmt"

	"github.com/cplieger/go/slogx/capture"
)

func ExampleNew() {
	logger, rec := capture.New()

	logger.Warn("disk almost full", "pct", 92)

	fmt.Println(rec.Contains("disk almost full"))
	fmt.Println(rec.Len())
	// Output:
	// true
	// 1
}
