// Command modelcatalog-gen regenerates internal/modelcatalog/catalog.json
// from the YAML source files in internal/modelcatalog/data. Run it from the
// repository root; the golden test in internal/modelcatalog fails in CI when
// the committed artifact drifts from the sources.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"

	"github.com/keakon/chord/internal/modelcatalog/gen"
)

func main() {
	dir := flag.String("dir", "internal/modelcatalog/data", "catalog source directory")
	out := flag.String("out", "internal/modelcatalog/catalog.json", "generated artifact path")
	check := flag.Bool("check", false, "verify the artifact matches the sources without writing")
	flag.Parse()

	generated, err := gen.Generate(*dir)
	if err != nil {
		fail(err)
	}
	if *check {
		current, err := os.ReadFile(*out)
		if err != nil {
			fail(fmt.Errorf("read %s: %w", *out, err))
		}
		if !bytes.Equal(current, generated) {
			fail(fmt.Errorf("%s is stale; run `go run ./cmd/modelcatalog-gen` to regenerate", *out))
		}
		fmt.Printf("catalog artifact up to date: %s\n", *out)
		return
	}
	if err := os.WriteFile(*out, generated, 0o644); err != nil {
		fail(fmt.Errorf("write %s: %w", *out, err))
	}
	fmt.Printf("generated %s\n", *out)
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "modelcatalog-gen:", err)
	os.Exit(1)
}
