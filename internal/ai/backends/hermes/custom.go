package hermes

import (
	"clawbench/internal/ai/backends"
	"clawbench/internal/model"
)

func init() {
	backends.Register(&backends.BackendPlugin{
		ID: "hermes",
		Spec: model.BackendSpec{
			ID: "hermes", Backend: "hermes", DefaultCmd: "hermes", Name: "Hermes", Icon: "🏷️", Specialty: "Hermes AI 智能体",
			AcpCommand: "hermes acp",
			SortOrder:  13,
		},
		// Hermes is ACP-only (no CLI streaming backend). When an agent is configured
		// with the acp-stdio transport, NewBackendForAgentWithTransport routes to
		// ACPBackend via SupportsACP(); a CLI fallback would return an error.
	})
}
