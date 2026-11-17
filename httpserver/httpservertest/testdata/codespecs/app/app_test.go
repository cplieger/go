package app

import "testing"

func TestIgnored(t *testing.T) {
	hs.WriteError(nil, nil, 400, "test_only_code", "a test file is never read")
}
