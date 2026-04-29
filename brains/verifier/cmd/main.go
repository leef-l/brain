package main

import (
	"log"

	"github.com/leef-l/brain/sdk/shared"
)

func main() {
	learner := NewVerifierBrainLearner()
	if err := shared.RunBrainWithLearner("verifier", learner); err != nil {
		log.Fatal(err)
	}
}
