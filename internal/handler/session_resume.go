package handler

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	acp "github.com/coder/acp-go-sdk"

	"clawbench/internal/ai"
	"clawbench/internal/middleware"
	"clawbench/internal/model"
	"clawbench/internal/service"
)

// ServeSessionResume handles POST /api/ai/session/resume — restores a soft-deleted
// session and returns the session ID. Validates project ownership and session count limits.
func ServeSessionResume(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}

	projectPath := middleware.GetProjectFromCookie(r)
	if projectPath == "" {
		writeLocalizedError(w, r, model.Forbidden(nil, "NoProjectSelected"))
		return
	}

	var req struct {
		SessionID string `json:"session_id"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.SessionID == "" {
		writeLocalizedErrorf(w, r, http.StatusBadRequest, "SessionIdRequired")
		return
	}

	// Check session exists and belongs to project
	var sessionProjectPath string
	var deleted int
	err := service.DBRead.QueryRowContext(
		r.Context(),
		"SELECT project_path, deleted FROM chat_sessions WHERE id = ?",
		req.SessionID,
	).Scan(&sessionProjectPath, &deleted)
	if errors.Is(err, sql.ErrNoRows) {
		writeLocalizedErrorf(w, r, http.StatusNotFound, "SessionNotFound")
		return
	}
	if err != nil {
		model.WriteError(w, model.Internal(err))
		return
	}

	// Project isolation
	if sessionProjectPath != projectPath {
		writeLocalizedError(w, r, model.Forbidden(nil, "AccessDenied"))
		return
	}

	// If soft-deleted, check session count limit before restoring
	if deleted == 1 {
		if model.SessionMaxCount > 0 {
			var count int
			err = service.DBRead.QueryRowContext(
				r.Context(),
				"SELECT COUNT(*) FROM chat_sessions WHERE project_path = ? AND deleted = 0 AND session_type = 'chat'",
				sessionProjectPath,
			).Scan(&count)
			if err != nil {
				model.WriteError(w, model.Internal(err))
				return
			}
			// Restoring a soft-deleted session would increase active count by 1
			if count+1 > model.SessionMaxCount {
				writeLocalizedErrorf(w, r, http.StatusConflict, "SessionLimitReached", map[string]any{
					"Count": count,
					"Limit": model.SessionMaxCount,
				})
				return
			}
		}

		// Restore the session
		_, err = service.DB.ExecContext(
			r.Context(),
			"UPDATE chat_sessions SET deleted = 0, updated_at = CURRENT_TIMESTAMP WHERE id = ?",
			req.SessionID,
		)
		if err != nil {
			model.WriteError(w, model.Internal(fmt.Errorf("failed to restore session %s: %w", req.SessionID, err)))
			return
		}
		slog.Info("session restored from soft-delete",
			slog.String("session", req.SessionID),
			slog.String("project", sessionProjectPath))
	} else {
		slog.Info("session resume requested (already active)",
			slog.String("session", req.SessionID),
			slog.String("project", sessionProjectPath))
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"ok":         true,
		"session_id": req.SessionID,
	})
}

// ServeACPLoadSession handles POST /api/ai/session/acp-load — creates a new ClawBench
// session by loading an existing ACP session via LoadSession. The agent replays the
// full conversation history which is collected and saved to chat_history.
func ServeACPLoadSession(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}

	projectPath := middleware.GetProjectFromCookie(r)
	if projectPath == "" {
		writeLocalizedError(w, r, model.Forbidden(nil, "NoProjectSelected"))
		return
	}

	var req struct {
		AgentID       string `json:"agentId"`
		AcpSessionID  string `json:"acpSessionId"`
		ProjectID     string `json:"projectId"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.AgentID == "" || req.AcpSessionID == "" {
		writeLocalizedErrorf(w, r, http.StatusBadRequest, "InvalidRequestBody")
		return
	}

	// Validate agent exists and supports LoadSession
	configMutex.RLock()
	agent, ok := model.Agents[req.AgentID]
	configMutex.RUnlock()

	if !ok {
		writeLocalizedErrorf(w, r, http.StatusNotFound, "AgentNotFound")
		return
	}

	if agent.Transport != transportACP {
		writeLocalizedErrorf(w, r, http.StatusBadRequest, "InvalidRequestBody")
		return
	}

	reg := ai.GetAgentCapabilityRegistry()
	if !reg.GetLoadSession(req.AgentID) {
		writeLocalizedErrorf(w, r, http.StatusNotImplemented, "NotImplemented")
		return
	}

	// Create new ClawBench session
	sessionID, err := service.CreateSession(projectPath, agent.Backend, "", req.AgentID, "", "default", "chat")
	if err != nil {
		slog.Error("handler: failed to create session for acp-load", "error", err)
		writeLocalizedErrorf(w, r, http.StatusInternalServerError, "InternalError")
		return
	}

	// Set SourceSessionID to track the ACP session origin
	if err := service.UpdateSessionSourceID(sessionID, "acp:"+req.AcpSessionID); err != nil {
		slog.Warn("handler: failed to update source_session_id", "session_id", sessionID, "error", err)
	}

	// Load ACP session via connection manager
	mgr := ai.GetACPConnManager()
	conn, err := mgr.GetOrCreateConnForLoad(r.Context(), agent, sessionID, req.AcpSessionID, projectPath)
	if err != nil {
		slog.Error("handler: LoadSession failed", "agent", req.AgentID, "acp_sid", req.AcpSessionID, "error", err)
		// Clean up the session we just created
		_ = service.DeleteSession(projectPath, agent.Backend, sessionID)
		writeLocalizedErrorf(w, r, http.StatusInternalServerError, "InternalError")
		return
	}

	// Collect replayed messages from the buffer
	client := conn.GetClient()
	var messages []model.ChatMessage
	if client != nil {
		buf := client.GetAndClearLoadSessionBuf()
		for _, n := range buf {
			msgs := convertACPSessionUpdateToMessages(n, sessionID, agent.Backend)
			messages = append(messages, msgs...)
		}
	}

	// Batch insert replay messages to chat_history
	if len(messages) > 0 {
		for _, msg := range messages {
			contentJSON, _ := json.Marshal(map[string]any{
				"blocks": []map[string]any{{"type": "text", "text": msg.Content}},
			})
			_, err := service.DB.Exec(
				"INSERT INTO chat_history (project_path, backend, session_id, role, content, streaming, indexed) VALUES (?, ?, ?, ?, ?, 0, 0)",
				projectPath, msg.Backend, msg.SessionID, msg.Role, string(contentJSON),
			)
			if err != nil {
				slog.Error("handler: failed to save LoadSession replay message", "error", err)
			}
		}
	}

	slog.Info("handler: acp-load completed",
		"session_id", sessionID,
		"agent", req.AgentID,
		"acp_sid", req.AcpSessionID,
		"messages", len(messages))

	writeJSON(w, http.StatusOK, map[string]any{
		"sessionId": sessionID,
	})
}

// convertACPSessionUpdateToMessages converts a single ACP SessionUpdate notification
// into ClawBench chat messages for persistence.
func convertACPSessionUpdateToMessages(n acp.SessionNotification, sessionID, backend string) []model.ChatMessage {
	// Store the raw JSON of the update as an assistant message.
	// The frontend will parse the full content when displaying the session.
	// A more sophisticated conversion can extract individual message blocks
	// (user/assistant/tool) from the SessionUpdate variants.
	var messages []model.ChatMessage

	role := "assistant"
	if n.Update.UserMessageChunk != nil {
		role = "user"
	}

	content, _ := json.Marshal(n.Update)
	messages = append(messages, model.ChatMessage{
		SessionID: sessionID,
		Role:      role,
		Content:   string(content),
		Backend:   backend,
	})

	return messages
}
