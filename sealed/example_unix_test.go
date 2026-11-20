//go:build darwin || dragonfly || freebsd || illumos || linux || netbsd || openbsd

package sealed_test

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/cplieger/go/sealed"
)

func ExampleOpenKeyFile() {
	dir, err := os.MkdirTemp("", "sealed-example-")
	if err != nil {
		log.Fatal(err)
	}
	defer os.RemoveAll(dir)
	keyPath := filepath.Join(dir, "keys", "sealing.key") // the app's KEY_PATH

	const tokenPurpose sealed.Purpose = "example/github-token"
	ctx := context.Background()
	keys, err := sealed.OpenKeyFile(ctx, keyPath)
	if err != nil {
		// ErrUnsafeKeyFile, ErrKeyFileFormat, ErrKeyFileInUse: the app's key
		// outage, never a reason to create a new key.
		log.Fatal(err)
	}
	defer keys.Close() // last, after the stores that use its Sealer
	s := keys.Sealer() // handed to every store now

	_, err = s.SealAtRest(tokenPurpose, []byte("example token"))
	fmt.Println(errors.Is(err, sealed.ErrNoKey))
	if s.KeyIDs().Current == "" { // and the app's store holds no sealed value
		if err := keys.Create(ctx); err != nil {
			log.Fatal(err)
		}
	}

	stored, err := s.SealAtRest(tokenPurpose, []byte("example token"))
	if err != nil {
		log.Fatal(err)
	}
	token, current, err := s.OpenAtRest(tokenPurpose, stored)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(string(token), current)
	// Output:
	// true
	// example token true
}

func ExampleKeyFile_Rotate() {
	dir, err := os.MkdirTemp("", "sealed-example-")
	if err != nil {
		log.Fatal(err)
	}
	defer os.RemoveAll(dir)
	ctx := context.Background()
	keys, err := sealed.OpenKeyFile(ctx, filepath.Join(dir, "sealing.key"))
	if err != nil {
		log.Fatal(err)
	}
	defer keys.Close()
	if err := keys.Create(ctx); err != nil {
		log.Fatal(err)
	}
	s := keys.Sealer()

	const tokenPurpose sealed.Purpose = "example/github-token"
	old, err := s.SealAtRest(tokenPurpose, []byte("example token"))
	if err != nil {
		log.Fatal(err)
	}
	store := []string{old}

	// The app's admin command, served by the process that holds keys.
	rotateKeys := func(ctx context.Context, keys *sealed.KeyFile) error {
		return keys.Rotate(ctx) // ErrRotationPending: retire the previous key first
	}
	if err := rotateKeys(ctx, keys); err != nil {
		log.Fatal(err)
	}

	// The housekeeper seals again every value that opens with current false.
	for i, v := range store {
		token, current, err := s.OpenAtRest(tokenPurpose, v)
		if err != nil {
			log.Fatal(err)
		}
		fmt.Println("current:", current)
		if !current {
			if store[i], err = s.SealAtRest(tokenPurpose, token); err != nil {
				log.Fatal(err)
			}
		}
	}

	// Once every value is sealed again, a person's step retires the old key.
	if err := keys.Retire(ctx); err != nil {
		log.Fatal(err)
	}
	token, current, err := s.OpenAtRest(tokenPurpose, store[0])
	fmt.Println(string(token), current, err)
	_, _, err = s.OpenAtRest(tokenPurpose, old)
	fmt.Println(errors.Is(err, sealed.ErrInvalid))
	// Output:
	// current: false
	// example token true <nil>
	// true
}

func ExampleSealer_Subkey() {
	dir, err := os.MkdirTemp("", "sealed-example-")
	if err != nil {
		log.Fatal(err)
	}
	defer os.RemoveAll(dir)
	ctx := context.Background()
	keys, err := sealed.OpenKeyFile(ctx, filepath.Join(dir, "sealing.key"))
	if err != nil {
		log.Fatal(err)
	}
	defer keys.Close()
	if err := keys.Create(ctx); err != nil {
		log.Fatal(err)
	}
	s := keys.Sealer()

	// A library that takes one key, such as a page cursor sealer, gets a
	// Subkey under a label its own package declares.
	const cursorLabel sealed.Label = "example/cursor"
	const cursorPurpose sealed.Purpose = "example/page"
	cursorKey, err := s.Subkey(cursorLabel)
	if err != nil {
		log.Fatal(err)
	}
	cursor, err := sealed.New(cursorKey).Seal(cursorPurpose, []byte("page 2"), time.Hour)
	if err != nil {
		log.Fatal(err)
	}
	again, err := s.Subkey(cursorLabel)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(again == cursorKey)

	if err := keys.Rotate(ctx); err != nil {
		log.Fatal(err)
	}
	// The Subkey is a copy that keeps the old key: take it again after a
	// rotation and rebuild what holds it.
	newKey, err := s.Subkey(cursorLabel)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(newKey == cursorKey)
	_, _, err = sealed.New(newKey).Open(cursorPurpose, cursor)
	fmt.Println(errors.Is(err, sealed.ErrInvalid))
	// Output:
	// true
	// false
	// true
}
