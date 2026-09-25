package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRewriteCatalogMergesEveryField(t *testing.T) {
	catalog := []byte(`{"models":[{"slug":"deepseek-v4.1-flash","display_name":"Old","description":"old","input_modalities":["text"],"supported_reasoning_levels":[{"effort":"low","description":"old"}],"default_reasoning_level":"low","context_window":1000,"model_messages":{"instructions_template":"old","instructions_variables":{"personality_default":"keep-me"},"approvals":{"review":true}},"untouched":true},{"slug":"gpt-5.5","display_name":"Leave me","priority":9}]}`)
	override := []byte(`{"models":[{
		"slug":"deepseek-v4.1-flash",
		"display_name":"DeepSeek-Flash",
		"input_modalities":["text","image"],
		"supports_image_detail_original":true,
		"tool_mode":null,
		"truncation_policy":{"mode":"tokens","limit":10000},
		"supported_reasoning_levels":[{"effort":"low","description":"Fast"},{"effort":"max","description":"Maximum"}],
		"default_reasoning_level":"high",
		"context_window":1048576,
		"model_messages":{"instructions_template":"You are Codex"},
		"base_instructions":"base <tag> & more",
		"_mode":"merge",
		"_delete":["description"]
	}]}`)
	snap := mustSnapshot(t, override, resolvedConfig{Mode: modeMerge, MatchBase: true})
	got, ok := rewriteCatalog(catalog, snap)
	if !ok {
		t.Fatal("expected catalog rewrite")
	}
	if !bytes.Contains(got, []byte(`"slug":"gpt-5.5","display_name":"Leave me","priority":9`)) {
		t.Fatalf("unmatched model was rewritten: %s", got)
	}

	var doc struct {
		Models []map[string]any `json:"models"`
	}
	if err := json.Unmarshal(got, &doc); err != nil {
		t.Fatal(err)
	}
	model := doc.Models[0]
	if model["display_name"] != "DeepSeek-Flash" || model["default_reasoning_level"] != "high" {
		t.Fatalf("scalar override = %#v", model)
	}
	if _, exists := model["description"]; exists {
		t.Fatal("description should be deleted")
	}
	if model["untouched"] != true {
		t.Fatal("unspecified field was dropped")
	}
	if model["tool_mode"] != nil {
		t.Fatalf("tool_mode = %#v, want null", model["tool_mode"])
	}
	if model["context_window"] != float64(1048576) {
		t.Fatalf("context_window = %#v", model["context_window"])
	}
	modalities, _ := model["input_modalities"].([]any)
	if len(modalities) != 2 || modalities[1] != "image" {
		t.Fatalf("input_modalities = %#v", modalities)
	}
	levels, _ := model["supported_reasoning_levels"].([]any)
	if len(levels) != 2 {
		t.Fatalf("supported_reasoning_levels = %#v", levels)
	}
	messages, _ := model["model_messages"].(map[string]any)
	if messages["instructions_template"] != "You are Codex" {
		t.Fatalf("instructions_template = %#v", messages["instructions_template"])
	}
	vars, _ := messages["instructions_variables"].(map[string]any)
	if vars["personality_default"] != "keep-me" {
		t.Fatalf("nested sibling dropped: %#v", vars)
	}
	if _, exists := messages["approvals"]; !exists {
		t.Fatal("nested approvals should stay when not overridden")
	}
	if model["base_instructions"] != "base <tag> & more" {
		t.Fatalf("base_instructions = %#v", model["base_instructions"])
	}
	if bytes.Contains(got, []byte(`"_mode"`)) || bytes.Contains(got, []byte(`"_delete"`)) {
		t.Fatalf("control keys leaked: %s", got)
	}
}

func TestRewriteCatalogReplaceAndMatchBase(t *testing.T) {
	catalog := []byte(`{"models":[{"slug":"openai/deepseek-v4-pro","display_name":"Native","extra":1}]}`)
	override := []byte(`{"models":[{"slug":"deepseek-v4-pro","display_name":"DeepSeek-V4-Pro","_mode":"replace"}]}`)
	snap := mustSnapshot(t, override, resolvedConfig{Mode: modeMerge, MatchBase: true})
	got, ok := rewriteCatalog(catalog, snap)
	if !ok {
		t.Fatal("expected rewrite")
	}
	var doc struct {
		Models []map[string]any `json:"models"`
	}
	if err := json.Unmarshal(got, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Models[0]["slug"] != "deepseek-v4-pro" || doc.Models[0]["display_name"] != "DeepSeek-V4-Pro" {
		t.Fatalf("replaced model = %#v", doc.Models[0])
	}
	if _, exists := doc.Models[0]["extra"]; exists {
		t.Fatal("replace mode kept a native field")
	}
}

func TestRewriteCatalogExactBeatsBase(t *testing.T) {
	catalog := []byte(`{"models":[{"slug":"prov/m","display_name":"old"}]}`)
	override := []byte(`{"models":[
		{"slug":"m","display_name":"base"},
		{"slug":"prov/m","display_name":"exact"}
	]}`)
	snap := mustSnapshot(t, override, resolvedConfig{Mode: modeMerge, MatchBase: true})
	got, ok := rewriteCatalog(catalog, snap)
	if !ok {
		t.Fatal("expected rewrite")
	}
	if !bytes.Contains(got, []byte(`"display_name":"exact"`)) {
		t.Fatalf("exact match lost: %s", got)
	}
}

func TestRewriteCatalogSkipsOtherResponses(t *testing.T) {
	snap := mustSnapshot(t, []byte(`{"models":[{"slug":"m","display_name":"X"}]}`), resolvedConfig{Mode: modeMerge, MatchBase: true})
	for _, body := range []string{
		`{"object":"list","data":[{"id":"gpt-5.5"}]}`,
		`{"id":"resp","choices":[{"message":{"content":"models"}}]}`,
		`{"models":[{"name":"models/gemini-2.5-pro","displayName":"Gemini"}]}`,
		`not-json`,
	} {
		if _, ok := rewriteCatalog([]byte(body), snap); ok {
			t.Fatalf("rewrote non-catalog body %s", body)
		}
	}
}

func TestRewriteCatalogInsert(t *testing.T) {
	catalog := []byte(`{"models":[{"slug":"gpt-5.5","display_name":"GPT"}]}`)
	override := []byte(`{"models":[{"slug":"new-model","display_name":"New","_insert":true},{"slug":"hidden","display_name":"No","_insert":false}]}`)
	snap := mustSnapshot(t, override, resolvedConfig{Mode: modeMerge, MatchBase: true, Insert: false})
	got, ok := rewriteCatalog(catalog, snap)
	if !ok {
		t.Fatal("expected insert")
	}
	if !bytes.Contains(got, []byte(`"slug":"new-model"`)) || bytes.Contains(got, []byte(`"slug":"hidden"`)) {
		t.Fatalf("insert selection failed: %s", got)
	}
	if !bytes.Contains(got, []byte(`"slug":"gpt-5.5","display_name":"GPT"`)) {
		t.Fatalf("existing model changed: %s", got)
	}
}

func TestRefModelConfigOverridesCatalog(t *testing.T) {
	raw, err := os.ReadFile("/root/ref-model-config.json")
	if err != nil {
		t.Fatal(err)
	}
	snap := mustSnapshot(t, raw, resolvedConfig{Mode: modeMerge, MatchBase: true})
	if len(snap.overrides) != 2 {
		t.Fatalf("overrides = %d, want 2", len(snap.overrides))
	}
	catalog := []byte(`{"models":[{"slug":"deepseek-v4.1-flash","display_name":"native","input_modalities":["text"],"supported_reasoning_levels":[{"effort":"medium","description":"med"}]}]}`)
	got, ok := rewriteCatalog(catalog, snap)
	if !ok {
		t.Fatal("expected rewrite")
	}
	var doc struct {
		Models []map[string]any `json:"models"`
	}
	if err := json.Unmarshal(got, &doc); err != nil {
		t.Fatal(err)
	}
	model := doc.Models[0]
	if model["display_name"] != "DeepSeek-Flash" || model["shell_type"] != "shell_command" {
		t.Fatalf("identity fields = %#v", model["display_name"])
	}
	if model["tool_mode"] != nil || model["auto_compact_token_limit"] != nil {
		t.Fatalf("null fields = tool:%#v compact:%#v", model["tool_mode"], model["auto_compact_token_limit"])
	}
	policy, _ := model["truncation_policy"].(map[string]any)
	if policy["mode"] != "tokens" || policy["limit"] != float64(10000) {
		t.Fatalf("truncation_policy = %#v", policy)
	}
	messages, _ := model["model_messages"].(map[string]any)
	template, _ := messages["instructions_template"].(string)
	if len(template) < 100 || template[:12] != "You are Code" {
		t.Fatalf("instructions_template length %d", len(template))
	}
	base, _ := model["base_instructions"].(string)
	if base == "" {
		t.Fatal("base_instructions missing")
	}
}

func TestFileReloadPicksUpEdits(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "models.json")
	if err := os.WriteFile(path, []byte(`{"models":[{"slug":"m","display_name":"First"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	var state runtimeState
	raw, err := json.Marshal(map[string]any{"config_yaml": []byte("file: " + path + "\nmode: merge\n")})
	if err != nil {
		t.Fatal(err)
	}
	var req struct {
		ConfigYAML []byte `json:"config_yaml"`
	}
	if err := json.Unmarshal(raw, &req); err != nil {
		t.Fatal(err)
	}
	if err := state.configure(req.ConfigYAML); err != nil {
		t.Fatal(err)
	}
	catalog := []byte(`{"models":[{"slug":"m","display_name":"native"}]}`)
	got, ok := rewriteCatalog(catalog, state.current())
	if !ok || !bytes.Contains(got, []byte(`"display_name":"First"`)) {
		t.Fatalf("first load = %s ok=%v", got, ok)
	}
	time.Sleep(20 * time.Millisecond)
	if err := os.WriteFile(path, []byte(`{"models":[{"slug":"m","display_name":"Second","visibility":"list"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	got, ok = rewriteCatalog(catalog, state.current())
	if !ok || !bytes.Contains(got, []byte(`"display_name":"Second"`)) || !bytes.Contains(got, []byte(`"visibility":"list"`)) {
		t.Fatalf("reload = %s ok=%v", got, ok)
	}
}

func TestUpstreamChangeKeepsOnlyPatchedField(t *testing.T) {
	dir := t.TempDir()
	var runtime runtimeState
	runtime.dir = dir
	runtime.cfg = resolvedConfig{Mode: modeMerge, MatchBase: true}
	runtime.captured = []byte(`{"models":[{"slug":"m","display_name":"Old","context_window":10}]}`)
	if err := runtime.editModel("set-model", "m", []byte(`{"slug":"m","display_name":"Mine","context_window":10}`)); err != nil {
		t.Fatal(err)
	}
	runtime.captured = []byte(`{"models":[{"slug":"m","display_name":"Old","context_window":99}]}`)
	got, ok := runtime.applyTo(runtime.captured)
	if !ok || !bytes.Contains(got, []byte(`"display_name":"Mine"`)) || !bytes.Contains(got, []byte(`"context_window":99`)) {
		t.Fatalf("rewritten = %s ok=%v", got, ok)
	}
	if err := runtime.editModel("reset-model", "m", nil); err != nil {
		t.Fatal(err)
	}
	if _, ok := runtime.applyTo(runtime.captured); ok {
		t.Fatal("reset model should leave the upstream catalog unchanged")
	}
}

func TestRemoveHidesModelUntilRestored(t *testing.T) {
	dir := t.TempDir()
	var runtime runtimeState
	runtime.dir = dir
	runtime.cfg = resolvedConfig{Mode: modeMerge, MatchBase: true}
	runtime.captured = []byte(`{"models":[{"slug":"keep","display_name":"Keep"},{"slug":"gone","display_name":"Gone"}]}`)
	if err := runtime.editModel("remove", "gone", nil); err != nil {
		t.Fatal(err)
	}
	preview, _ := runtime.preview()
	if bytes.Contains(preview, []byte(`"gone"`)) || !bytes.Contains(preview, []byte(`"keep"`)) {
		t.Fatalf("preview = %s", preview)
	}
	models, removed, _ := runtime.modelViews()
	if len(models) != 1 || models[0].Slug != "keep" {
		t.Fatalf("issued = %#v", models)
	}
	if len(removed) != 1 || removed[0].Slug != "gone" || removed[0].Status != "removed" {
		t.Fatalf("removed = %#v", removed)
	}
	if err := runtime.editModel("reset-model", "gone", nil); err != nil {
		t.Fatal(err)
	}
	preview, _ = runtime.preview()
	if !bytes.Contains(preview, []byte(`"gone"`)) {
		t.Fatalf("restored preview = %s", preview)
	}
}

func TestDiffKeepsUntouchedFieldsOutOfPatch(t *testing.T) {
	base := []byte(`{"models":[{"slug":"m","display_name":"Old","context_window":10,"tool_mode":null}]}`)
	edited := []byte(`{"models":[{"slug":"m","display_name":"New","context_window":10,"tool_mode":null},{"slug":"extra","display_name":"Extra"}]}`)
	saved, err := diffCatalog(base, edited)
	if err != nil {
		t.Fatal(err)
	}
	if len(saved.Models) != 2 {
		t.Fatalf("patches = %d, want changed model plus append", len(saved.Models))
	}
	if bytes.Contains(saved.Models[0], []byte(`"context_window"`)) {
		t.Fatalf("unchanged field stored in patch: %s", saved.Models[0])
	}
	if !bytes.Contains(saved.Models[0], []byte(`"display_name":"New"`)) {
		t.Fatalf("patch = %s", saved.Models[0])
	}
}

func mustSnapshot(t *testing.T, raw []byte, cfg resolvedConfig) *snapshot {
	t.Helper()
	models, err := parseModelDocument(raw)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Inline = models
	if cfg.Mode == "" {
		cfg.Mode = modeMerge
	}
	snap, err := buildSnapshot(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return snap
}
