package openclaw

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"clawbench/internal/ai/backends"
)

func TestOpenClawPlugin_Registered(t *testing.T) {
	p := backends.Lookup("openclaw")
	assert.NotNil(t, p, "openclaw plugin should be registered")
	if p == nil {
		return
	}
	assert.Equal(t, "openclaw", p.Spec.Backend)
	assert.Equal(t, "openclaw", p.Spec.DefaultCmd)
	assert.NotEmpty(t, p.Spec.AcpCommand, "openclaw should support ACP")
	assert.NotEmpty(t, p.Spec.ThinkingEffortLevels)
}

func TestParseOpenClawModels(t *testing.T) {
	output := "anthropic/claude-sonnet-4\nopenai/gpt-4.1\ngoogle/gemini-2.5-pro\n"
	models := parseOpenClawModels(output)
	assert.Len(t, models, 3)
	assert.Equal(t, "anthropic/claude-sonnet-4", models[0].ID)
	assert.True(t, models[0].Default, "first model should be default")
	assert.False(t, models[1].Default)
}

func TestParseOpenClawModels_Empty(t *testing.T) {
	assert.Empty(t, parseOpenClawModels(""))
	assert.Empty(t, parseOpenClawModels("\n\n  \n"))
}

func TestDiscoverOpenClawModels_NotInstalled(t *testing.T) {
	// When openclaw CLI is not on PATH, discovery returns nil (graceful skip).
	models := DiscoverOpenClawModels()
	if models == nil {
		return // expected when CLI absent
	}
	assert.NotEmpty(t, models)
}
