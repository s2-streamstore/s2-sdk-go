// Command specsync checks hand-written package s2 docs and structure against
// the OpenAPI spec. It intentionally does not compare Go type or kind
// compatibility, pointer-ness, or required-ness.
package main

import (
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
)

func main() {
	configPath := flag.String("config", "internal/specsync/config.json", "path to specsync config")
	flag.Parse()
	if err := run(*configPath); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(configPath string) error {
	cfg, err := loadConfig(configPath)
	if err != nil {
		return err
	}
	spec, err := loadSpec(cfg.Spec)
	if err != nil {
		return err
	}
	goSource, err := loadGoSource(cfg.GoPackageDir)
	if err != nil {
		return err
	}
	findings := check(cfg, spec, goSource)
	for index, finding := range findings {
		if index > 0 {
			fmt.Println()
		}
		fmt.Println(finding.String())
		if finding.Spec != "" {
			fmt.Printf("  spec: %s\n", finding.Spec)
		}
		if finding.Go != "" {
			fmt.Printf("  go: %s\n", finding.Go)
		}
	}
	printSummary(findings)
	if len(findings) > 0 {
		return fmt.Errorf("specsync found %d issue(s)", len(findings))
	}
	return nil
}

func printSummary(findings []Finding) {
	counts := make(map[string]int)
	for _, finding := range findings {
		counts[finding.Kind]++
	}
	kinds := make([]string, 0, len(counts))
	for kind := range counts {
		kinds = append(kinds, kind)
	}
	sort.Strings(kinds)
	if len(kinds) == 0 {
		fmt.Println("Summary: no findings.")
		return
	}
	parts := make([]string, 0, len(kinds))
	for _, kind := range kinds {
		parts = append(parts, fmt.Sprintf("%d %s", counts[kind], kind))
	}
	fmt.Printf("Summary: %s (%d total).\n", strings.Join(parts, ", "), len(findings))
}
