package manifest

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// LoadFromFile 从指定路径加载 manifest（支持 .json 和 .yaml/.yml）
func LoadFromFile(path string) (*Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("manifest: 读取文件失败: %w", err)
	}

	m := &Manifest{}
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".yaml", ".yml":
		if err := yaml.Unmarshal(data, m); err != nil {
			return nil, fmt.Errorf("manifest: YAML 解析失败: %w", err)
		}
	default:
		if err := json.Unmarshal(data, m); err != nil {
			return nil, fmt.Errorf("manifest: JSON 解析失败: %w", err)
		}
	}

	abs, err := filepath.Abs(path)
	if err == nil {
		m.SourcePath = abs
	} else {
		m.SourcePath = path
	}

	return m, nil
}

// LoadFromDir 在目录中查找 brain.json 或 brain.yaml，按优先级加载第一个找到的
func LoadFromDir(dir string) (*Manifest, error) {
	candidates := []string{"brain.json", "brain.yaml", "brain.yml"}
	for _, name := range candidates {
		p := filepath.Join(dir, name)
		if _, err := os.Stat(p); err == nil {
			return LoadFromFile(p)
		}
	}
	return nil, fmt.Errorf("manifest: 在目录 %s 中未找到 brain.json 或 brain.yaml", dir)
}

// LoadAll 递归搜索指定目录下所有 brain.json / brain.yaml 并加载
func LoadAll(baseDir string) ([]*Manifest, error) {
	var results []*Manifest
	targetNames := map[string]bool{
		"brain.json": true,
		"brain.yaml": true,
		"brain.yml":  true,
	}

	err := filepath.Walk(baseDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // 跳过无权限的目录
		}
		if info.IsDir() {
			return nil
		}
		if targetNames[info.Name()] {
			m, loadErr := LoadFromFile(path)
			if loadErr != nil {
				return nil // 跳过解析失败的文件
			}
			results = append(results, m)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("manifest: 遍历目录失败: %w", err)
	}

	return results, nil
}
