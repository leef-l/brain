package main

import (
	"github.com/leef-l/brain/cmd/brain/config"
	"github.com/leef-l/brain/cmd/brain/provider"
)

type providerSession = provider.Session

var (
	openConfiguredProvider = provider.OpenConfigured
	openMockProvider       = provider.OpenMock
)

func resolveProviderConfigWithInput(cfg *brainConfig, brainKind string, input *modelConfigInput, flagProvider, flagKey, flagURL, flagModel string) (resolvedProvider, error) {
	return provider.ResolveWithInput(cfg, brainKind, input, flagProvider, flagKey, flagURL, flagModel)
}

var _ func(*config.Config, string, *config.ModelConfigInput, string, string, string, string) (provider.Session, error) = openConfiguredProvider
