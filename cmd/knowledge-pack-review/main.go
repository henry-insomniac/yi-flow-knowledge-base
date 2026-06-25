package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"yi-flow/knowledge-base/internal/packreview"
)

func main() {
	manifestPath := flag.String("manifest", "", "path to manifest.json")
	packagePath := flag.String("package", "", "path to knowledge-pack.zip")
	goldenPath := flag.String("golden", "", "path to golden questions json")
	sampleSize := flag.Int("sample-size", 30, "number of chunks to sample")
	questionSize := flag.Int("question-size", 20, "number of golden questions to include")
	flag.Parse()

	if *manifestPath == "" || *packagePath == "" || *goldenPath == "" {
		fmt.Fprintln(os.Stderr, "usage: knowledge-pack-review -manifest manifest.json -package knowledge-pack.zip -golden golden.json [-sample-size 30] [-question-size 20]")
		os.Exit(2)
	}

	report, err := packreview.BuildFiles(*manifestPath, *packagePath, *goldenPath, packreview.Options{
		SampleSize:         *sampleSize,
		GoldenQuestionSize: *questionSize,
	})
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if encodeErr := encoder.Encode(report); encodeErr != nil {
		fmt.Fprintf(os.Stderr, "encode review report: %v\n", encodeErr)
		os.Exit(1)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		os.Exit(1)
	}
}
