// Command exchange-executor is the execution sidecar for OrderIntent -> ExecutionResult.
//
// The binary is intentionally thin: it reads an order intent from stdin or a
// flag, executes it with the in-memory Paper backend, and prints a JSON result.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/leef-l/brain/internal/execution"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	fs := flag.NewFlagSet("exchange-executor", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	intentJSON := fs.String("intent", "", "order intent JSON payload")
	intentFile := fs.String("intent-file", "", "path to an order intent JSON file")
	backendName := fs.String("backend", "paper", "execution backend: paper")
	markPrice := fs.Float64("mark-price", 0, "reference mark price for paper execution")
	slippageBps := fs.Float64("slippage-bps", 0, "paper slippage in basis points")
	feeBps := fs.Float64("fee-bps", 5, "paper fee in basis points")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "exchange-executor: unexpected argument %q\n", fs.Arg(0))
		return 2
	}

	payload, err := readIntentPayload(*intentJSON, *intentFile, os.Stdin)
	if err != nil {
		fmt.Fprintf(os.Stderr, "exchange-executor: %v\n", err)
		return 1
	}

	var intent execution.OrderIntent
	if err := json.Unmarshal(payload, &intent); err != nil {
		fmt.Fprintf(os.Stderr, "exchange-executor: parse intent: %v\n", err)
		return 1
	}
	if intent.Timestamp == 0 {
		intent.Timestamp = time.Now().UnixMilli()
	}

	client, err := buildClient(*backendName, *markPrice, *slippageBps, *feeBps)
	if err != nil {
		fmt.Fprintf(os.Stderr, "exchange-executor: %v\n", err)
		return 1
	}

	result, err := client.Execute(context.Background(), intent)
	if err != nil {
		fmt.Fprintf(os.Stderr, "exchange-executor: execute: %v\n", err)
		return 1
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(result); err != nil {
		fmt.Fprintf(os.Stderr, "exchange-executor: encode result: %v\n", err)
		return 1
	}
	return 0
}

func buildClient(name string, markPrice, slippageBps, feeBps float64) (*execution.Client, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "", "paper":
		var provider execution.PriceProvider
		if markPrice > 0 {
			provider = func(context.Context, string) (float64, bool) {
				return markPrice, true
			}
		}
		backend := execution.NewPaperBackend(
			execution.WithPaperPriceProvider(provider),
			execution.WithPaperSlippageBps(slippageBps),
			execution.WithPaperFeeBps(feeBps),
		)
		return execution.NewClient(backend), nil
	default:
		return nil, fmt.Errorf("unsupported backend %q", name)
	}
}

func readIntentPayload(intentJSON, intentFile string, stdin io.Reader) ([]byte, error) {
	switch {
	case strings.TrimSpace(intentJSON) != "":
		return []byte(intentJSON), nil
	case strings.TrimSpace(intentFile) != "":
		return os.ReadFile(intentFile)
	default:
		return io.ReadAll(stdin)
	}
}
