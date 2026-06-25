package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"yi-flow/knowledge-base/internal/packaudit"
)

func main() {
	manifestPath := flag.String("manifest", "", "path to manifest.json")
	packagePath := flag.String("package", "", "path to knowledge-pack.zip")
	flag.Parse()

	if *manifestPath == "" || *packagePath == "" {
		fmt.Fprintln(os.Stderr, "usage: knowledge-pack-audit -manifest manifest.json -package knowledge-pack.zip")
		os.Exit(2)
	}

	report, err := packaudit.AuditFiles(*manifestPath, *packagePath)
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if encodeErr := encoder.Encode(report); encodeErr != nil {
		fmt.Fprintf(os.Stderr, "encode audit report: %v\n", encodeErr)
		os.Exit(1)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		os.Exit(1)
	}
}
