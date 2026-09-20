package notify

import (
	"time"

	"github.com/moutansos/op/internal/domain"
)

// Only explicit lifecycle hook names carry state evidence. Tool arguments alone
// do not imply a running tool, and a denied permission does not imply resumption.
func hookObservation(source Source, payload map[string]any) (Observation, bool) {
	if hookString(payload, "agent_id", "agentId") != "" {
		return Observation{}, false
	}
	id := hookString(payload, "session_id", "sessionId")
	if id == "" {
		return Observation{}, false
	}
	event := normalizeEventName(hookString(payload, "hook_event_name", "hookEventName"))
	observation := Observation{Source: source, SessionID: id,
		ProjectID:        hookString(payload, "project_id", "projectId"),
		ProjectDirectory: hookString(payload, "cwd", "workspaceRoot"),
		Timestamp:        time.Now(), Detail: event, Coverage: CoverageHooks}
	switch source {
	case SourceClaudeCode, SourceCopilotCLI:
		switch event {
		case "user_prompt_submit", "userpromptsubmit", "user_prompt_submitted", "pre_tool_use", "pretooluse":
			observation.Activity = domain.AgentActivityWorking
		case "session_start", "sessionstart":
			observation.Activity = domain.AgentActivityStarting
		case "session_end", "sessionend":
			observation.Activity = domain.AgentActivityUnknown
			observation.Terminated = true
		default:
			return Observation{}, false
		}
	default:
		return Observation{}, false
	}
	return observation, true
}
