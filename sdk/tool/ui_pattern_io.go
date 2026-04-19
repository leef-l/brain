package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Pattern library import/export.
//
// See sdk/docs/40-Browser-Brain语义理解架构.md §3.3.
//
// Goals:
//   - Share seed/user patterns across installs (community, team, pro-tier).
//   - Never leak private telemetry (Stats) or disabled/learned rows by default.
//   - Never silently overwrite builtin `source=seed` patterns on import — this
//     would let a malicious pack redirect `login_username_password` etc.
//   - Only touch the library through the already-public Upsert / ListAll
//     surface; no direct SQL here.

// PatternExportSchemaVersion is the on-disk envelope version for exported
// pattern packs. Bump whenever the envelope shape changes (not the UIPattern
// shape — that's handled by pattern-level migration in load()).
const PatternExportSchemaVersion = "1.0.0"

// ExportFilter narrows which patterns make it into an export bundle.
// Empty fields mean "no filter on this dimension". Multiple fields AND
// together (e.g. IDs={"x"} + Categories={"auth"} → only id=x if it is also
// auth). Zero-value filter exports everything exportable.
//
// IncludeDisabled / IncludeLearned default to false — a pattern pack meant
// for sharing should contain curated, enabled, non-telemetry material only.
type ExportFilter struct {
	IDs              []string
	Categories       []string
	Sources          []string // e.g. {"seed","user"}; empty = all sources
	IncludeDisabled  bool     // include Enabled=false rows
	IncludeLearned   bool     // include Source="learned" rows
	Origin           string   // free-form label written into envelope (e.g. "brain-team-share")
}

// PatternExport is the file envelope. Stats are intentionally zeroed out in
// Marshal so exporting a pack doesn't leak hit/success counts from a private
// run. Consumers who want telemetry can keep their own DB.
type PatternExport struct {
	SchemaVersion string      `json:"schema_version"`
	ExportedAt    time.Time   `json:"exported_at"`
	Origin        string      `json:"origin,omitempty"`
	Count         int         `json:"count"`
	Patterns      []UIPattern `json:"patterns"`
}

// ImportMode controls how Import handles existing rows with the same ID.
type ImportMode string

const (
	// ImportModeMerge keeps existing rows on conflict. New rows are added.
	// Existing rows' Stats and Enabled flag are preserved. Safe default.
	ImportModeMerge ImportMode = "merge"

	// ImportModeOverwrite replaces existing rows (except builtin-seed rows,
	// which stay protected unless opts.AllowOverwriteBuiltin=true). Stats
	// on overwritten rows are NOT reset — they keep their local telemetry.
	ImportModeOverwrite ImportMode = "overwrite"

	// ImportModeDryRun performs all validation + conflict analysis but does
	// not write anything to the DB. Report is filled normally.
	ImportModeDryRun ImportMode = "dry-run"
)

// ImportOptions configures an Import call.
type ImportOptions struct {
	Mode                   ImportMode
	AllowOverwriteBuiltin  bool // permit Source="seed" rows to be overwritten; default false
	CategoryFilter         []string // if non-empty, only import patterns in these categories
}

// ImportReport summarizes what an Import would do / did. Counts always reflect
// what *would* happen; in dry-run mode Written==0.
type ImportReport struct {
	Mode        ImportMode `json:"mode"`
	Total       int        `json:"total"`        // patterns in the envelope
	Added       int        `json:"added"`        // new IDs added
	Updated     int        `json:"updated"`      // existing IDs overwritten (overwrite mode only)
	Skipped     int        `json:"skipped"`      // conflict kept existing (merge mode) or filtered by category
	Rejected    int        `json:"rejected"`     // invalid / protected
	RejectedIDs []string   `json:"rejected_ids,omitempty"`
	SkippedIDs  []string   `json:"skipped_ids,omitempty"`
	Errors      []string   `json:"errors,omitempty"`
	Written     int        `json:"written"`      // actually persisted (0 in dry-run)
}

// Export writes patterns matching filter into an envelope-wrapped JSON blob.
// Stats are zeroed; disabled/learned rows are skipped unless the filter opts
// them in. The returned bytes are deterministic within a given clock second
// (patterns sorted by ID).
func (lib *PatternLibrary) Export(ctx context.Context, filter ExportFilter) ([]byte, error) {
	if lib == nil {
		return nil, fmt.Errorf("pattern library is nil")
	}
	idSet := toStringSet(filter.IDs)
	catSet := toStringSet(filter.Categories)
	srcSet := toStringSet(filter.Sources)

	// ListAll gives us enabled+disabled; filter down manually to respect
	// IncludeDisabled / IncludeLearned semantics.
	all := lib.ListAll("")
	picked := make([]UIPattern, 0, len(all))
	for _, p := range all {
		if p == nil {
			continue
		}
		if len(idSet) > 0 {
			if _, ok := idSet[p.ID]; !ok {
				continue
			}
		}
		if len(catSet) > 0 {
			if _, ok := catSet[p.Category]; !ok {
				continue
			}
		}
		if len(srcSet) > 0 {
			if _, ok := srcSet[p.Source]; !ok {
				continue
			}
		}
		if !filter.IncludeDisabled && !p.Enabled {
			continue
		}
		if !filter.IncludeLearned && p.Source == "learned" {
			continue
		}
		cp := *p
		// Strip private telemetry: Stats, audit timestamps.
		cp.Stats = PatternStats{}
		cp.CreatedAt = time.Time{}
		cp.UpdatedAt = time.Time{}
		picked = append(picked, cp)
	}
	sort.Slice(picked, func(i, j int) bool { return picked[i].ID < picked[j].ID })

	env := PatternExport{
		SchemaVersion: PatternExportSchemaVersion,
		ExportedAt:    time.Now().UTC(),
		Origin:        strings.TrimSpace(filter.Origin),
		Count:         len(picked),
		Patterns:      picked,
	}
	return json.MarshalIndent(env, "", "  ")
}

// ExportByCategory is a shortcut for the common "share all enabled patterns
// in this category" operation. Learned patterns and disabled patterns are
// both excluded; private stats are stripped.
func (lib *PatternLibrary) ExportByCategory(ctx context.Context, category string) ([]byte, error) {
	category = strings.TrimSpace(category)
	if category == "" {
		return nil, fmt.Errorf("category required")
	}
	return lib.Export(ctx, ExportFilter{Categories: []string{category}})
}

// Import ingests a pattern pack according to opts. It never touches Stats on
// existing rows. Protected cases (builtin-seed overwrite, structural
// violations) are collected in ImportReport rather than aborting the batch,
// so a partial pack can still be applied.
func (lib *PatternLibrary) Import(ctx context.Context, data []byte, opts ImportOptions) (*ImportReport, error) {
	if lib == nil {
		return nil, fmt.Errorf("pattern library is nil")
	}
	if opts.Mode == "" {
		opts.Mode = ImportModeMerge
	}
	switch opts.Mode {
	case ImportModeMerge, ImportModeOverwrite, ImportModeDryRun:
	default:
		return nil, fmt.Errorf("unknown import mode %q", opts.Mode)
	}

	var env PatternExport
	if err := json.Unmarshal(data, &env); err != nil {
		return nil, fmt.Errorf("decode envelope: %w", err)
	}
	if env.SchemaVersion == "" {
		return nil, fmt.Errorf("missing schema_version — not a pattern export file")
	}
	if !schemaCompatible(env.SchemaVersion) {
		return nil, fmt.Errorf("unsupported schema_version %q (this build supports %s)", env.SchemaVersion, PatternExportSchemaVersion)
	}

	catFilter := toStringSet(opts.CategoryFilter)
	report := &ImportReport{Mode: opts.Mode, Total: len(env.Patterns)}
	seen := make(map[string]struct{}, len(env.Patterns))

	for i := range env.Patterns {
		p := env.Patterns[i]
		if err := validatePattern(&p); err != nil {
			report.Rejected++
			report.RejectedIDs = append(report.RejectedIDs, p.ID)
			report.Errors = append(report.Errors, fmt.Sprintf("%s: %v", p.ID, err))
			continue
		}
		if _, dup := seen[p.ID]; dup {
			report.Rejected++
			report.RejectedIDs = append(report.RejectedIDs, p.ID)
			report.Errors = append(report.Errors, fmt.Sprintf("%s: duplicate id in envelope", p.ID))
			continue
		}
		seen[p.ID] = struct{}{}

		if len(catFilter) > 0 {
			if _, ok := catFilter[p.Category]; !ok {
				report.Skipped++
				report.SkippedIDs = append(report.SkippedIDs, p.ID)
				continue
			}
		}

		existing := lib.GetAny(p.ID)
		if existing != nil {
			// Refuse to clobber seed/builtin rows unless explicitly allowed.
			if existing.Source == "seed" && !opts.AllowOverwriteBuiltin {
				report.Rejected++
				report.RejectedIDs = append(report.RejectedIDs, p.ID)
				report.Errors = append(report.Errors, fmt.Sprintf("%s: builtin seed pattern — pass allow_overwrite_builtin to override", p.ID))
				continue
			}
			if opts.Mode == ImportModeMerge {
				report.Skipped++
				report.SkippedIDs = append(report.SkippedIDs, p.ID)
				continue
			}
		}

		// Preserve local telemetry + enabled flag on overwrite, so re-importing
		// a curated pack doesn't reset a user's hard-won success stats.
		merged := p
		if existing != nil {
			merged.Stats = existing.Stats
			merged.Enabled = existing.Enabled
			merged.CreatedAt = existing.CreatedAt
		}
		// Fresh import with no prior row: default Enabled=true so Upsert doesn't
		// need to special-case an envelope that omitted the flag.
		if existing == nil {
			merged.Enabled = true
		}

		if opts.Mode == ImportModeDryRun {
			if existing == nil {
				report.Added++
			} else {
				report.Updated++
			}
			continue
		}

		if err := lib.Upsert(ctx, &merged); err != nil {
			report.Rejected++
			report.RejectedIDs = append(report.RejectedIDs, p.ID)
			report.Errors = append(report.Errors, fmt.Sprintf("%s: upsert: %v", p.ID, err))
			continue
		}
		if existing == nil {
			report.Added++
		} else {
			report.Updated++
		}
		report.Written++
	}
	return report, nil
}

// validatePattern checks the minimum invariants an exported pattern must hold
// so it can be executed safely after import.
func validatePattern(p *UIPattern) error {
	if p.ID == "" {
		return fmt.Errorf("empty id")
	}
	if strings.ContainsAny(p.ID, " \t\n") {
		return fmt.Errorf("id must not contain whitespace")
	}
	if p.Category == "" {
		return fmt.Errorf("empty category")
	}
	// A pattern with no AppliesWhen + no ElementRoles + no ActionSequence is
	// a no-op and almost certainly a corrupted entry. Seed pattern
	// `skip_login_already_authed` has empty roles+actions but a non-empty
	// AppliesWhen, so we only reject the all-three-empty case.
	emptyApplies := len(p.AppliesWhen.Has) == 0 && len(p.AppliesWhen.HasNot) == 0 &&
		len(p.AppliesWhen.TitleContains) == 0 && len(p.AppliesWhen.TextContains) == 0 &&
		strings.TrimSpace(p.AppliesWhen.URLPattern) == ""
	if emptyApplies && len(p.ElementRoles) == 0 && len(p.ActionSequence) == 0 {
		return fmt.Errorf("empty pattern (no applies_when / roles / actions)")
	}
	return nil
}

// schemaCompatible decides whether this build can ingest a given envelope
// version. Rule: same major version is compatible, minor/patch are forward-
// compatible (unknown fields ignored by json.Unmarshal).
func schemaCompatible(v string) bool {
	v = strings.TrimSpace(v)
	if v == "" {
		return false
	}
	major := strings.SplitN(v, ".", 2)[0]
	curMajor := strings.SplitN(PatternExportSchemaVersion, ".", 2)[0]
	return major == curMajor
}

func toStringSet(in []string) map[string]struct{} {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]struct{}, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		out[s] = struct{}{}
	}
	return out
}
