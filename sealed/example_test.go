package sealed_test

import (
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/cplieger/go/sealed"
)

func ExampleSealer_Seal() {
	const sessionPurpose sealed.Purpose = "example/session"
	s := sealed.NewEphemeral() // an app's key file Sealer in production

	payload := []byte("session 42")
	cookie, err := s.Seal(sessionPurpose, payload, 15*time.Minute)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(len(cookie) == sealed.ExpiringLen(len(payload)))

	// After 15 minutes Open answers ErrExpired.
	got, current, err := s.Open(sessionPurpose, cookie)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(string(got), current)

	_, _, err = s.Open("example/other", cookie)
	fmt.Println(errors.Is(err, sealed.ErrInvalid))
	// Output:
	// true
	// session 42 true
	// true
}
