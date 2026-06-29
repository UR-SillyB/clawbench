package openclaw

import (
	"context"
	"log/slog"
	"os/exec"
	"strings"
	"time"

	"clawbench/internal/model"
)

func init() {
	model.RegisterDiscoverModelsFunc("openclaw", DiscoverOpenClawModels)
}

// parseOpenClawModels parses `openclaw models list --plain` output.
// Output format: one "provider/model" per line, same as opencode.
// The first model is marked as default.
func parseOpenClawModels(output string) []model.AgentModel {
	var models []model.AgentModel
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		models = append(models, model.AgentModel{
			ID:      line,
			Name:    line,
			Default: len(models) == 0,
		})
	}
	return models
}

// DiscoverOpenClawModels discovers OpenClaw model IDs by running
// `openclaw models list --plain` and parsing the output.
func DiscoverOpenClawModels() []model.AgentModel {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "openclaw", "models", "list", "--plain")
	out, err := cmd.Output()
	if err != nil {
		slog.Debug("openclaw model discovery: command failed", "error", err)
		return nil
	}

	models := parseOpenClawModels(string(out))
	if len(models) == 0 {
		slog.Debug("openclaw model discovery: no models parsed")
		return nil
	}

	slog.Info("openclaw model discovery succeeded", "models", len(models))
	return models
}
