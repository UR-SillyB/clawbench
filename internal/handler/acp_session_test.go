package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"clawbench/internal/ai"
	"clawbench/internal/model"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- ServeACPSessions ---

func TestServeACPSessions_AgentNotFound(t *testing.T) {
	env, teardown := setupTestEnv(t)
	defer teardown()

	// No agents configured
	model.Agents = map[string]*model.Agent{}
	model.AgentList = []*model.Agent{}

	req := newRequest(t, http.MethodGet, "/api/agents/nonexistent/acp-sessions", nil)
	req = withProjectCookie(req, env.ProjectDir)
	w := httptest.NewRecorder()
	ServeACPSessions(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestServeACPSessions_NonACPTransport(t *testing.T) {
	env, teardown := setupTestEnv(t)
	defer teardown()

	model.Agents = map[string]*model.Agent{
		"cli-agent": {ID: "cli-agent", Backend: "claude", Transport: "cli"},
	}
	model.AgentList = []*model.Agent{model.Agents["cli-agent"]}

	req := newRequest(t, http.MethodGet, "/api/agents/cli-agent/acp-sessions", nil)
	req = withProjectCookie(req, env.ProjectDir)
	w := httptest.NewRecorder()
	ServeACPSessions(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestServeACPSessions_NoLoadSessionCapability(t *testing.T) {
	env, teardown := setupTestEnv(t)
	defer teardown()

	agentID := "acp-no-load"
	model.Agents = map[string]*model.Agent{
		agentID: {ID: agentID, Backend: "acp-stdio", Transport: "acp-stdio", AcpCommand: "echo"},
	}
	model.AgentList = []*model.Agent{model.Agents[agentID]}

	// No capabilities registered — LoadSession defaults to false
	reg := ai.GetAgentCapabilityRegistry()
	reg.Update(agentID, &ai.AgentCapability{AvailableModes: []ai.ModeDef{{ID: "code"}}})

	req := newRequest(t, http.MethodGet, "/api/agents/"+agentID+"/acp-sessions", nil)
	req = withProjectCookie(req, env.ProjectDir)
	w := httptest.NewRecorder()
	ServeACPSessions(w, req)

	assert.Equal(t, http.StatusNotImplemented, w.Code)
}

func TestServeACPSessions_NoAliveConnection(t *testing.T) {
	env, teardown := setupTestEnv(t)
	defer teardown()

	agentID := "acp-no-conn"
	model.Agents = map[string]*model.Agent{
		agentID: {ID: agentID, Backend: "acp-stdio", Transport: "acp-stdio", AcpCommand: "echo"},
	}
	model.AgentList = []*model.Agent{model.Agents[agentID]}

	// Register capability but no active connection
	reg := ai.GetAgentCapabilityRegistry()
	ls := true
	lss := true
	reg.Update(agentID, &ai.AgentCapability{LoadSession: &ls, ListSessions: &lss})

	req := newRequest(t, http.MethodGet, "/api/agents/"+agentID+"/acp-sessions", nil)
	req = withProjectCookie(req, env.ProjectDir)
	w := httptest.NewRecorder()
	ServeACPSessions(w, req)

	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
}

// --- ServeACPLoadSession ---

func TestServeACPLoadSession_InvalidRequestBody(t *testing.T) {
	env, teardown := setupTestEnv(t)
	defer teardown()

	// Missing required fields
	req := newRequest(t, http.MethodPost, "/api/ai/session/acp-load", map[string]string{"agentId": ""})
	req = withProjectCookie(req, env.ProjectDir)
	w := httptest.NewRecorder()
	ServeACPLoadSession(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestServeACPLoadSession_AgentNotFound(t *testing.T) {
	env, teardown := setupTestEnv(t)
	defer teardown()

	model.Agents = map[string]*model.Agent{}
	model.AgentList = []*model.Agent{}

	req := newRequest(t, http.MethodPost, "/api/ai/session/acp-load", map[string]string{
		"agentId":      "nonexistent",
		"acpSessionId": "acp-123",
	})
	req = withProjectCookie(req, env.ProjectDir)
	w := httptest.NewRecorder()
	ServeACPLoadSession(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestServeACPLoadSession_NonACPTransport(t *testing.T) {
	env, teardown := setupTestEnv(t)
	defer teardown()

	model.Agents = map[string]*model.Agent{
		"cli-agent": {ID: "cli-agent", Backend: "claude", Transport: "cli"},
	}
	model.AgentList = []*model.Agent{model.Agents["cli-agent"]}

	req := newRequest(t, http.MethodPost, "/api/ai/session/acp-load", map[string]string{
		"agentId":      "cli-agent",
		"acpSessionId": "acp-123",
	})
	req = withProjectCookie(req, env.ProjectDir)
	w := httptest.NewRecorder()
	ServeACPLoadSession(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestServeACPLoadSession_NoLoadSessionCapability(t *testing.T) {
	env, teardown := setupTestEnv(t)
	defer teardown()

	agentID := "acp-no-load"
	model.Agents = map[string]*model.Agent{
		agentID: {ID: agentID, Backend: "acp-stdio", Transport: "acp-stdio", AcpCommand: "echo"},
	}
	model.AgentList = []*model.Agent{model.Agents[agentID]}

	req := newRequest(t, http.MethodPost, "/api/ai/session/acp-load", map[string]string{
		"agentId":      agentID,
		"acpSessionId": "acp-123",
	})
	req = withProjectCookie(req, env.ProjectDir)
	w := httptest.NewRecorder()
	ServeACPLoadSession(w, req)

	assert.Equal(t, http.StatusNotImplemented, w.Code)
}

// --- Agents GET response includes LoadSession/ListSessions ---

func TestServeAgentsGet_IncludesLoadSessionCapability(t *testing.T) {
	env, teardown := setupTestEnv(t)
	defer teardown()

	agentID := "acp-with-load"
	model.Agents = map[string]*model.Agent{
		agentID: {ID: agentID, Backend: "acp-stdio", Transport: "acp-stdio", AcpCommand: "echo"},
	}
	model.AgentList = []*model.Agent{model.Agents[agentID]}

	// Register LoadSession/ListSessions capability
	reg := ai.GetAgentCapabilityRegistry()
	ls := true
	lss := true
	reg.Update(agentID, &ai.AgentCapability{LoadSession: &ls, ListSessions: &lss})

	req := newRequest(t, http.MethodGet, "/api/agents", nil)
	req = withProjectCookie(req, env.ProjectDir)
	w := httptest.NewRecorder()
	ServeAgents(w, req)

	require.Equal(t, http.StatusOK, w.Code)

	var resp map[string]any
	err := json.Unmarshal(w.Body.Bytes(), &resp)
	require.NoError(t, err)

	states, ok := resp["acpStates"].(map[string]any)
	require.True(t, ok, "acpStates should be present")

	agentState, ok := states[agentID].(map[string]any)
	require.True(t, ok, "agent state should be present")

	assert.Equal(t, true, agentState["loadSession"])
	assert.Equal(t, true, agentState["listSessions"])
}

func TestServeAgentsGet_NoCapabilityOmitsFields(t *testing.T) {
	env, teardown := setupTestEnv(t)
	defer teardown()

	agentID := "acp-no-cap"
	model.Agents = map[string]*model.Agent{
		agentID: {ID: agentID, Backend: "acp-stdio", Transport: "acp-stdio", AcpCommand: "echo"},
	}
	model.AgentList = []*model.Agent{model.Agents[agentID]}

	// No capabilities registered

	req := newRequest(t, http.MethodGet, "/api/agents", nil)
	req = withProjectCookie(req, env.ProjectDir)
	w := httptest.NewRecorder()
	ServeAgents(w, req)

	require.Equal(t, http.StatusOK, w.Code)

	var resp map[string]any
	err := json.Unmarshal(w.Body.Bytes(), &resp)
	require.NoError(t, err)

	states, ok := resp["acpStates"].(map[string]any)
	if !ok {
		// No acpStates at all — also fine
		return
	}
	// If agent has state, loadSession/listSessions should be false
	if agentState, ok := states[agentID].(map[string]any); ok {
		assert.Equal(t, false, agentState["loadSession"])
		assert.Equal(t, false, agentState["listSessions"])
	}
}
