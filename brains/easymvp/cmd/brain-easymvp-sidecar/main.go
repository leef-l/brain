package main

import (
	"fmt"
	"os"

	"github.com/leef-l/brain/brains/easymvp"
	"github.com/leef-l/brain/sdk/sidecar"
)

func main() {
	listen := ""
	for i, arg := range os.Args[1:] {
		if arg == "--listen" && i+1 < len(os.Args[1:]) {
			listen = os.Args[i+2]
		}
	}

	handler := easymvp.NewHandler()
	if listen != "" {
		fmt.Fprintf(os.Stderr, "brain-easymvp sidecar v1.0.0 listening on %s (network mode)\n", listen)
		if err := sidecar.ListenAndServe(listen, handler); err != nil {
			fmt.Fprintf(os.Stderr, "brain-easymvp: %v\n", err)
			os.Exit(1)
		}
	} else {
		fmt.Fprintf(os.Stderr, "brain-easymvp sidecar v1.0.0 starting (stdio mode)\n")
		if err := sidecar.Run(handler); err != nil {
			fmt.Fprintf(os.Stderr, "brain-easymvp: %v\n", err)
			os.Exit(1)
		}
	}
}
