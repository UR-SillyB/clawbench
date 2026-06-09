package ai

import (
	"database/sql"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"clawbench/internal/model"
)

// AgentCapability holds agent-level ACP capabilities that are shared
// across all sessions of the same agent. These are inherent to the
// agent binary (e.g., Claude's available modes: [ask, architect, code])
// and do not vary between sessions.
type AgentCapability struct {
	AvailableModes           []ModeDef
	AvailableThinkingEfforts []ThinkingEffortDef
	AvailableModels          []model.AgentModel
	AvailableCommands        []AvailableCommandInfo
	ConfigOptionState        *ConfigOptionState
	UpdatedAt                time.Time

	// refreshedInProcess marks whether this capability has already been
	// refreshed from the current agent process instance. Each agent process
	// should trigger exactly one full refresh (via ForceUpdate) when it first
	// establishes a session. Subsequent ResumeSession calls on the same
	// process instance skip the refresh. The marker is cleared when the
	// process dies (connection closed) so a new process triggers a fresh refresh.
	refreshedInProcess bool
}

// HasData returns true if the capability has any non-empty data.
func (c *AgentCapability) HasData() bool {
	return len(c.AvailableModes) > 0 ||
		len(c.AvailableThinkingEfforts) > 0 ||
		len(c.AvailableModels) > 0 ||
		len(c.AvailableCommands) > 0 ||
		c.ConfigOptionState != nil
}

// AgentCapabilityRegistry stores agent-level capabilities, keyed by agent ID.
// Thread-safe via sync.RWMutex. Capabilities are persisted to the agents
// table so they survive restarts without requiring prefetch.
type AgentCapabilityRegistry struct {
	mu   sync.RWMutex
	caps map[string]*AgentCapability // agentID -> capability
}

// Global singleton
var (
	globalCapabilityRegistry     *AgentCapabilityRegistry
	globalCapabilityRegistryOnce sync.Once
)

// GetAgentCapabilityRegistry returns the global AgentCapabilityRegistry singleton.
func GetAgentCapabilityRegistry() *AgentCapabilityRegistry {
	globalCapabilityRegistryOnce.Do(func() {
		globalCapabilityRegistry = &AgentCapabilityRegistry{
			caps: make(map[string]*AgentCapability),
		}
	})
	return globalCapabilityRegistry
}

// Get returns the capability for the given agent ID.
// Returns nil if no capability has been registered yet.
func (r *AgentCapabilityRegistry) Get(agentID string) *AgentCapability {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.caps[agentID]
}

// Update updates the capability for an agent and persists to DB.
// Only non-nil/non-empty fields are overwritten; others are preserved.
func (r *AgentCapabilityRegistry) Update(agentID string, cap *AgentCapability) {
	r.mu.Lock()
	existing, ok := r.caps[agentID]
	if !ok || existing == nil {
		r.caps[agentID] = cap
		r.mu.Unlock()
	} else {
		r.mu.Unlock()
		r.merge(agentID, cap)
	}
	r.persistAsync(agentID)
}

// merge overlays non-empty fields from src onto the existing capability.
func (r *AgentCapabilityRegistry) merge(agentID string, src *AgentCapability) {
	r.mu.Lock()
	defer r.mu.Unlock()

	existing, ok := r.caps[agentID]
	if !ok || existing == nil {
		r.caps[agentID] = src
		return
	}

	if len(src.AvailableModes) > 0 {
		existing.AvailableModes = src.AvailableModes
	}
	if len(src.AvailableThinkingEfforts) > 0 {
		existing.AvailableThinkingEfforts = src.AvailableThinkingEfforts
	}
	if len(src.AvailableModels) > 0 {
		existing.AvailableModels = src.AvailableModels
	}
	if len(src.AvailableCommands) > 0 {
		existing.AvailableCommands = src.AvailableCommands
	}
	if src.ConfigOptionState != nil {
		existing.ConfigOptionState = src.ConfigOptionState
	}
	existing.UpdatedAt = time.Now()
}

// UpdateModes updates only the available modes for an agent.
func (r *AgentCapabilityRegistry) UpdateModes(agentID string, modes []ModeDef) {
	r.Update(agentID, &AgentCapability{AvailableModes: modes})
}

// UpdateThinkingEfforts updates only the available thinking effort levels.
func (r *AgentCapabilityRegistry) UpdateThinkingEfforts(agentID string, levels []ThinkingEffortDef) {
	r.Update(agentID, &AgentCapability{AvailableThinkingEfforts: levels})
}

// UpdateModels updates only the available models.
func (r *AgentCapabilityRegistry) UpdateModels(agentID string, models []model.AgentModel) {
	r.Update(agentID, &AgentCapability{AvailableModels: models})
}

// UpdateCommands updates only the available commands.
func (r *AgentCapabilityRegistry) UpdateCommands(agentID string, cmds []AvailableCommandInfo) {
	r.Update(agentID, &AgentCapability{AvailableCommands: cmds})
}

// UpdateConfigState updates only the config option state.
func (r *AgentCapabilityRegistry) UpdateConfigState(agentID string, state *ConfigOptionState) {
	r.Update(agentID, &AgentCapability{ConfigOptionState: state})
}

// ForceUpdate replaces all capability fields for an agent (full overwrite, not merge)
// and persists to DB asynchronously. Used when a new agent process establishes its
// first session — the ACP response is the authoritative source of truth.
// Returns true if the update was applied, false if skipped because the current
// process instance already refreshed this agent.
func (r *AgentCapabilityRegistry) ForceUpdate(agentID string, cap *AgentCapability) bool {
	r.mu.Lock()
	existing, ok := r.caps[agentID]
	if ok && existing != nil && existing.refreshedInProcess {
		r.mu.Unlock()
		slog.Debug("acp capability: skipping ForceUpdate, already refreshed in this process", "agent", agentID)
		return false
	}
	cap.UpdatedAt = time.Now()
	cap.refreshedInProcess = true
	r.caps[agentID] = cap
	r.mu.Unlock()
	r.persistAsync(agentID)
	slog.Info("acp capability: ForceUpdate applied", "agent", agentID,
		"modes", len(cap.AvailableModes),
		"efforts", len(cap.AvailableThinkingEfforts),
		"models", len(cap.AvailableModels),
		"commands", len(cap.AvailableCommands))
	return true
}

// MarkStale clears the refreshedInProcess flag for an agent, indicating that
// the next ForceUpdate should be applied (e.g., after the agent process dies
// and a new one is spawned).
func (r *AgentCapabilityRegistry) MarkStale(agentID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	existing, ok := r.caps[agentID]
	if ok && existing != nil {
		existing.refreshedInProcess = false
	}
}

// ForceUpdateIfNeeded extracts agent-level capabilities from a NewSession/ResumeSession
// response and calls ForceUpdate. This is the single entry point for full capability
// refresh — called once when an ACP connection first establishes a session.
// The update is synchronous on the registry but DB persistence is async.
func (r *AgentCapabilityRegistry) ForceUpdateIfNeeded(agentID string, modes []ModeDef, efforts []ThinkingEffortDef, models []model.AgentModel, cmds []AvailableCommandInfo, configState *ConfigOptionState) bool {
	return r.ForceUpdate(agentID, &AgentCapability{
		AvailableModes:           modes,
		AvailableThinkingEfforts: efforts,
		AvailableModels:          models,
		AvailableCommands:        cmds,
		ConfigOptionState:        configState,
	})
}

// GetModeState returns a ModeState combining agent-level available modes
// with the session-level current mode ID.
func (r *AgentCapabilityRegistry) GetModeState(agentID, currentModeID string) *ModeState {
	r.mu.RLock()
	cap, ok := r.caps[agentID]
	r.mu.RUnlock()
	if !ok || cap == nil {
		return nil
	}
	if len(cap.AvailableModes) > 0 {
		return &ModeState{
			CurrentModeID:  currentModeID,
			AvailableModes: cap.AvailableModes,
		}
	}
	// ACP v2 agents (like OpenCode) report modes via ConfigOptionState
	// with Category "mode" instead of the legacy Modes field.
	if ms := modeStateFromConfigState(cap.ConfigOptionState); ms != nil {
		if currentModeID != "" {
			ms.CurrentModeID = currentModeID
		}
		return ms
	}
	return nil
}

// GetThinkingEffortState returns a ThinkingEffortState combining agent-level
// available levels with the session-level current ID.
func (r *AgentCapabilityRegistry) GetThinkingEffortState(agentID, currentID string) *ThinkingEffortState {
	r.mu.RLock()
	cap, ok := r.caps[agentID]
	r.mu.RUnlock()
	if !ok || cap == nil || len(cap.AvailableThinkingEfforts) == 0 {
		return nil
	}
	return &ThinkingEffortState{
		CurrentID:       currentID,
		AvailableLevels: cap.AvailableThinkingEfforts,
	}
}

// GetModelListState returns a ModelListState combining agent-level available
// models with the session-level current model ID.
func (r *AgentCapabilityRegistry) GetModelListState(agentID, currentModelID string) *ModelListState {
	r.mu.RLock()
	cap, ok := r.caps[agentID]
	r.mu.RUnlock()
	if !ok || cap == nil || len(cap.AvailableModels) == 0 {
		return nil
	}
	return &ModelListState{
		CurrentModelID: currentModelID,
		Models:         cap.AvailableModels,
	}
}

// GetCommands returns the available commands for an agent.
func (r *AgentCapabilityRegistry) GetCommands(agentID string) []AvailableCommandInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()
	cap, ok := r.caps[agentID]
	if !ok || cap == nil {
		return nil
	}
	return cap.AvailableCommands
}

// GetConfigState returns the config option state for an agent.
func (r *AgentCapabilityRegistry) GetConfigState(agentID string) *ConfigOptionState {
	r.mu.RLock()
	defer r.mu.RUnlock()
	cap, ok := r.caps[agentID]
	if !ok || cap == nil {
		return nil
	}
	return cap.ConfigOptionState
}

// HasAvailableModes checks whether an agent has available modes in the registry.
func (r *AgentCapabilityRegistry) HasAvailableModes(agentID string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	cap, ok := r.caps[agentID]
	return ok && cap != nil && len(cap.AvailableModes) > 0
}

// IsModeAvailable checks whether a specific mode ID exists in the agent's
// available modes. Used to validate agent-reported mode changes.
func (r *AgentCapabilityRegistry) IsModeAvailable(agentID, modeID string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	cap, ok := r.caps[agentID]
	if !ok || cap == nil {
		return false
	}
	for _, m := range cap.AvailableModes {
		if m.ID == modeID {
			return true
		}
	}
	return false
}

// HasNewAvailableModes returns true if the given modes list contains IDs
// not present in the registry's available modes for this agent.
func (r *AgentCapabilityRegistry) HasNewAvailableModes(agentID string, newModes []ModeDef) bool {
	r.mu.RLock()
	cap, ok := r.caps[agentID]
	r.mu.RUnlock()
	if !ok || cap == nil || len(cap.AvailableModes) == 0 {
		return len(newModes) > 0
	}
	seen := make(map[string]struct{}, len(cap.AvailableModes))
	for _, m := range cap.AvailableModes {
		seen[m.ID] = struct{}{}
	}
	for _, m := range newModes {
		if _, found := seen[m.ID]; !found {
			return true
		}
	}
	return false
}

// HasNewAvailableThinkingEfforts returns true if the given levels contain
// IDs not present in the registry.
func (r *AgentCapabilityRegistry) HasNewAvailableThinkingEfforts(agentID string, newLevels []ThinkingEffortDef) bool {
	r.mu.RLock()
	cap, ok := r.caps[agentID]
	r.mu.RUnlock()
	if !ok || cap == nil || len(cap.AvailableThinkingEfforts) == 0 {
		return len(newLevels) > 0
	}
	seen := make(map[string]struct{}, len(cap.AvailableThinkingEfforts))
	for _, l := range cap.AvailableThinkingEfforts {
		seen[l.ID] = struct{}{}
	}
	for _, l := range newLevels {
		if _, found := seen[l.ID]; !found {
			return true
		}
	}
	return false
}

// HasNewAvailableModels returns true if the given models contain IDs
// not present in the registry.
func (r *AgentCapabilityRegistry) HasNewAvailableModels(agentID string, newModels []model.AgentModel) bool {
	r.mu.RLock()
	cap, ok := r.caps[agentID]
	r.mu.RUnlock()
	if !ok || cap == nil || len(cap.AvailableModels) == 0 {
		return len(newModels) > 0
	}
	seen := make(map[string]struct{}, len(cap.AvailableModels))
	for _, m := range cap.AvailableModels {
		seen[m.ID] = struct{}{}
	}
	for _, m := range newModels {
		if _, found := seen[m.ID]; !found {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// DB Persistence
// ---------------------------------------------------------------------------

// dbHolder stores the DB reference for async persistence.
var dbHolder struct {
	mu sync.RWMutex
	db *sql.DB
}

// SetRegistryDB sets the database reference used for persistence.
// Called once during startup after InitDB.
func SetRegistryDB(db *sql.DB) {
	dbHolder.mu.Lock()
	defer dbHolder.mu.Unlock()
	dbHolder.db = db
}

func getRegistryDB() *sql.DB {
	dbHolder.mu.RLock()
	defer dbHolder.mu.RUnlock()
	return dbHolder.db
}

// persistAsync saves capabilities for a single agent to DB in a background goroutine.
func (r *AgentCapabilityRegistry) persistAsync(agentID string) {
	db := getRegistryDB()
	if db == nil {
		return
	}

	r.mu.RLock()
	cap, ok := r.caps[agentID]
	r.mu.RUnlock()
	if !ok || cap == nil {
		return
	}

	go func() {
		if err := r.saveToDB(db, agentID, cap); err != nil {
			slog.Warn("failed to persist agent capability to DB", "agent", agentID, "error", err)
		}
	}()
}

// saveToDB persists capabilities for a single agent to the agents table.
func (r *AgentCapabilityRegistry) saveToDB(db *sql.DB, agentID string, cap *AgentCapability) error {
	modesJSON, _ := json.Marshal(cap.AvailableModes)
	if string(modesJSON) == "null" {
		modesJSON = []byte("[]")
	}
	effortsJSON, _ := json.Marshal(cap.AvailableThinkingEfforts)
	if string(effortsJSON) == "null" {
		effortsJSON = []byte("[]")
	}
	cmdsJSON, _ := json.Marshal(cap.AvailableCommands)
	if string(cmdsJSON) == "null" {
		cmdsJSON = []byte("[]")
	}
	var configJSON string
	if cap.ConfigOptionState != nil {
		b, _ := json.Marshal(cap.ConfigOptionState)
		configJSON = string(b)
	}

	_, err := db.Exec(`
		UPDATE agents SET
			acp_available_modes = ?,
			acp_available_thinking_efforts = ?,
			acp_available_commands = ?,
			acp_config_options = ?,
			updated_at = CURRENT_TIMESTAMP
		WHERE id = ?`,
		string(modesJSON), string(effortsJSON), string(cmdsJSON), configJSON, agentID)
	return err
}

// LoadFromDB loads persisted capabilities from the agents table on startup.
func (r *AgentCapabilityRegistry) LoadFromDB(db *sql.DB) {
	rows, err := db.Query(`
		SELECT id, acp_available_modes, acp_available_thinking_efforts,
		       acp_available_commands, acp_config_options
		FROM agents
		WHERE transport = 'acp-stdio'
	`)
	if err != nil {
		slog.Warn("failed to load agent capabilities from DB", "error", err)
		return
	}
	defer func() { _ = rows.Close() }()

	r.mu.Lock()
	defer r.mu.Unlock()

	for rows.Next() {
		var agentID, modesJSON, effortsJSON, cmdsJSON, configJSON string
		if err := rows.Scan(&agentID, &modesJSON, &effortsJSON, &cmdsJSON, &configJSON); err != nil {
			slog.Warn("failed to scan agent capability row", "error", err)
			continue
		}

		cap := &AgentCapability{}

		if modesJSON != "" && modesJSON != "[]" {
			var modes []ModeDef
			if err := json.Unmarshal([]byte(modesJSON), &modes); err == nil {
				cap.AvailableModes = modes
			}
		}
		if effortsJSON != "" && effortsJSON != "[]" {
			var efforts []ThinkingEffortDef
			if err := json.Unmarshal([]byte(effortsJSON), &efforts); err == nil {
				cap.AvailableThinkingEfforts = efforts
			}
		}
		if cmdsJSON != "" && cmdsJSON != "[]" {
			var cmds []AvailableCommandInfo
			if err := json.Unmarshal([]byte(cmdsJSON), &cmds); err == nil {
				cap.AvailableCommands = cmds
			}
		}
		if configJSON != "" {
			var config ConfigOptionState
			if err := json.Unmarshal([]byte(configJSON), &config); err == nil {
				cap.ConfigOptionState = &config
			}
		}

		if cap.HasData() {
			cap.UpdatedAt = time.Now()
			r.caps[agentID] = cap
			slog.Info("loaded agent capability from DB",
				"agent", agentID,
				"modes", len(cap.AvailableModes),
				"efforts", len(cap.AvailableThinkingEfforts),
				"commands", len(cap.AvailableCommands))
		}
	}
}