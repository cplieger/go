package httpclient_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"time"

	"github.com/cplieger/go/httpclient"
)

func ExampleGetBytes() {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, "hello")
	}))
	defer srv.Close()

	// context.Background() (not t.Context()): an Example has no *testing.T.
	body, err := httpclient.GetBytes(context.Background(), srv.Client(), srv.URL,
		httpclient.WithMaxAttempts(3),
		httpclient.WithBaseDelay(100*time.Millisecond),
	)
	if err != nil {
		fmt.Println("error:", err)
		return
	}
	fmt.Println(string(body))
	// Output: hello
}

func ExampleDo() {
	attempts := 0
	// context.Background() (not t.Context()): an Example has no *testing.T.
	result, err := httpclient.Do(context.Background(), func(_ context.Context) (string, error) {
		attempts++
		if attempts < 2 {
			return "", &httpclient.StatusError{Code: 503}
		}
		return "done", nil
	}, httpclient.WithMaxAttempts(3), httpclient.WithBaseDelay(time.Millisecond), httpclient.WithLabel("example"))
	if err != nil {
		fmt.Println("error:", err)
		return
	}
	fmt.Println(result)
	// Output: done
}
