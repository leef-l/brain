package env

import "github.com/leef-l/brain/sdk/executionpolicy"

// NewFilePolicy creates a FilePolicy from a FilePolicyInput spec.
func NewFilePolicy(root string, input *FilePolicyInput) (*FilePolicy, error) {
	return executionpolicy.NewFilePolicy(root, input)
}

// ApplyFilePolicy applies a file policy to an Environment.
func ApplyFilePolicy(e *Environment, input *FilePolicyInput) error {
	if e == nil {
		return nil
	}
	policy, err := executionpolicy.NewFilePolicy(e.Workdir, input)
	if err != nil {
		return err
	}
	e.FilePolicy = policy
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
		e.FilePolicySpec = &spec
	} else {
		e.FilePolicySpec = nil
	}
	return nil
}

// ResolveFilePolicyInput returns the effective file policy input,
// falling back to config if no explicit input is provided.
func ResolveFilePolicyInput(cfgFilePolicy *FilePolicyInput, input *FilePolicyInput) *FilePolicyInput {
	if input != nil {
		return input
	}
	if cfgFilePolicy == nil {
		return nil
	}
	spec := *cfgFilePolicy
	spec.AllowRead = append([]string(nil), cfgFilePolicy.AllowRead...)
	spec.AllowCreate = append([]string(nil), cfgFilePolicy.AllowCreate...)
	spec.AllowEdit = append([]string(nil), cfgFilePolicy.AllowEdit...)
	spec.AllowDelete = append([]string(nil), cfgFilePolicy.AllowDelete...)
	spec.Deny = append([]string(nil), cfgFilePolicy.Deny...)
	if cfgFilePolicy.AllowCommands != nil {
		v := *cfgFilePolicy.AllowCommands
		spec.AllowCommands = &v
	}
	if cfgFilePolicy.AllowDelegate != nil {
		v := *cfgFilePolicy.AllowDelegate
		spec.AllowDelegate = &v
	}
	return &spec
}
