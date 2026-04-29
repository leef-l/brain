package main

import (
	"log"

	"github.com/leef-l/brain/sdk/shared"
)

func main() {
	learner := NewCodeBrainLearner()
	if err := shared.RunBrainWithLearner("code", learner); err != nil {
		log.Fatal(err)
	}
}
