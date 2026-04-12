package kernel

import (
	"context"

	"github.com/leef-l/brain/agent"
)

// BrainRunner is the Kernel-side abstraction that launches and stops brain
// sidecars. A Runner implementation knows how to locate a sidecar binary
// (fork/exec, IPC, or in-process), perform the initialize handshake defined
// in 20-协议规格.md §5, and return an agent.Agent handle.
//
// See 02-BrainKernel设计.md §12.5 for the runner decisions (sidecar topology,
// LLM access modes, credential passing).
type BrainRunner interface {
	// Start launches a brain sidecar of the given kind and blocks until it
	// completes the `initialize` handshake. The returned Agent is ready for
	// the Kernel to dispatch work to.
	//
	// The descriptor is the one the Kernel expects the sidecar to declare;
	// the runner may use it as a sanity check on the handshake response.
	Start(ctx context.Context, kind agent.Kind, desc agent.Descriptor) (agent.Agent, error)

	// Stop gracefully shuts down the named brain. The sidecar receives a
	// `$/shutdown` notification and is given a grace period to flush trace
	// events before being killed.
	Stop(ctx context.Context, kind agent.Kind) error
}
