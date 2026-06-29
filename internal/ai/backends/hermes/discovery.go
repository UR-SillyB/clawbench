package hermes

import (
	"log/slog"
	"os/exec"

	"clawbench/internal/model"
)

func init() {
	model.RegisterDiscoverModelsFunc("hermes", DiscoverHermesModels)
}

// hermesDefaultModels lists known models for Hermes Agent.
var hermesDefaultModels = []model.AgentModel{
	{ID: "glm-5.1", Name: "GLM-5.1"},
	{ID: "glm-5", Name: "GLM-5"},
	{ID: "anthropic/claude-sonnet-4", Name: "Claude Sonnet 4"},
	{ID: "openai/gpt-4.1", Name: "GPT-4.1"},
	{ID: "google/gemini-2.5-pro", Name: "Gemini 2.5 Pro"},
}

// DiscoverHermesModels discovers models for Hermes Agent.
func DiscoverHermesModels() []model.AgentModel {
	if _, err := exec.LookPath("hermes"); err != nil {
		return nil
	}
	models := make([]model.AgentModel, len(hermesDefaultModels))
	copy(models, hermesDefaultModels)
	slog.Info("hermes model discovery: using hardcoded defaults", "models", len(models))
	return models
}
