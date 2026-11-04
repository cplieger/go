package slogx_test

import (
	"fmt"
	"log/slog"

	"github.com/cplieger/go/slogx"
)

func ExampleParseLevel() {
	lvl, ok := slogx.ParseLevel("warning", slog.LevelInfo)
	fmt.Println(lvl, ok)

	_, ok = slogx.ParseLevel("banana", slog.LevelInfo)
	fmt.Println(ok)
	// Output:
	// WARN true
	// false
}

func ExampleParseFormat() {
	format, ok := slogx.ParseFormat("JSON", slogx.Text)
	fmt.Println(format == slogx.JSON, ok)

	_, ok = slogx.ParseFormat("yaml", slogx.Text)
	fmt.Println(ok)
	// Output:
	// true true
	// false
}
