package openclaw

import (
	"clawbench/internal/ai/backends"
	"clawbench/internal/model"
)

func init() {
	backends.Register(&backends.BackendPlugin{
		ID: "openclaw",
		Spec: model.BackendSpec{
			ID: "openclaw", Backend: "openclaw", DefaultCmd: "openclaw", Name: "OpenClaw", Icon: "🐾", Specialty: "OpenClaw 多模型网关",
			ThinkingEffortLevels: []string{"off", "minimal", "low", "medium", "high", "xhigh", "adaptive", "max"},
			AcpCommand:           "openclaw acp",
			SortOrder:            14,
		},
		// OpenClaw is ACP-only (no CLI streaming backend). When an agent is configured
		// with the acp-stdio transport, NewBackendForAgentWithTransport routes to
		// ACPBackend via SupportsACP(); a CLI fallback would return an error.
	})
}
