// Package central provides the internal Central Brain service skeleton.
//
// The package is intentionally split into small subpackages so the future
// brain-central entrypoint can wire review, control, and reviewrun without
// carrying transport or framework concerns into the core logic.
package central
