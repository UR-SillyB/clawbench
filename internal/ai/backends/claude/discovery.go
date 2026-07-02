package claude

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"clawbench/internal/model"
	"clawbench/internal/platform"
)

func init() {
	model.RegisterDiscoverModelsFunc("claude", DiscoverClaudeModels)
}

// claudeDefaultModels lists known Claude models as a fallback when binary
// scanning fails (e.g. claude CLI not found or ExtractStrings returns nothing).
var claudeDefaultModels = []model.AgentModel{
	{ID: "sonnet", Name: "Claude Sonnet", Default: true},
	{ID: "opus", Name: "Claude Opus"},
	{ID: "haiku", Name: "Claude Haiku"},
	{ID: "claude-sonnet-4-20250514", Name: "Claude Sonnet 4"},
	{ID: "claude-opus-4-20250514", Name: "Claude Opus 4"},
	{ID: "claude-haiku-3-5-20241022", Name: "Claude 3.5 Haiku"},
}

// claudeModelRe matches Claude model IDs like "claude-sonnet-4-6" or "claude-opus-4-5" from strings output.
// Requires exactly two version segments (major-minor), excludes:
// - date-stamped like "claude-opus-4-20250514" (8-digit date suffix)
// - short aliases like "claude-sonnet-4" (points to latest snapshot)
var claudeModelRe = regexp.MustCompile(`^claude-(sonnet|opus|haiku)-\d+-\d+$`)

// claudeModelOrder defines the preferred display order: sonnet first (default), then opus, then haiku.
var claudeModelOrder = map[string]int{"sonnet": 0, "opus": 1, "haiku": 2}

// knownClaudeAlias lists the short alias IDs that discovery appends as safe
// fallbacks alongside the full versioned Claude model IDs.
var knownClaudeAlias = map[string]bool{"sonnet": true, "opus": true, "haiku": true}

// KnownClaudeAlias reports whether id is one of the short alias IDs
// (sonnet/opus/haiku) produced by Claude model discovery.
func KnownClaudeAlias(id string) bool { return knownClaudeAlias[id] }

// claudeModelNames maps model ID prefixes to human-readable names.
var claudeModelNames = map[string]string{
	"sonnet": "Sonnet",
	"opus":   "Opus",
	"haiku":  "Haiku",
}

// claudeConfigDir returns the Claude config directory (~/.claude/).
// Overridable for testing (same pattern as DiscoverModels variable).
var claudeConfigDir = platform.ClaudeConfigDir

// LoadClaudeModelOverrides reads ~/.claude/settings.json and returns the
// modelOverrides map if present. Returns nil on any error (missing file,
// invalid JSON, no overrides key) — graceful degradation.
func LoadClaudeModelOverrides() map[string]string {
	path := filepath.Join(claudeConfigDir(), "settings.json")
	data, err := os.ReadFile(path)
	if err != nil {
		slog.Debug("claude model overrides: settings.json not found", "path", path, "error", err)
		return nil
	}
	var cfg struct {
		ModelOverrides map[string]string `json:"modelOverrides"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		slog.Debug("claude model overrides: invalid JSON", "path", path, "error", err)
		return nil
	}
	if len(cfg.ModelOverrides) == 0 {
		return nil
	}
	return cfg.ModelOverrides
}

// LoadClaudeEnvModelNames reads ~/.claude/settings.json env fields set by CC Switch
// and returns a map from Claude family (sonnet/opus/haiku) to the actual model name.
// CC Switch configures env vars like ANTHROPIC_DEFAULT_SONNET_MODEL=glm-5.1 to route
// Claude model families to third-party providers. We use these to rename the display
// name so users see which real model is in use.
func LoadClaudeEnvModelNames() map[string]string {
	path := filepath.Join(claudeConfigDir(), "settings.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var cfg struct {
		Env map[string]string `json:"env"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil
	}
	if len(cfg.Env) == 0 {
		return nil
	}
	result := make(map[string]string)
	for _, family := range []string{"sonnet", "opus", "haiku"} {
		key := "ANTHROPIC_DEFAULT_" + strings.ToUpper(family) + "_MODEL"
		if name := cfg.Env[key]; name != "" {
			result[family] = name
		}
	}
	return result
}

// claudeIsDateStamped returns true if the model ID contains an 8-digit date segment
// like "claude-opus-4-20250514", which are snapshot aliases we want to skip.
func claudeIsDateStamped(modelID string) bool {
	for _, seg := range strings.Split(modelID, "-") {
		if len(seg) == 8 {
			return true
		}
	}
	return false
}

// DiscoverClaudeModels discovers Claude model IDs by scanning the claude binary
// with `strings`. Claude CLI does not have a --list-models command, so we extract
// model IDs from the binary which contains hardcoded model name patterns.
func DiscoverClaudeModels() []model.AgentModel { //nolint:gocyclo,gocognit // binary scanning model discovery
	// Resolve the real path for the claude binary, handling Windows .cmd wrappers
	path := platform.ResolveCLIPath("claude")
	if path == "" {
		// Claude binary not found — fall back to known defaults
		models := make([]model.AgentModel, len(claudeDefaultModels))
		copy(models, claudeDefaultModels)
		if len(models) > 0 {
			models[0].Default = true
		}
		slog.Info("claude model discovery: binary not found, using defaults", "models", len(models))
		return models
	}

	// Extract printable strings from the binary (cross-platform replacement for
	// the POSIX "strings" command, which does not exist on Windows)
	lines, err := platform.ExtractStrings(path, 4)
	if err != nil {
		slog.Debug("claude model discovery: extract strings failed", "error", err)
		return nil
	}

	// Extract unique model IDs matching the pattern
	seen := make(map[string]bool)
	var models []model.AgentModel
	for _, line := range lines {
		if !claudeModelRe.MatchString(line) || seen[line] {
			continue
		}
		// Skip date-stamped versions like claude-opus-4-20250514
		if claudeIsDateStamped(line) {
			continue
		}
		seen[line] = true

		// Generate human-readable name: claude-sonnet-4-6 → "Claude Sonnet 4.6"
		parts := strings.SplitN(line, "-", 3) // ["claude", "sonnet", "4-6"]
		name := line
		if len(parts) == 3 {
			if family, ok := claudeModelNames[parts[1]]; ok {
				version := strings.ReplaceAll(parts[2], "-", ".")
				name = "Claude " + family + " " + version
			}
		}

		models = append(models, model.AgentModel{
			ID:   line,
			Name: name,
		})
	}

	// Sort: sonnet first, then opus, then haiku; within each family, newest first
	sort.Slice(models, func(i, j int) bool {
		familyI := strings.SplitN(models[i].ID, "-", 3)
		familyJ := strings.SplitN(models[j].ID, "-", 3)
		if len(familyI) >= 2 && len(familyJ) >= 2 {
			orderI, okI := claudeModelOrder[familyI[1]]
			orderJ, okJ := claudeModelOrder[familyJ[1]]
			if okI && okJ && orderI != orderJ {
				return orderI < orderJ
			}
		}
		// Same family: sort by ID descending (newest first)
		return models[i].ID > models[j].ID
	})

	// Apply model name overrides from ~/.claude/settings.json
	// When modelOverrides maps a Claude model ID to another name (e.g. "MiniMax-M2.7"),
	// we replace the display name so the user sees which underlying model is actually used.
	// The model ID is NOT changed — CLI invocation always uses the original Claude model ID.
	if overrides := LoadClaudeModelOverrides(); len(overrides) > 0 {
		for i := range models {
			if name, ok := overrides[models[i].ID]; ok {
				slog.Debug("claude model override applied", "id", models[i].ID, "name", name)
				models[i].Name = name
			}
		}
		// Deduplicate by Name: when multiple IDs override to the same name,
		// keep only the first occurrence (highest priority by sort order) and drop the rest.
		seenNames := make(map[string]bool)
		deduped := make([]model.AgentModel, 0, len(models))
		for i := range models {
			if seenNames[models[i].Name] {
				slog.Debug("claude model override dedup: dropping duplicate name", "id", models[i].ID, "name", models[i].Name)
				continue
			}
			seenNames[models[i].Name] = true
			deduped = append(deduped, models[i])
		}
		models = deduped
	}

	// Mark first model as default (after dedup so the flag is on the actual first entry)
	if len(models) > 0 {
		models[0].Default = true
	}

	// Apply CC Switch env model names (e.g. ANTHROPIC_DEFAULT_SONNET_MODEL=glm-5.1).
	// Maps the family portion of claude-sonnet-4-6 to the real model name.
	envNames := LoadClaudeEnvModelNames()
	if len(envNames) > 0 {
		for i := range models {
			parts := strings.SplitN(models[i].ID, "-", 3) // ["claude", "sonnet", "4-6"]
			if len(parts) >= 2 {
				if realName, ok := envNames[parts[1]]; ok {
					slog.Debug("claude env model name applied", "id", models[i].ID, "family", parts[1], "name", realName)
					models[i].Name = realName
				}
			}
		}
	}

	// When CC Switch maps all families to the same model (e.g., "free"),
	// merge everything into a single entry using an alias ID (works in both
	// CLI and ACP modes) with the CC Switch display name.
	// Otherwise, keep per-family deduped entries + prepend alias fallbacks.
	if len(envNames) > 0 {
		// Check if all mapped families share the same name
		uniqueNames := make(map[string]bool)
		for _, name := range envNames {
			uniqueNames[name] = true
		}
		if len(uniqueNames) == 1 {
			// All families route to the same model - single entry
			singleName := ""
			for _, name := range envNames {
				singleName = name
				break
			}
			models = []model.AgentModel{
				{ID: "sonnet", Name: singleName, Default: true},
			}
			slog.Debug("claude model discovery: CC Switch merged all families into single model", "name", singleName)
		}
	}

	// If CC Switch didn't collapse into one, do per-family dedup and add aliases
	if len(models) > 1 {
		// Deduplicate within same family by display name
		seenKey := make(map[string]bool)
		var deduped []model.AgentModel
		for _, m := range models {
			parts := strings.SplitN(m.ID, "-", 3)
			family := "other"
			if len(parts) >= 2 {
				family = parts[1]
			}
			key := family + ":" + m.Name
			if seenKey[key] {
				continue
			}
			seenKey[key] = true
			deduped = append(deduped, m)
		}
		if len(deduped) > 0 {
			deduped[0].Default = true
		}
		models = deduped

		// Append well-known alias models as safe fallbacks after the versioned
		// models, so the first entry remains the default versioned model.
		aliasModels := []model.AgentModel{
			{ID: "sonnet", Name: "Claude Sonnet (alias)"},
			{ID: "opus", Name: "Claude Opus (alias)"},
			{ID: "haiku", Name: "Claude Haiku (alias)"},
		}
		seenID := make(map[string]bool, len(models))
		for _, m := range models {
			seenID[m.ID] = true
		}
		for _, am := range aliasModels {
			if !seenID[am.ID] {
				models = append(models, am)
			}
		}
	}

	// If binary scanning found no models, fall back to known defaults
	if len(models) == 0 {
		models = make([]model.AgentModel, len(claudeDefaultModels))
		copy(models, claudeDefaultModels)
		if len(models) > 0 {
			models[0].Default = true
		}
		slog.Info("claude model discovery: binary scan found nothing, using defaults", "models", len(models))
		return models
	}

	return models
}
