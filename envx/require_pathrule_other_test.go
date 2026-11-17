//go:build !windows

// Windows strips a path's trailing periods (https://learn.microsoft.com/en-us/dotnet/standard/io/file-path-formats#trim-characters), so these names cannot exist there.

package envx

import "testing"

// TestSecret_trailing_dot_names_are_ordinary_files pins the path rule's accepted
// half for names whose dots end the name or make up all of it.
func TestSecret_trailing_dot_names_are_ordinary_files(t *testing.T) {
	assertDottedNamesAreRead(t, t.TempDir(), "...", "....", "token..")
}
