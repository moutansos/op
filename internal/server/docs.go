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
      "Error": {"type": "object", "required": ["code", "message"], "properties": {"code": {"type": "string"}, "operation": {"type": "string"}, "field": {"type": "string"}, "resource": {"type": "string"}, "message": {"type": "string"}}},
      "Job": {"type": "object", "required": ["id", "kind", "status", "createdAt"], "properties": {"id": {"type": "string"}, "kind": {"type": "string"}, "status": {"type": "string", "enum": ["queued", "running", "succeeded", "failed", "canceled"]}, "createdAt": {"type": "string", "format": "date-time"}, "startedAt": {"type": "string", "format": "date-time"}, "finishedAt": {"type": "string", "format": "date-time"}, "projectId": {"type": "string"}, "result": {"type": "object"}, "error": {"$ref": "#/components/schemas/Error"}}}
    }
  },
  "paths": {
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
