package main

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/leef-l/brain/sdk/executionpolicy"
)

type filePolicyInput = executionpolicy.FilePolicySpec
type filePolicy = executionpolicy.FilePolicy

func parseFilePolicyJSON(raw string) (*filePolicyInput, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}

	cfg := &filePolicyInput{}
	if err := json.Unmarshal([]byte(raw), cfg); err != nil {
		return nil, fmt.Errorf("parse file_policy_json: %w", err)
	}
	return cfg, nil
}

func newFilePolicy(root string, input *filePolicyInput) (*filePolicy, error) {
	return executionpolicy.NewFilePolicy(root, input)
}

func applyFilePolicy(env *executionEnvironment, input *filePolicyInput) error {
	if env == nil {
		return nil
	}
	policy, err := newFilePolicy(env.workdir, input)
	if err != nil {
		return err
	}
	env.filePolicy = policy
	if input != nil {
		spec := *input
		spec.AllowRead = append([]string(nil), input.AllowRead...)
		spec.AllowCreate = append([]string(nil), input.AllowCreate...)
		spec.AllowEdit = append([]string(nil), input.AllowEdit...)
		spec.AllowDelete = append([]string(nil), input.AllowDelete...)
		spec.Deny = append([]string(nil), input.Deny...)
		if input.AllowCommands != nil {
			v := *input.AllowCommands
			spec.AllowCommands = &v
		}
		if input.AllowDelegate != nil {
			v := *input.AllowDelegate
			spec.AllowDelegate = &v
		}
		env.filePolicySpec = &spec
	} else {
		env.filePolicySpec = nil
	}
	return nil
}

func resolveFilePolicyInput(cfg *brainConfig, input *filePolicyInput) *filePolicyInput {
	if input != nil {
		return input
	}
	if cfg == nil || cfg.FilePolicy == nil {
		return nil
	}
	spec := *cfg.FilePolicy
	spec.AllowRead = append([]string(nil), cfg.FilePolicy.AllowRead...)
	spec.AllowCreate = append([]string(nil), cfg.FilePolicy.AllowCreate...)
	spec.AllowEdit = append([]string(nil), cfg.FilePolicy.AllowEdit...)
	spec.AllowDelete = append([]string(nil), cfg.FilePolicy.AllowDelete...)
	spec.Deny = append([]string(nil), cfg.FilePolicy.Deny...)
	if cfg.FilePolicy.AllowCommands != nil {
		v := *cfg.FilePolicy.AllowCommands
		spec.AllowCommands = &v
	}
	if cfg.FilePolicy.AllowDelegate != nil {
		v := *cfg.FilePolicy.AllowDelegate
		spec.AllowDelegate = &v
	}
	return &spec
}
