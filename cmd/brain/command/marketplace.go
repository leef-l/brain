package command

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/leef-l/brain/sdk/cli"
	"github.com/leef-l/brain/sdk/kernel"
)

// MarketplaceDeps 是 marketplace 子命令所需的依赖。
type MarketplaceDeps struct {
	// IndexPath 返回 index.json 路径，默认 ~/.brain/marketplace/index.json
	IndexPath string
}

// runBrainSearch 实现 `brain brain search <query>` 命令。
func runBrainSearch(args []string, deps MarketplaceDeps) int {
	fs := flag.NewFlagSet("brain brain search", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	jsonOut := fs.Bool("json", false, "output JSON format")
	kind := fs.String("kind", "", "filter by brain kind")
	capability := fs.String("capability", "", "filter by capability")
	runtimeType := fs.String("runtime", "", "filter by runtime type (native/mcp-backed/hybrid)")
	publisher := fs.String("publisher", "", "filter by publisher")

	if err := fs.Parse(args); err != nil {
		return cli.ExitUsage
	}

	query := strings.Join(fs.Args(), " ")

	mp := kernel.NewLocalMarketplace(deps.IndexPath)
	ctx := context.Background()

	var results []kernel.MarketplaceEntry
	var err error

	// 如果有 filter 标志，优先用 List；否则用 Search
	hasFilter := *kind != "" || *capability != "" || *runtimeType != "" || *publisher != ""
	if hasFilter && query == "" {
		results, err = mp.List(ctx, kernel.MarketplaceFilter{
			Kind:        *kind,
			Capability:  *capability,
			RuntimeType: *runtimeType,
			Publisher:   *publisher,
		})
	} else {
		// 先 search，再 filter
		results, err = mp.Search(ctx, query)
		if err == nil && hasFilter {
			results = applyFilter(results, kernel.MarketplaceFilter{
				Kind:        *kind,
				Capability:  *capability,
				RuntimeType: *runtimeType,
				Publisher:   *publisher,
			})
		}
	}

	if err != nil {
		fmt.Fprintf(os.Stderr, "brain brain search: %v\n", err)
		return cli.ExitSoftware
	}

	if *jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(map[string]interface{}{
			"query":   query,
			"results": results,
			"total":   len(results),
		})
	} else {
		if len(results) == 0 {
			fmt.Println("No packages found.")
			fmt.Fprintf(os.Stdout, "Index: %s\n", mp.IndexPath())
			return cli.ExitOK
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintf(tw, "PACKAGE\tNAME\tKIND\tVERSION\tRUNTIME\tPUBLISHER\n")
		for _, e := range results {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n",
				e.PackageID, e.Name, e.Kind, e.Version, e.RuntimeType, e.Publisher)
		}
		tw.Flush()
		fmt.Fprintf(os.Stdout, "\n%d package(s) found.\n", len(results))
	}

	return cli.ExitOK
}

// runBrainInfo 实现 `brain brain info <package-id>` 命令。
func runBrainInfo(args []string, deps MarketplaceDeps) int {
	fs := flag.NewFlagSet("brain brain info", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	jsonOut := fs.Bool("json", false, "output JSON format")
	if err := fs.Parse(args); err != nil {
		return cli.ExitUsage
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "Usage: brain brain info <package-id>")
		return cli.ExitUsage
	}

	packageID := fs.Arg(0)
	mp := kernel.NewLocalMarketplace(deps.IndexPath)
	ctx := context.Background()

	entry, err := mp.Get(ctx, packageID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "brain brain info: %v\n", err)
		return cli.ExitNotFound
	}

	if *jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(entry)
	} else {
		printEntryDetail(entry)
	}

	return cli.ExitOK
}

// printEntryDetail 人类可读地打印 package 详情。
func printEntryDetail(e *kernel.MarketplaceEntry) {
	fmt.Printf("Package:      %s\n", e.PackageID)
	fmt.Printf("Name:         %s\n", e.Name)
	if e.Description != "" {
		fmt.Printf("Description:  %s\n", e.Description)
	}
	fmt.Printf("Kind:         %s\n", e.Kind)
	fmt.Printf("Version:      %s\n", e.Version)
	fmt.Printf("Publisher:    %s\n", e.Publisher)
	fmt.Printf("Runtime:      %s\n", e.RuntimeType)
	if len(e.Capabilities) > 0 {
		fmt.Printf("Capabilities: %s\n", strings.Join(e.Capabilities, ", "))
	}
	if e.Downloads > 0 {
		fmt.Printf("Downloads:    %d\n", e.Downloads)
	}
	if e.Rating > 0 {
		fmt.Printf("Rating:       %.1f\n", e.Rating)
	}
	if e.Compatible {
		fmt.Println("Compatible:   yes")
	} else {
		fmt.Println("Compatible:   no")
	}
	if e.LicenseRequired {
		fmt.Println("License:      required")
	}
	if e.Edition != "" {
		fmt.Printf("Edition:      %s\n", e.Edition)
	}
	if e.Channel != "" {
		fmt.Printf("Channel:      %s\n", e.Channel)
	}
}

// applyFilter 在 search 结果上再做 filter 筛选。
func applyFilter(entries []kernel.MarketplaceEntry, f kernel.MarketplaceFilter) []kernel.MarketplaceEntry {
	var out []kernel.MarketplaceEntry
	for _, e := range entries {
		if f.Kind != "" && !strings.EqualFold(e.Kind, f.Kind) {
			continue
		}
		if f.RuntimeType != "" && !strings.EqualFold(e.RuntimeType, f.RuntimeType) {
			continue
		}
		if f.Publisher != "" && !strings.EqualFold(e.Publisher, f.Publisher) {
			continue
		}
		if f.Capability != "" {
			found := false
			capLower := strings.ToLower(f.Capability)
			for _, c := range e.Capabilities {
				if strings.ToLower(c) == capLower {
					found = true
					break
				}
			}
			if !found {
				continue
			}
		}
		out = append(out, e)
	}
	return out
}
