package server

import "net/http"

func (h *Handler) openAPI(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(openAPIDocument)
}

func (h *Handler) swaggerUI(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(swaggerHTML))
}

const swaggerHTML = `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>op API</title>
  <link rel="stylesheet" href="https://unpkg.com/swagger-ui-dist@5/swagger-ui.css">
</head>
<body>
  <div id="swagger-ui"></div>
  <script src="https://unpkg.com/swagger-ui-dist@5/swagger-ui-bundle.js"></script>
  <script>window.onload = () => SwaggerUIBundle({url: '/openapi.json', dom_id: '#swagger-ui'});</script>
</body>
</html>`

var openAPIDocument = []byte(`{
  "openapi": "3.0.3",
  "info": {"title": "op remote-control API", "version": "1.0.0", "description": "Opening a project executes configured commands on the host."},
  "servers": [{"url": "/"}],
  "components": {
    "securitySchemes": {"bearerAuth": {"type": "http", "scheme": "bearer"}},
    "schemas": {
      "StateSnapshot": {"type":"object","required":["version","instanceId","hostname","epoch","revision","capturedAt","projects","tmux","agents","host"],"properties":{
        "version":{"type":"integer","enum":[1]},"instanceId":{"type":"string"},"hostname":{"type":"string"},"epoch":{"type":"string","description":"Random per-process epoch; retire prior epochs on replacement."},"revision":{"type":"integer","format":"int64","minimum":0},"capturedAt":{"type":"string","format":"date-time"},
        "projects":{"allOf":[{"$ref":"#/components/schemas/StateSection"},{"type":"object","properties":{"data":{"type":"array","items":{"$ref":"#/components/schemas/StateProject"}}}}]},
        "tmux":{"allOf":[{"$ref":"#/components/schemas/StateSection"},{"type":"object","properties":{"data":{"type":"array","items":{"$ref":"#/components/schemas/StateWindow"}}}}]},
        "agents":{"allOf":[{"$ref":"#/components/schemas/StateSection"},{"type":"object","properties":{"data":{"type":"array","items":{"$ref":"#/components/schemas/StateAgent"}}}}]},
        "host":{"allOf":[{"$ref":"#/components/schemas/StateSection"},{"type":"object","properties":{"data":{"type":"object","properties":{"cpuPercent":{"type":"number"},"memoryUsed":{"type":"integer"},"memoryTotal":{"type":"integer"},"loadAverage":{"type":"array","items":{"type":"number"},"minItems":3,"maxItems":3},"uptimeSeconds":{"type":"integer"}}}}}]}
      }},
      "StateSection":{"type":"object","required":["data","available","stale","attemptedAt","updatedAt"],"description":"Data is retained on failure. available means a sample has succeeded at least once; stale/error distinguish failed or aged evidence from successful emptiness. Agent-section freshness describes pane detection; native agents carry individual freshness.","properties":{"available":{"type":"boolean"},"stale":{"type":"boolean"},"attemptedAt":{"type":"string","format":"date-time"},"updatedAt":{"type":"string","format":"date-time"},"error":{"type":"string"}}},
      "StateProject":{"type":"object","required":["id","nativeId","name","path","kind","gitState"],"properties":{"id":{"type":"string","description":"Instance-namespaced canonical-path identity."},"nativeId":{"type":"string","description":"Op catalog ID for existing project APIs; NOT an agent-native project ID."},"name":{"type":"string"},"path":{"type":"string"},"branch":{"type":"string"},"kind":{"type":"string","enum":["repository","worktree","custom_entry"]},"gitState":{"type":"string"},"tags":{"type":"array","items":{"type":"string"}}}},
      "StateWindow":{"type":"object","required":["id","nativeId","sessionId","nativeSessionId","sessionName","mode","panes"],"description":"Observed managed tmux window, including the dashboard. A projectId identifies a tracked project instance. Multiple windows/profiles remain distinct. GUI lifecycles are untracked.","properties":{"id":{"type":"string"},"nativeId":{"type":"string"},"sessionId":{"type":"string"},"nativeSessionId":{"type":"string"},"sessionName":{"type":"string"},"sessionAttached":{"type":"boolean"},"name":{"type":"string"},"index":{"type":"integer"},"active":{"type":"boolean"},"projectId":{"type":"string"},"profile":{"type":"string"},"mode":{"type":"string","enum":["tmux"]},"panes":{"type":"array","items":{"$ref":"#/components/schemas/StatePane"}}}},
      "StatePane":{"type":"object","properties":{"id":{"type":"string"},"nativeId":{"type":"string"},"projectId":{"type":"string"},"index":{"type":"integer"},"pid":{"type":"integer"},"currentCommand":{"type":"string"},"currentPath":{"type":"string"},"active":{"type":"boolean"},"dead":{"type":"boolean"}}},
      "StateAgent":{"type":"object","required":["id","nativeId","source","provenance","activity","observedAt","stale","terminated"],"properties":{"id":{"type":"string"},"nativeId":{"type":"string","description":"Native session ID for native evidence; local pane ID for detector evidence."},"source":{"type":"string"},"provenance":{"type":"string","enum":["pane-detector","native-event"]},"coverage":{"type":"string","enum":["native","hooks","notification-only"]},"projectId":{"type":"string","description":"Namespaced op catalog association using longest canonical directory ancestor."},"nativeProjectId":{"type":"string","description":"Agent-native project identity, never assumed equal to an op catalog ID."},"directory":{"type":"string"},"paneId":{"type":"string"},"foregroundPid":{"type":"integer"},"activity":{"type":"string","enum":["starting","working","awaiting_input","permission_required","awaiting_approval","idle","unknown"]},"detail":{"type":"string"},"observedAt":{"type":"string","format":"date-time"},"stale":{"type":"boolean"},"terminated":{"type":"boolean","description":"Only explicit native termination evidence sets true; silence/eviction is not termination."},"notification":{"$ref":"#/components/schemas/StateNotification"}}},
      "StateNotification":{"type":"object","description":"Last attention notification accompanying this observation. Replaced/cleared by newer activity evidence.","properties":{"type":{"type":"string","enum":["idle","question","permission"]},"source":{"type":"string"},"sessionId":{"type":"string"},"sessionTitle":{"type":"string"},"projectId":{"type":"string","description":"Original agent-native ID."},"projectDirectory":{"type":"string"},"desktopUrl":{"type":"string"},"timestamp":{"type":"string","format":"date-time"},"hostname":{"type":"string"},"hops":{"type":"integer"},"question":{"type":"string"},"permissionTitle":{"type":"string"},"permissionType":{"type":"string"},"choices":{"type":"array","items":{"type":"object","properties":{"label":{"type":"string"},"description":{"type":"string"}}}}}},
      "StateEvent":{"type":"object","required":["version","instanceId","epoch","sequence","eventId","occurredAt","type","payload"],"description":"Child POST to the exact configured parentUrl, authenticated with parentToken/OP_PARENT_TOKEN. Every event is a full replacement and heartbeat. Accept with 2xx; retries use identical bytes and IDs. Deduplicate IDs and ignore lower sequences in an epoch. Sequence gaps reconcile from this payload. A new epoch replaces old state; retire earlier epochs. No receiver route is hosted by op.","properties":{"version":{"type":"integer","enum":[1]},"instanceId":{"type":"string"},"epoch":{"type":"string"},"sequence":{"type":"integer","format":"int64","minimum":1},"eventId":{"type":"string"},"occurredAt":{"type":"string","format":"date-time"},"type":{"type":"string","enum":["state.snapshot"]},"payload":{"$ref":"#/components/schemas/StateSnapshot"}}},
      "Error": {"type": "object", "required": ["code", "message"], "properties": {"code": {"type": "string"}, "operation": {"type": "string"}, "field": {"type": "string"}, "resource": {"type": "string"}, "message": {"type": "string"}}},
      "Job": {"type": "object", "required": ["id", "kind", "status", "createdAt"], "properties": {"id": {"type": "string"}, "kind": {"type": "string"}, "status": {"type": "string", "enum": ["queued", "running", "succeeded", "failed", "canceled"]}, "createdAt": {"type": "string", "format": "date-time"}, "startedAt": {"type": "string", "format": "date-time"}, "finishedAt": {"type": "string", "format": "date-time"}, "projectId": {"type": "string"}, "result": {"type": "object"}, "error": {"$ref": "#/components/schemas/Error"}}}
    }
  },
  "paths": {
    "/v1/state":{"get":{"security":[{"bearerAuth":[]}],"summary":"Read cached reconcilable instance state","description":"Sampler runs in op serve without a dashboard. Reads evaluate freshness without advancing the revision or detector baseline. Before the initial sample, sections are unavailable/stale. See StateEvent for child-initiated delivery to a configured parent.","responses":{"200":{"description":"Full current state","content":{"application/json":{"schema":{"$ref":"#/components/schemas/StateSnapshot"}}}},"401":{"description":"Unauthorized"},"503":{"description":"Sampler not configured"}}}},
    "/v1/health": {"get": {"security": [{"bearerAuth": []}], "summary": "Authenticated dependency health and version", "responses": {"200": {"description": "Healthy"}, "401": {"description": "Unauthorized"}}}},
    "/v1/projects": {
      "get": {"security": [{"bearerAuth": []}], "summary": "List projects", "responses": {"200": {"description": "Project list"}, "401": {"description": "Unauthorized"}}},
      "post": {"security": [{"bearerAuth": []}], "summary": "Create a project", "requestBody": {"required": true, "content": {"application/json": {"schema": {"type": "object", "required": ["name"], "properties": {"name": {"type": "string"}, "openOnFinish": {"type": "boolean"}, "profile": {"type": "string"}}}}}}, "responses": {"201": {"description": "Created"}}}
    },
    "/v1/projects/clone": {"post": {"security": [{"bearerAuth": []}], "summary": "Queue a clone", "parameters": [{"name": "Idempotency-Key", "in": "header", "schema": {"type": "string", "maxLength": 255}, "description": "Replays the original job when reused with the same payload"}], "requestBody": {"required": true, "content": {"application/json": {"schema": {"type": "object", "required": ["url"], "properties": {"url": {"type": "string"}, "directory": {"type": "string"}, "openOnFinish": {"type": "boolean"}, "profile": {"type": "string"}}}}}}, "responses": {"202": {"description": "Queued or replayed", "content": {"application/json": {"schema": {"$ref": "#/components/schemas/Job"}}}}, "409": {"description": "Idempotency key payload mismatch"}}}},
    "/v1/projects/{id}/open": {"post": {"security": [{"bearerAuth": []}], "summary": "Open a project with a configured profile", "parameters": [{"name": "id", "in": "path", "required": true, "schema": {"type": "string"}}], "requestBody": {"required": true, "content": {"application/json": {"schema": {"type": "object", "properties": {"profile": {"type": "string"}, "newInstance": {"type": "boolean", "description": "Tmux profiles only"}}}}}}, "responses": {"200": {"description": "Opened"}}}},
    "/v1/projects/{id}/worktrees": {"post": {"security": [{"bearerAuth": []}], "summary": "Queue a worktree", "parameters": [{"name": "id", "in": "path", "required": true, "schema": {"type": "string"}}, {"name": "Idempotency-Key", "in": "header", "schema": {"type": "string", "maxLength": 255}, "description": "Replays the original job when reused with the same payload"}], "requestBody": {"required": true, "content": {"application/json": {"schema": {"type": "object", "required": ["branch"], "properties": {"branch": {"type": "string"}, "directory": {"type": "string"}, "openOnFinish": {"type": "boolean"}, "profile": {"type": "string"}}}}}}, "responses": {"202": {"description": "Queued or replayed", "content": {"application/json": {"schema": {"$ref": "#/components/schemas/Job"}}}}, "409": {"description": "Idempotency key payload mismatch"}}}},
    "/v1/tmux": {"get": {"security": [{"bearerAuth": []}], "summary": "Get tmux snapshot", "responses": {"200": {"description": "Snapshot"}}}},
    "/v1/jobs/{id}": {"get": {"security": [{"bearerAuth": []}], "summary": "Get a job", "parameters": [{"name": "id", "in": "path", "required": true, "schema": {"type": "string"}}], "responses": {"200": {"description": "Job", "content": {"application/json": {"schema": {"$ref": "#/components/schemas/Job"}}}}, "404": {"description": "Not found"}}}},
    "/v1/notify": {"post": {"security": [{"bearerAuth": []}], "summary": "Ingest a normalized notification", "responses": {"200": {"description": "Accepted"}, "401": {"description": "Unauthorized"}}}},
    "/v1/claude-code/hook": {"post": {"security": [{"bearerAuth": []}], "summary": "Ingest a Claude Code hook payload", "responses": {"200": {"description": "Accepted"}, "401": {"description": "Unauthorized"}}}},
    "/v1/grok-code/hook": {"post": {"security": [{"bearerAuth": []}], "summary": "Ingest a Grok hook payload", "responses": {"200": {"description": "Accepted"}, "401": {"description": "Unauthorized"}}}},
    "/v1/codex/hook": {"post": {"security": [{"bearerAuth": []}], "summary": "Ingest a Codex hook payload", "responses": {"200": {"description": "Accepted"}, "401": {"description": "Unauthorized"}}}},
    "/v1/copilot-cli/hook": {"post": {"security": [{"bearerAuth": []}], "summary": "Ingest a Copilot CLI hook payload", "responses": {"200": {"description": "Accepted"}, "401": {"description": "Unauthorized"}}}}
  }
}
`)
