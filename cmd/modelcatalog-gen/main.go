// Command modelcatalog-gen regenerates internal/modelcatalog/catalog.json
// from the YAML source files in internal/modelcatalog/data. Run it from the
// repository root; the golden test in internal/modelcatalog fails in CI when
// the committed artifact drifts from the sources.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/keakon/chord/internal/modelcatalog/gen"
)

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fail(err)
	}
}

func run(args []string, stdout io.Writer) error {
	flags := flag.NewFlagSet("modelcatalog-gen", flag.ContinueOnError)
	dir := flags.String("dir", "internal/modelcatalog/data", "catalog source directory")
	out := flags.String("out", "internal/modelcatalog/catalog.json", "generated artifact path")
	check := flags.Bool("check", false, "verify the artifact matches the sources without writing")
	validate := flags.Bool("validate", false, "validate verified sources and candidates without writing an artifact")
	revision := flags.String("revision", "", "require a release tag matching the catalog version")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || (*validate && *check) {
		return fmt.Errorf("use -validate or -check, with no positional arguments")
	}

	generated, err := gen.Generate(*dir)
	if err != nil {
		return err
	}
	if *revision != "" {
		var identity struct {
			Version string `json:"version"`
		}
		if err := json.Unmarshal(generated, &identity); err != nil {
			return err
		}
		if *revision != "v"+identity.Version {
			return fmt.Errorf("release tag %q does not match catalog version %q", *revision, identity.Version)
		}
	}
	if *validate {
		fmt.Fprintf(stdout, "catalog sources and candidates valid: %s\n", *dir)
		return nil
	}
	if *check {
		current, err := os.ReadFile(*out)
		if err != nil {
			return fmt.Errorf("read %s: %w", *out, err)
		}
		if !bytes.Equal(current, generated) {
			return fmt.Errorf("%s is stale; run `go run ./cmd/modelcatalog-gen` to regenerate", *out)
		}
		fmt.Fprintf(stdout, "catalog artifact up to date: %s\n", *out)
		return nil
	}
	if err := os.WriteFile(*out, generated, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", *out, err)
	}
	fmt.Fprintf(stdout, "generated %s\n", *out)
	return nil
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "modelcatalog-gen:", err)
	os.Exit(1)
}
