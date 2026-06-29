package hermes

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"clawbench/internal/ai/backends"
)

func TestHermesPlugin_Registered(t *testing.T) {
	p := backends.Lookup("hermes")
	assert.NotNil(t, p, "hermes plugin should be registered")
	if p == nil {
		return
	}
	assert.Equal(t, "hermes", p.Spec.Backend)
	assert.Equal(t, "hermes", p.Spec.DefaultCmd)
	assert.NotEmpty(t, p.Spec.AcpCommand, "hermes should support ACP")
}

func TestHermesDefaultModels(t *testing.T) {
	assert.NotEmpty(t, hermesDefaultModels)
	ids := make(map[string]bool)
	for _, m := range hermesDefaultModels {
		assert.NotEmpty(t, m.ID)
		assert.NotEmpty(t, m.Name)
		ids[m.ID] = true
	}
	assert.True(t, ids["glm-5.1"], "should contain GLM-5.1")
}

func TestDiscoverHermesModels_NotInstalled(t *testing.T) {
	// When hermes CLI is not on PATH, discovery returns nil (graceful skip).
	// This test relies on hermes not being installed in the test environment.
	models := DiscoverHermesModels()
	if models == nil {
		return // expected when CLI absent
	}
	// If hermes is installed, ensure defaults are returned.
	assert.NotEmpty(t, models)
}
