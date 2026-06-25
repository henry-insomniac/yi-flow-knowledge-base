package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"yi-flow/knowledge-base/internal/packeval"
)

func main() {
	manifestPath := flag.String("manifest", "", "path to manifest.json")
	packagePath := flag.String("package", "", "path to knowledge-pack.zip")
	goldenPath := flag.String("golden", "", "path to golden questions json")
	topK := flag.Int("top-k", 5, "retrieval top k")
	flag.Parse()

	if *manifestPath == "" || *packagePath == "" || *goldenPath == "" {
		fmt.Fprintln(os.Stderr, "usage: knowledge-pack-eval -manifest manifest.json -package knowledge-pack.zip -golden golden.json [-top-k 5]")
		os.Exit(2)
	}

	report, err := packeval.EvaluateFiles(*manifestPath, *packagePath, *goldenPath, packeval.Options{TopK: *topK})
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if encodeErr := encoder.Encode(report); encodeErr != nil {
		fmt.Fprintf(os.Stderr, "encode eval report: %v\n", encodeErr)
		os.Exit(1)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		os.Exit(1)
	}
}
