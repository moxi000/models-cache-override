package main

import (
	_ "embed"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

//go:embed web/index.html
var dashboard []byte

func managementRegistration() map[string]any {
	return map[string]any{
		"routes": []map[string]string{
			{"Method": "GET", "Path": "/models-cache-override/document"},
			{"Method": "PUT", "Path": "/models-cache-override/document"},
			{"Method": "POST", "Path": "/models-cache-override/refresh"},
		},
		"resources": []map[string]string{{
			"Path":        "/status",
			"Menu":        "模型目录",
			"Description": "查看即将下发的模型 JSON，并覆写或追加字段。",
		}},
	}
}

func handleManagement(raw []byte) ([]byte, error) {
	var req pluginapi.ManagementRequest
	if len(raw) > 0 {
		if errDecode := json.Unmarshal(raw, &req); errDecode != nil {
			return nil, errDecode
		}
	}
	switch {
	case strings.HasSuffix(req.Path, "/status"):
		if req.Method != http.MethodGet {
			return managementJSON(http.StatusMethodNotAllowed, map[string]string{"error": "method"})
		}
		return okEnvelope(pluginapi.ManagementResponse{
			StatusCode: http.StatusOK,
			Headers:    http.Header{"Content-Type": {"text/html; charset=utf-8"}, "Cache-Control": {"no-store"}},
			Body:       dashboard,
		})
	case strings.HasSuffix(req.Path, "/refresh"):
		if req.Method != http.MethodPost {
			return managementJSON(http.StatusMethodNotAllowed, map[string]string{"error": "method"})
		}
		if errRefresh := state.refreshUpstream(); errRefresh != nil {
			return managementJSON(http.StatusBadGateway, map[string]string{"error": errRefresh.Error()})
		}
		return documentResponse()
	case strings.HasSuffix(req.Path, "/document"):
		switch req.Method {
		case http.MethodGet:
			return documentResponse()
		case http.MethodPut:
			var body struct {
				Action string          `json:"action"`
				Slug   string          `json:"slug"`
				Model  json.RawMessage `json:"model"`
			}
			if errDecode := json.Unmarshal(req.Body, &body); errDecode != nil || body.Action == "" {
				return managementJSON(http.StatusBadRequest, map[string]string{"error": "需要 action"})
			}
			if errSave := state.editModel(body.Action, body.Slug, body.Model); errSave != nil {
				return managementJSON(http.StatusBadRequest, map[string]string{"error": errSave.Error()})
			}
			return documentResponse()
		default:
			return managementJSON(http.StatusMethodNotAllowed, map[string]string{"error": "method"})
		}
	default:
		return managementJSON(http.StatusNotFound, map[string]string{"error": "not found"})
	}
}

func documentResponse() ([]byte, error) {
	models, removed, source := state.modelViews()
	state.mu.Lock()
	defer state.mu.Unlock()
	payload := map[string]any{
		"source":       source,
		"captured_at":  formatTime(state.capturedAt),
		"upstream_at":  formatTime(state.upstreamAt),
		"upstream_url": state.upstreamURL,
		"patch_count":  len(state.patches.Models),
		"models":       models,
		"removed":      removed,
	}
	return managementJSON(http.StatusOK, payload)
}

func formatTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339)
}

func managementJSON(status int, payload any) ([]byte, error) {
	body, errMarshal := json.Marshal(payload)
	if errMarshal != nil {
		return nil, errMarshal
	}
	return okEnvelope(pluginapi.ManagementResponse{
		StatusCode: status,
		Headers:    http.Header{"Content-Type": {"application/json; charset=utf-8"}, "Cache-Control": {"no-store"}},
		Body:       body,
	})
}
