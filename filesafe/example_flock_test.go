//go:build darwin || dragonfly || freebsd || illumos || linux || netbsd || openbsd

package filesafe_test

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"

	"github.com/cplieger/go/filesafe"
)

func ExampleAcquireLease() {
	dir, _ := os.MkdirTemp("", "example")
	defer os.RemoveAll(dir)
	ctx := context.Background()
	counter := filepath.Join(dir, "counter")

	// The data lives in its own file, written with WriteFile; the lease is on
	// counter.lock beside it, so two processes incrementing never lose an update.
	increment := func() error {
		lease, err := filesafe.AcquireLease(ctx, counter+".lock")
		if err != nil {
			return err
		}
		defer lease.Close()

		data, err := filesafe.ReadBounded(ctx, counter, 64)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		n, _ := strconv.Atoi(string(data))
		_, err = filesafe.WriteFile(ctx, counter, []byte(strconv.Itoa(n+1)), filesafe.WithMode(0o600))
		return err
	}

	for range 3 {
		if err := increment(); err != nil {
			fmt.Println(err)
			return
		}
	}
	data, _ := os.ReadFile(counter)
	fmt.Println(string(data))
	// Output: 3
}
