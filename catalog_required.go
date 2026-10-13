package main

import (
	"encoding/json"
	"strings"
)

// catalogField is a Codex models_cache key that must survive an override.
// Codex 0.162 rejects the entire catalog when any model omits one of these.
// nullable fields may be JSON null; the key itself still has to be present.
type catalogField struct {
	path     string
	nullable bool
}

var catalogRequiredFields = []catalogField{
	{path: "description", nullable: true},
	{path: "supported_reasoning_levels"},
	{path: "shell_type"},
	{path: "visibility"},
	{path: "supported_in_api"},
	{path: "priority"},
	{path: "availability_nux", nullable: true},
	{path: "upgrade", nullable: true},
	{path: "support_verbosity"},
	{path: "default_verbosity", nullable: true},
	{path: "apply_patch_tool_type", nullable: true},
	{path: "truncation_policy"},
	{path: "truncation_policy.mode"},
	{path: "truncation_policy.limit"},
	{path: "experimental_supported_tools"},
	{path: "upgrade.model"},
	{path: "upgrade.migration_markdown"},
	{path: "availability_nux.message"},
	{path: "model_messages.instructions_template", nullable: true},
	{path: "model_messages.approvals", nullable: true},
	{path: "model_messages.collaboration_modes", nullable: true},
	{path: "model_messages.auto_review", nullable: true},
	{path: "model_messages.permissions", nullable: true},
	{path: "model_messages.multi_agent", nullable: true},
}

func catalogRequiredPath(path string) bool {
	for _, field := range catalogRequiredFields {
		if field.path == path {
			return true
		}
	}
	return false
}

// keepCatalogRequired copies omitted Codex-required keys back from the catalog
// entry. A partial override, including one saved from JSON, must not make the
// client reject every model.
func keepCatalogRequired(result, base *orderedObject) {
	if result == nil {
		return
	}
	for _, field := range catalogRequiredFields {
		if _, ok := getPath(result, field.path); ok {
			continue
		}
		if base != nil {
			if raw, okBase := getPath(base, field.path); okBase {
				setPath(result, field.path, raw)
				continue
			}
		}
		if field.nullable {
			setPath(result, field.path, json.RawMessage("null"))
		}
	}
	restoreInstruction(result, base)
}

func restoreInstruction(result, base *orderedObject) {
	if result == nil || hasUsableInstruction(result) || !hasUsableInstruction(base) {
		return
	}
	if raw, ok := base.get("base_instructions"); ok && !isNull(raw) {
		result.set("base_instructions", raw)
		return
	}
	template, ok := instructionTemplate(base)
	if !ok {
		return
	}
	messages := messageObject(result)
	messages.set("instructions_template", template)
	for _, field := range catalogRequiredFields {
		const prefix = "model_messages."
		if !strings.HasPrefix(field.path, prefix) {
			continue
		}
		key := strings.TrimPrefix(field.path, prefix)
		if _, exists := messages.get(key); exists {
			continue
		}
		if field.nullable {
			messages.set(key, json.RawMessage("null"))
		}
	}
	result.set("model_messages", messages.marshal())
}

func hasUsableInstruction(obj *orderedObject) bool {
	if obj == nil {
		return false
	}
	if raw, ok := obj.get("base_instructions"); ok && !isNull(raw) {
		return true
	}
	_, ok := instructionTemplate(obj)
	return ok
}

func instructionTemplate(obj *orderedObject) (json.RawMessage, bool) {
	messages := messageObject(obj)
	if messages == nil {
		return nil, false
	}
	template, ok := messages.get("instructions_template")
	if !ok || isNull(template) {
		return nil, false
	}
	return template, true
}

func messageObject(obj *orderedObject) *orderedObject {
	if obj == nil {
		return nil
	}
	raw, ok := obj.get("model_messages")
	if !ok || isNull(raw) {
		return nil
	}
	messages, okObj := asObject(raw)
	if !okObj {
		return nil
	}
	return messages
}

func getPath(obj *orderedObject, path string) (json.RawMessage, bool) {
	if obj == nil {
		return nil, false
	}
	head, tail, nested := strings.Cut(path, ".")
	raw, ok := obj.get(head)
	if !ok {
		return nil, false
	}
	if !nested {
		return raw, true
	}
	child, okChild := asObject(raw)
	if !okChild {
		return nil, false
	}
	return getPath(child, tail)
}

func setPath(obj *orderedObject, path string, value json.RawMessage) bool {
	if obj == nil {
		return false
	}
	head, tail, nested := strings.Cut(path, ".")
	if !nested {
		obj.set(head, value)
		return true
	}
	raw, ok := obj.get(head)
	child, okChild := asObject(raw)
	if !ok || !okChild {
		return false
	}
	if !setPath(child, tail, value) {
		return false
	}
	obj.set(head, child.marshal())
	return true
}
