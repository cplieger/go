package keylock_test

import (
	"cmp"
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/cplieger/go/keylock"
)

func ExampleSet_Lock() {
	users := keylock.New(cmp.Compare[int])

	unlock, err := users.Lock(context.Background(), 42)
	if err != nil {
		fmt.Println(err)
		return
	}
	defer unlock()

	// A second caller on the same user waits until its context ends, then
	// holds nothing.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	_, err = users.Lock(ctx, 42)
	fmt.Println(err)
	// Output: context deadline exceeded
}

func ExampleSet_LockAll() {
	files := keylock.New(strings.Compare)
	ctx := context.Background()

	// Each writer names the same two files in its own order. LockAll takes
	// them in the Set's order, so neither writer can hold one file while
	// waiting on the other's.
	var wg sync.WaitGroup
	for _, names := range [][]string{{"movie.en.srt", "movie.fr.srt"}, {"movie.fr.srt", "movie.en.srt"}} {
		wg.Go(func() {
			unlock, err := files.LockAll(ctx, names)
			if err != nil {
				fmt.Println(err)
				return
			}
			defer unlock()
		})
	}
	wg.Wait()
	fmt.Println("both writers finished")
	// Output: both writers finished
}
