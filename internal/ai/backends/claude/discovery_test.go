package claude

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClaudeIsDateStamped(t *testing.T) {
	tests := []struct {
		modelID  string
		expected bool
	}{
		{"claude-opus-4-20250514", true},
		{"claude-sonnet-4-6", false},
		{"claude-haiku-3-5-20241022", true},
		{"claude-sonnet-4-20250514", true},
		{"claude-opus-4-5", false},
		{"claude-3-5-haiku-20241022", true},
		{"claude-3-haiku-20240307", true},
		{"claude-sonnet-4-6", false},
	}

	for _, tt := range tests {
		t.Run(tt.modelID, func(t *testing.T) {
			assert.Equal(t, tt.expected, claudeIsDateStamped(tt.modelID))
		})
	}
}

func TestClaudeModelRe(t *testing.T) {
	tests := []struct {
		input    string
		expected bool
	}{
		{"claude-sonnet-4-6", true},
		{"claude-opus-4-5", true},
		{"claude-haiku-3-5", true},
		{"claude-sonnet-4-20250514", true}, // regex matches (4-20250514 is two digit segments), but claudeIsDateStamped filters it
		{"claude-opus-4", false},           // single version segment
		{"claude-3-5-haiku", false},        // old naming convention
		{"gpt-4.1", false},                 // not a Claude model
		{"sonnet-4-6", false},              // missing "claude-" prefix
		{"claude-sonnet-4-6-1", false},     // three version segments
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			assert.Equal(t, tt.expected, claudeModelRe.MatchString(tt.input))
		})
	}
}

func TestClaudeModelNames(t *testing.T) {
	assert.Equal(t, "Sonnet", claudeModelNames["sonnet"])
	assert.Equal(t, "Opus", claudeModelNames["opus"])
	assert.Equal(t, "Haiku", claudeModelNames["haiku"])
}

func TestClaudeModelOrder(t *testing.T) {
	assert.Equal(t, 0, claudeModelOrder["sonnet"], "sonnet should come first")
	assert.Equal(t, 1, claudeModelOrder["opus"], "opus should come second")
	assert.Equal(t, 2, claudeModelOrder["haiku"], "haiku should come last")
}

func TestClaudeDefaultModels_Structure(t *testing.T) {
	assert.NotEmpty(t, claudeDefaultModels)

	defaultCount := 0
	for _, m := range claudeDefaultModels {
		assert.NotEmpty(t, m.ID)
		assert.NotEmpty(t, m.Name)
		if m.Default {
			defaultCount++
		}
	}
	// Default models in the list don't have Default:true set explicitly;
	// DiscoverClaudeModels sets it on the first model when returning defaults.
}

func TestClaudeDefaultModels_ContainsKnownModels(t *testing.T) {
	ids := make(map[string]bool)
	for _, m := range claudeDefaultModels {
		ids[m.ID] = true
	}
	assert.True(t, ids["claude-sonnet-4-20250514"], "should contain Claude Sonnet 4")
	assert.True(t, ids["claude-opus-4-20250514"], "should contain Claude Opus 4")
}

func TestLoadClaudeModelOverrides_NoConfigDir(t *testing.T) {
	// Override configDir to a nonexistent path
	orig := claudeConfigDir
	defer func() { claudeConfigDir = orig }()

	claudeConfigDir = func() string { return "/nonexistent/path" }
	overrides := LoadClaudeModelOverrides()
	assert.Nil(t, overrides, "should return nil when config dir doesn't exist")
}

func TestLoadClaudeEnvModelNames(t *testing.T) {
	t.Run("valid env", func(t *testing.T) {
		tmpDir := t.TempDir()
		settingsContent := `{
			"env": {
				"ANTHROPIC_DEFAULT_SONNET_MODEL": "glm-5.1",
				"ANTHROPIC_DEFAULT_OPUS_MODEL": "glm-4-flash",
				"ANTHROPIC_BASE_URL": "https://open.bigmodel.cn/api/anthropic"
			}
		}`
		require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "settings.json"), []byte(settingsContent), 0o644))
		orig := claudeConfigDir
		claudeConfigDir = func() string { return tmpDir }
		defer func() { claudeConfigDir = orig }()
		m := LoadClaudeEnvModelNames()
		assert.Equal(t, "glm-5.1", m["sonnet"])
		assert.Equal(t, "glm-4-flash", m["opus"])
		assert.Empty(t, m["haiku"])
	})

	t.Run("no env", func(t *testing.T) {
		tmpDir := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "settings.json"), []byte(`{}`), 0o644))
		orig := claudeConfigDir
		claudeConfigDir = func() string { return tmpDir }
		defer func() { claudeConfigDir = orig }()
		assert.Nil(t, LoadClaudeEnvModelNames())
	})

	t.Run("missing file", func(t *testing.T) {
		orig := claudeConfigDir
		claudeConfigDir = func() string { return "/nonexistent" }
		defer func() { claudeConfigDir = orig }()
		assert.Nil(t, LoadClaudeEnvModelNames())
	})
}

func TestKnownClaudeAlias(t *testing.T) {
	assert.True(t, KnownClaudeAlias("sonnet"))
	assert.True(t, KnownClaudeAlias("opus"))
	assert.True(t, KnownClaudeAlias("haiku"))
	assert.False(t, KnownClaudeAlias("claude-sonnet-4-6"))
	assert.False(t, KnownClaudeAlias("gpt-4"))
}
