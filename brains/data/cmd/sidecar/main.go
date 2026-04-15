// Command brain-data-sidecar is the DataBrain sidecar binary.
//
// It starts a complete DataBrain pipeline and exposes
// market-data query tools via the sidecar stdio JSON-RPC protocol.
// The Kernel launches this as a child process through BrainRegistration.
//
// Configuration is read from the path in DATA_CONFIG env var,
// or falls back to defaults.
//
// See: 36-数据大脑设计.md §13.
package main

import (
	datasidecar "github.com/leef-l/brain/brains/data/sidecar"
)

func main() {
	datasidecar.Main()
}
