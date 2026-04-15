// Command brain-quant-sidecar is the QuantBrain sidecar binary.
//
// It starts a complete DataBrain + QuantBrain pipeline and exposes
// account-query tools via the sidecar stdio JSON-RPC protocol.
// The Kernel launches this as a child process through BrainRegistration.
//
// Configuration is read from the path in QUANT_CONFIG env var,
// or falls back to paper-trading defaults.
//
// See: 37-量化大脑设计.md §13, 35-量化系统三脑架构总览.md §5.
package main

import (
	quantsidecar "github.com/leef-l/brain/brains/quant/sidecar"
)

func main() {
	quantsidecar.Main()
}
