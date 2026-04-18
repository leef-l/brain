// Package kernel implements the Marketplace interface for discovering,
// searching and filtering available Brain packages.
//
// Phase D-2: Marketplace 索引。
// 规范文档: sdk/docs/34-Brain-Package与Marketplace规范.md §11-12
package kernel

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// Public types
// ---------------------------------------------------------------------------

// MarketplaceEntry 表示 Marketplace 索引中的一个 package 条目。
// 字段设计参照 34 号文档 §12。
type MarketplaceEntry struct {
	PackageID    string   `json:"package_id"`
	Name         string   `json:"display_name"`
	Description  string   `json:"description,omitempty"`
	Kind         string   `json:"brain_kind"`
	Version      string   `json:"package_version"`
	Publisher    string   `json:"publisher"`
	Capabilities []string `json:"capabilities,omitempty"`
	RuntimeType  string   `json:"runtime_type"` // native / mcp-backed / hybrid
	Downloads    int      `json:"downloads,omitempty"`
	Rating       float64  `json:"rating,omitempty"`
	Compatible   bool     `json:"compatible"` // 与当前 kernel 版本兼容

	// 扩展字段（来自 34 号文档 §12.2）
	Channel         string `json:"channel,omitempty"`
	LicenseRequired bool   `json:"license_required,omitempty"`
	Edition         string `json:"edition,omitempty"`
	Homepage        string `json:"homepage,omitempty"`
	IconURL         string `json:"icon_url,omitempty"`
	DocsURL         string `json:"docs_url,omitempty"`
}

// MarketplaceFilter 用于 List 方法的筛选条件。
type MarketplaceFilter struct {
	Kind        string `json:"kind,omitempty"`
	Capability  string `json:"capability,omitempty"`
	RuntimeType string `json:"runtime_type,omitempty"`
	Publisher   string `json:"publisher,omitempty"`
}

// Marketplace 定义 package 索引的读取接口。
// Marketplace 不负责执行 brain，也不做 delegate 决策（34 号文档 §11）。
type Marketplace interface {
	// Search 对 name/description/kind 做模糊匹配。
	Search(ctx context.Context, query string) ([]MarketplaceEntry, error)

	// Get 按 package_id 精确获取。
	Get(ctx context.Context, packageID string) (*MarketplaceEntry, error)

	// List 按 filter 筛选全部条目。
	List(ctx context.Context, filter MarketplaceFilter) ([]MarketplaceEntry, error)
}

// ---------------------------------------------------------------------------
// Index file schema
// ---------------------------------------------------------------------------

// MarketplaceIndex 是 index.json 的顶层结构。
type MarketplaceIndex struct {
	Version   int                `json:"version"`
	UpdatedAt time.Time          `json:"updated_at"`
	Entries   []MarketplaceEntry `json:"entries"`
}

// ---------------------------------------------------------------------------
// LocalMarketplace — 基于本地 index.json 的实现
// ---------------------------------------------------------------------------

// LocalMarketplace 从 ~/.brain/marketplace/index.json 读取索引。
type LocalMarketplace struct {
	indexPath string
}

// NewLocalMarketplace 创建一个 LocalMarketplace。
// 如果 indexPath 为空，默认使用 ~/.brain/marketplace/index.json。
func NewLocalMarketplace(indexPath string) *LocalMarketplace {
	if indexPath == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			indexPath = filepath.Join(".", ".brain", "marketplace", "index.json")
		} else {
			indexPath = filepath.Join(home, ".brain", "marketplace", "index.json")
		}
	}
	return &LocalMarketplace{indexPath: indexPath}
}

// IndexPath 返回当前使用的索引文件路径。
func (m *LocalMarketplace) IndexPath() string {
	return m.indexPath
}

// loadIndex 读取并解析 index.json。
func (m *LocalMarketplace) loadIndex() (*MarketplaceIndex, error) {
	data, err := os.ReadFile(m.indexPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("marketplace index not found: %s (run `brain marketplace sync` to initialize)", m.indexPath)
		}
		return nil, fmt.Errorf("read marketplace index: %w", err)
	}

	var idx MarketplaceIndex
	if err := json.Unmarshal(data, &idx); err != nil {
		return nil, fmt.Errorf("parse marketplace index: %w", err)
	}

	if idx.Version < 1 {
		return nil, fmt.Errorf("unsupported marketplace index version: %d", idx.Version)
	}

	return &idx, nil
}

// Search 对 name、description、kind 做大小写不敏感的模糊匹配。
func (m *LocalMarketplace) Search(_ context.Context, query string) ([]MarketplaceEntry, error) {
	idx, err := m.loadIndex()
	if err != nil {
		return nil, err
	}

	q := strings.ToLower(query)
	var results []MarketplaceEntry
	for _, e := range idx.Entries {
		if fuzzyMatch(q, e) {
			results = append(results, e)
		}
	}
	return results, nil
}

// Get 按 package_id 精确查找。
func (m *LocalMarketplace) Get(_ context.Context, packageID string) (*MarketplaceEntry, error) {
	idx, err := m.loadIndex()
	if err != nil {
		return nil, err
	}

	for i := range idx.Entries {
		if idx.Entries[i].PackageID == packageID {
			return &idx.Entries[i], nil
		}
	}
	return nil, fmt.Errorf("package %q not found in marketplace index", packageID)
}

// List 按 filter 条件筛选。空 filter 返回全部。
func (m *LocalMarketplace) List(_ context.Context, filter MarketplaceFilter) ([]MarketplaceEntry, error) {
	idx, err := m.loadIndex()
	if err != nil {
		return nil, err
	}

	var results []MarketplaceEntry
	for _, e := range idx.Entries {
		if matchFilter(e, filter) {
			results = append(results, e)
		}
	}
	return results, nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// fuzzyMatch 检查 query 是否出现在 entry 的 name、description 或 kind 中。
func fuzzyMatch(query string, e MarketplaceEntry) bool {
	if query == "" {
		return true
	}
	fields := []string{
		strings.ToLower(e.Name),
		strings.ToLower(e.Description),
		strings.ToLower(e.Kind),
		strings.ToLower(e.PackageID),
		strings.ToLower(e.Publisher),
	}
	for _, cap := range e.Capabilities {
		fields = append(fields, strings.ToLower(cap))
	}
	for _, f := range fields {
		if strings.Contains(f, query) {
			return true
		}
	}
	return false
}

// matchFilter 检查 entry 是否满足所有非空 filter 字段。
func matchFilter(e MarketplaceEntry, f MarketplaceFilter) bool {
	if f.Kind != "" && !strings.EqualFold(e.Kind, f.Kind) {
		return false
	}
	if f.RuntimeType != "" && !strings.EqualFold(e.RuntimeType, f.RuntimeType) {
		return false
	}
	if f.Publisher != "" && !strings.EqualFold(e.Publisher, f.Publisher) {
		return false
	}
	if f.Capability != "" {
		found := false
		capLower := strings.ToLower(f.Capability)
		for _, c := range e.Capabilities {
			if strings.ToLower(c) == capLower {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
