package main

import (
	"log"

	"github.com/leef-l/brain/sdk/shared"
)

func main() {
	learner := NewFaultBrainLearner()
	if err := shared.RunBrainWithLearner("fault", learner); err != nil {
		log.Fatal(err)
	}
}
