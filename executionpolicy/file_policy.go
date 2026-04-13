package executionpolicy

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/leef-l/brain/internal/pathglob"
)

type FilePolicySpec struct {
	AllowRead     []string `json:"allow_read,omitempty"`
	AllowCreate   []string `json:"allow_create,omitempty"`
	AllowEdit     []string `json:"allow_edit,omitempty"`
	AllowDelete   []string `json:"allow_delete,omitempty"`
	Deny          []string `json:"deny,omitempty"`
	AllowCommands *bool    `json:"allow_commands,omitempty"`
	AllowDelegate *bool    `json:"allow_delegate,omitempty"`
}

type ExecutionSpec struct {
	Workdir    string          `json:"workdir,omitempty"`
	FilePolicy *FilePolicySpec `json:"file_policy,omitempty"`
}

type FilePolicy struct {
	root string
	spec FilePolicySpec
}

func NewFilePolicy(root string, spec *FilePolicySpec) (*FilePolicy, error) {
	if spec == nil {
		return nil, nil
	}
	policy := &FilePolicy{
		root: filepath.Clean(root),
		spec: FilePolicySpec{
			AllowRead:     normalizePatterns(spec.AllowRead),
			AllowCreate:   normalizePatterns(spec.AllowCreate),
			AllowEdit:     normalizePatterns(spec.AllowEdit),
			AllowDelete:   normalizePatterns(spec.AllowDelete),
			Deny:          normalizePatterns(spec.Deny),
			AllowCommands: cloneOptionalBool(spec.AllowCommands),
			AllowDelegate: cloneOptionalBool(spec.AllowDelegate),
		},
	}
	for _, patterns := range [][]string{
		policy.spec.AllowRead,
		policy.spec.AllowCreate,
		policy.spec.AllowEdit,
		policy.spec.AllowDelete,
		policy.spec.Deny,
	} {
		for _, pattern := range patterns {
			if err := pathglob.Validate(pattern); err != nil {
				return nil, fmt.Errorf("invalid file policy pattern %q: %w", pattern, err)
			}
		}
	}
	if !policy.Enabled() {
		return nil, nil
	}
	return policy, nil
}

func (p *FilePolicy) Enabled() bool {
	if p == nil {
		return false
	}
	return len(p.spec.AllowRead) > 0 ||
		len(p.spec.AllowCreate) > 0 ||
		len(p.spec.AllowEdit) > 0 ||
		len(p.spec.AllowDelete) > 0 ||
		len(p.spec.Deny) > 0 ||
		p.spec.AllowCommands != nil ||
		p.spec.AllowDelegate != nil
}

func (p *FilePolicy) AllowsCommands() bool {
	if p == nil || !p.Enabled() || p.spec.AllowCommands == nil {
		return true
	}
	return *p.spec.AllowCommands
}

func (p *FilePolicy) AllowsDelegation() bool {
	if p == nil || !p.Enabled() || p.spec.AllowDelegate == nil {
		return true
	}
	return *p.spec.AllowDelegate
}

func (p *FilePolicy) Spec() *FilePolicySpec {
	if p == nil {
		return nil
	}
	spec := p.spec
	spec.AllowRead = append([]string(nil), p.spec.AllowRead...)
	spec.AllowCreate = append([]string(nil), p.spec.AllowCreate...)
	spec.AllowEdit = append([]string(nil), p.spec.AllowEdit...)
	spec.AllowDelete = append([]string(nil), p.spec.AllowDelete...)
	spec.Deny = append([]string(nil), p.spec.Deny...)
	spec.AllowCommands = cloneOptionalBool(p.spec.AllowCommands)
	spec.AllowDelegate = cloneOptionalBool(p.spec.AllowDelegate)
	return &spec
}

func (p *FilePolicy) Root() string {
	if p == nil {
		return ""
	}
	return p.root
}

func (p *FilePolicy) CheckRead(target string) error {
	if !p.Enabled() {
		return nil
	}
	rel, abs, err := p.resolve(target)
	if err != nil {
		return err
	}
	if matchesAny(rel, p.spec.Deny) {
		return fmt.Errorf("file read denied by policy: %s", rel)
	}
	if len(p.spec.AllowRead) == 0 || !matchesAny(rel, p.spec.AllowRead) {
		return fmt.Errorf("file read denied by policy: %s", rel)
	}
	if !fileExists(abs) {
		return fmt.Errorf("file read denied: %s does not exist", rel)
	}
	return nil
}

func (p *FilePolicy) CheckWrite(target string) error {
	if !p.Enabled() {
		return nil
	}
	rel, abs, err := p.resolve(target)
	if err != nil {
		return err
	}
	if matchesAny(rel, p.spec.Deny) {
		return fmt.Errorf("file mutation denied by policy: %s", rel)
	}
	if fileExists(abs) {
		if len(p.spec.AllowEdit) == 0 || !matchesAny(rel, p.spec.AllowEdit) {
			return fmt.Errorf("file edit denied by policy: %s", rel)
		}
		return nil
	}
	if len(p.spec.AllowCreate) == 0 || !matchesAny(rel, p.spec.AllowCreate) {
		return fmt.Errorf("file creation denied by policy: %s", rel)
	}
	return nil
}

func (p *FilePolicy) CheckDelete(target string) error {
	if !p.Enabled() {
		return nil
	}
	rel, _, err := p.resolve(target)
	if err != nil {
		return err
	}
	if matchesAny(rel, p.spec.Deny) {
		return fmt.Errorf("file delete denied by policy: %s", rel)
	}
	if len(p.spec.AllowDelete) == 0 || !matchesAny(rel, p.spec.AllowDelete) {
		return fmt.Errorf("file delete denied by policy: %s", rel)
	}
	return nil
}

func (p *FilePolicy) CheckSearchPath(target string) error {
	if !p.Enabled() {
		return nil
	}
	rel, abs, err := p.resolveAllowingRoot(target)
	if err != nil {
		return err
	}
	if matchesAny(rel, p.spec.Deny) {
		return fmt.Errorf("search denied by policy: %s", rel)
	}
	if len(p.spec.AllowRead) == 0 {
		return fmt.Errorf("search denied by policy: no allow_read entries")
	}
	info, err := os.Stat(abs)
	if err == nil && !info.IsDir() {
		return p.CheckRead(abs)
	}

	prefixes := p.searchPrefixes()
	for _, prefix := range prefixes {
		if prefix == "" || rel == prefix || strings.HasPrefix(rel, prefix+"/") {
			return nil
		}
	}
	return fmt.Errorf("search denied by policy: %s", rel)
}

func (p *FilePolicy) MarshalJSON() ([]byte, error) {
	return json.Marshal(p.Spec())
}

func (p *FilePolicy) resolve(target string) (string, string, error) {
	rel, abs, err := p.resolveAllowingRoot(target)
	if err != nil {
		return "", "", err
	}
	if rel == "." {
		return "", "", fmt.Errorf("file policy denied: target must be a file path")
	}
	return rel, abs, nil
}

func (p *FilePolicy) resolveAllowingRoot(target string) (string, string, error) {
	abs := target
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(p.root, target)
	}
	abs = filepath.Clean(abs)

	rel, err := filepath.Rel(p.root, abs)
	if err != nil {
		return "", "", fmt.Errorf("resolve file policy path: %w", err)
	}
	rel = filepath.ToSlash(rel)
	if strings.HasPrefix(rel, "../") || rel == ".." {
		return "", "", fmt.Errorf("file policy denied: %q is outside workdir %s", abs, p.root)
	}
	return rel, abs, nil
}

func (p *FilePolicy) searchPrefixes() []string {
	if p == nil {
		return nil
	}
	seen := make(map[string]struct{})
	var prefixes []string
	for _, pattern := range p.spec.AllowRead {
		segments := strings.Split(pattern, "/")
		var prefix []string
		for _, segment := range segments {
			if segment == "**" || strings.ContainsAny(segment, "*?[") {
				break
			}
			prefix = append(prefix, segment)
		}
		value := strings.Join(prefix, "/")
		if len(prefix) == len(segments) {
			value = pattern
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		prefixes = append(prefixes, value)
	}
	return prefixes
}

func normalizePatterns(src []string) []string {
	if len(src) == 0 {
		return nil
	}
	out := make([]string, 0, len(src))
	for _, item := range src {
		item = strings.TrimSpace(filepath.ToSlash(item))
		if item != "" {
			out = append(out, item)
		}
	}
	return out
}

func cloneOptionalBool(src *bool) *bool {
	if src == nil {
		return nil
	}
	v := *src
	return &v
}

func matchesAny(target string, patterns []string) bool {
	for _, pattern := range patterns {
		ok, err := pathglob.Match(pattern, target)
		if err == nil && ok {
			return true
		}
	}
	return false
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
