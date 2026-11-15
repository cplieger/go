// Package gen generates chartdata's TypeScript contract, contract/frame.ts,
// and the deadset edges that pair it with the Go wire types, from
// chartdata.WireTypes and chartdata.WireEnums. It is a module of its own, so
// wiregen and its dependencies stay out of every chartdata importer's module
// graph.
package gen

import (
	"context"

	"github.com/cplieger/go/chartdata"
	"github.com/cplieger/go/wiregen/v2"
)

// TypesFile is the name of the generated TypeScript types file.
const TypesFile = "frame.ts"

// Generate writes chartdata's wire types as TypeScript to tsDir, as
// TypesFile, beside wiregen's decoders and validators, and the deadset edges
// file at edges, whose directory must hold tsDir. root is the chartdata
// module directory, the target deadset analyses.
func Generate(ctx context.Context, root, tsDir, edges string) error {
	var r wiregen.Registry
	for _, t := range chartdata.WireTypes() {
		r.Types = append(r.Types, wiregen.TypeOf(t))
	}
	for _, t := range chartdata.WireEnums() {
		r.Enums = append(r.Enums, wiregen.EnumRef{Type: wiregen.TypeOf(t)})
	}
	return r.Generate(ctx,
		wiregen.WithTSDir(tsDir),
		wiregen.WithTypesFile(TypesFile),
		wiregen.WithRequireJSONTags(true),
		wiregen.WithDeadsetEdges(root),
		wiregen.WithDeadsetEdgesFile(edges))
}
