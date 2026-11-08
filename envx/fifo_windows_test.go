//go:build windows

package envx

import "errors"

// mkfifoForTest reports that Windows has no FIFO to make.
func mkfifoForTest(string) error { return errors.New("no FIFOs on Windows") }
