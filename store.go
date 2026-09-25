package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	defaultDataDir = "/CLIProxyAPI/logs/.plugins/models-cache-override"
	legacyFile     = "/CLIProxyAPI/logs/.plugins/models-cache-override.json"
)

var catalogURLs = []string{
	"https://models.router-for.me/codex_client_models.json",
	"https://raw.githubusercontent.com/router-for-me/models/refs/heads/main/codex_client_models.json",
}

type savedPatches struct {
	Removes []string          `json:"removes,omitempty"`
	Models  []json.RawMessage `json:"models,omitempty"`
}

func (s *runtimeState) dataDir() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if strings.TrimSpace(s.dir) != "" {
		return s.dir
	}
	return defaultDataDir
}

func (s *runtimeState) loadPatches() error {
	dir := s.dataDir()
	raw, errRead := os.ReadFile(filepath.Join(dir, "patches.json"))
	if errRead != nil {
		if !os.IsNotExist(errRead) {
			return errRead
		}
		return s.importLegacy(dir)
	}
	var saved savedPatches
	if errDecode := json.Unmarshal(raw, &saved); errDecode != nil {
		return errDecode
	}
	return s.usePatches(saved)
}

func (s *runtimeState) importLegacy(dir string) error {
	raw, errRead := os.ReadFile(legacyFile)
	if errRead != nil {
		if os.IsNotExist(errRead) {
			return s.usePatches(savedPatches{})
		}
		return errRead
	}
	models, errParse := parseModelDocument(raw)
	if errParse != nil {
		return errParse
	}
	saved := savedPatches{Models: models}
	if errWrite := writePatches(dir, saved); errWrite != nil {
		return errWrite
	}
	return s.usePatches(saved)
}

func (s *runtimeState) usePatches(saved savedPatches) error {
	cfg := s.resolved()
	overrides := make([]compiledOverride, 0, len(saved.Models))
	for i, raw := range saved.Models {
		ov, errCompile := compileOverride(raw, i, cfg.Mode)
		if errCompile != nil {
			return fmt.Errorf("patches.models[%d]: %w", i, errCompile)
		}
		overrides = append(overrides, ov)
	}
	s.mu.Lock()
	s.patches = saved
	s.snap = &snapshot{overrides: overrides, insert: cfg.Insert, matchBase: cfg.MatchBase}
	s.mu.Unlock()
	return nil
}

func (s *runtimeState) resolved() resolvedConfig {
	s.mu.Lock()
	defer s.mu.Unlock()
	cfg := s.cfg
	if cfg.Mode == "" {
		cfg.Mode = modeMerge
	}
	if s.cfg.MatchBase == false && s.cfg.File == "" && s.cfg.Mode == "" {
		cfg.MatchBase = true
	}
	return cfg
}

func writePatches(dir string, saved savedPatches) error {
	if errMk := os.MkdirAll(dir, 0o755); errMk != nil {
		return errMk
	}
	raw, errMarshal := json.MarshalIndent(saved, "", "  ")
	if errMarshal != nil {
		return errMarshal
	}
	raw = append(raw, '\n')
	return os.WriteFile(filepath.Join(dir, "patches.json"), raw, 0o644)
}

func (s *runtimeState) savePatches(saved savedPatches) error {
	if errWrite := writePatches(s.dataDir(), saved); errWrite != nil {
		return errWrite
	}
	return s.usePatches(saved)
}

func (s *runtimeState) loadCachedFiles() {
	dir := s.dataDir()
	captured, _ := os.ReadFile(filepath.Join(dir, "captured.json"))
	upstream, _ := os.ReadFile(filepath.Join(dir, "upstream.json"))
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(captured) > 0 {
		s.captured = captured
	}
	if len(upstream) > 0 {
		s.upstream = upstream
	}
}

func (s *runtimeState) applyTo(body []byte) ([]byte, bool) {
	if !maybeCatalog(body) {
		return nil, false
	}
	s.rememberCaptured(body)
	rewritten := s.compose(body)
	if bytes.Equal(bytes.TrimSpace(body), bytes.TrimSpace(rewritten)) {
		return nil, false
	}
	return rewritten, true
}

func (s *runtimeState) appendMissingPatches(body []byte) []byte {
	s.mu.Lock()
	models := append([]json.RawMessage(nil), s.patches.Models...)
	removes := append([]string(nil), s.patches.Removes...)
	s.mu.Unlock()
	if len(models) == 0 {
		return body
	}
	dropped := map[string]struct{}{}
	for _, slug := range removes {
		dropped[slug] = struct{}{}
	}
	doc, errDoc := decodeOrdered(json.NewDecoder(bytes.NewReader(body)))
	if errDoc != nil {
		return body
	}
	modelsRaw, ok := doc.get("models")
	if !ok {
		return body
	}
	var existing []json.RawMessage
	if errModels := json.Unmarshal(modelsRaw, &existing); errModels != nil {
		return body
	}
	present := map[string]struct{}{}
	for _, raw := range existing {
		if obj, okObj := asObject(raw); okObj {
			if slug := stringField(obj, "slug"); slug != "" {
				present[slug] = struct{}{}
			}
		}
	}
	changed := false
	for _, raw := range models {
		obj, okObj := asObject(raw)
		if !okObj {
			continue
		}
		slug := stringField(obj, "slug")
		if slug == "" {
			continue
		}
		if _, skip := dropped[slug]; skip {
			continue
		}
		if _, okPresent := present[slug]; okPresent {
			continue
		}
		obj.delete("_mode")
		obj.delete("_insert")
		obj.delete("_match")
		obj.delete("_delete")
		existing = append(existing, obj.marshal())
		changed = true
	}
	if !changed {
		return body
	}
	doc.set("models", marshalArray(existing))
	return doc.marshal()
}

func (s *runtimeState) withoutRemoved(body []byte) []byte {
	s.mu.Lock()
	removes := append([]string(nil), s.patches.Removes...)
	s.mu.Unlock()
	if len(removes) == 0 {
		return body
	}
	drop := map[string]struct{}{}
	for _, slug := range removes {
		drop[slug] = struct{}{}
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	doc, errDoc := decodeOrdered(dec)
	if errDoc != nil {
		return body
	}
	modelsRaw, ok := doc.get("models")
	if !ok {
		return body
	}
	var models []json.RawMessage
	if errModels := json.Unmarshal(modelsRaw, &models); errModels != nil {
		return body
	}
	kept := make([]json.RawMessage, 0, len(models))
	for _, model := range models {
		obj, okObj := asObject(model)
		slug := ""
		if okObj {
			slug = stringField(obj, "slug")
		}
		if _, removed := drop[slug]; removed {
			continue
		}
		kept = append(kept, bytes.TrimSpace(model))
	}
	doc.set("models", marshalArray(kept))
	return doc.marshal()
}

func (s *runtimeState) rememberCaptured(body []byte) {
	if !maybeCatalog(body) {
		return
	}
	cloned := append([]byte(nil), body...)
	s.mu.Lock()
	s.captured = cloned
	s.capturedAt = time.Now().UTC()
	dir := s.dir
	s.mu.Unlock()
	if strings.TrimSpace(dir) == "" {
		dir = defaultDataDir
	}
	_ = os.MkdirAll(dir, 0o755)
	_ = os.WriteFile(filepath.Join(dir, "captured.json"), cloned, 0o644)
}

func (s *runtimeState) deliveryBase() (body []byte, source string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.captured) > 0 {
		return append([]byte(nil), s.captured...), "captured"
	}
	if len(s.upstream) > 0 {
		return append([]byte(nil), s.upstream...), "upstream"
	}
	return nil, "empty"
}

func (s *runtimeState) preview() ([]byte, string) {
	base, source := s.deliveryBase()
	if len(base) == 0 {
		return []byte(`{"models":[]}`), source
	}
	return s.compose(base), source
}

func (s *runtimeState) compose(body []byte) []byte {
	out := s.withoutRemoved(body)
	if rewritten, ok := rewriteCatalog(out, s.current()); ok {
		out = rewritten
	}
	return s.appendMissingPatches(out)
}

func (s *runtimeState) acceptPreview(preview []byte) error {
	base, _ := s.deliveryBase()
	if len(base) == 0 {
		return fmt.Errorf("还没有 CPA 模型目录，请先拉取")
	}
	saved, errDiff := diffCatalog(base, preview)
	if errDiff != nil {
		return errDiff
	}
	return s.savePatches(saved)
}

func (s *runtimeState) refreshUpstream() error {
	body, source, errFetch := fetchCatalog()
	if errFetch != nil {
		return errFetch
	}
	dir := s.dataDir()
	if errMk := os.MkdirAll(dir, 0o755); errMk != nil {
		return errMk
	}
	if errWrite := os.WriteFile(filepath.Join(dir, "upstream.json"), body, 0o644); errWrite != nil {
		return errWrite
	}
	s.mu.Lock()
	s.upstream = append([]byte(nil), body...)
	s.upstreamAt = time.Now().UTC()
	s.upstreamURL = source
	if len(s.captured) == 0 {
		s.captured = append([]byte(nil), body...)
		s.capturedAt = s.upstreamAt
	}
	s.mu.Unlock()
	return nil
}

func fetchCatalog() ([]byte, string, error) {
	var last error
	for _, sourceURL := range catalogURLs {
		body, errGet := fetchURL(sourceURL)
		if errGet != nil {
			last = errGet
			continue
		}
		if _, errParse := parseModelDocument(body); errParse != nil {
			last = errParse
			continue
		}
		return body, sourceURL, nil
	}
	if last == nil {
		last = fmt.Errorf("no catalog url")
	}
	return nil, "", last
}

func fetchURL(sourceURL string) ([]byte, error) {
	if raw, errHost := hostHTTP(sourceURL); errHost == nil && len(raw) > 0 {
		return raw, nil
	}
	client := &http.Client{Timeout: 20 * time.Second}
	resp, errDo := client.Get(sourceURL)
	if errDo != nil {
		return nil, errDo
	}
	defer resp.Body.Close()
	body, errRead := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if errRead != nil {
		return nil, errRead
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s returned %d", sourceURL, resp.StatusCode)
	}
	return body, nil
}

func diffCatalog(baseRaw, editedRaw []byte) (savedPatches, error) {
	baseModels, errBase := parseModelDocument(baseRaw)
	if errBase != nil {
		return savedPatches{}, fmt.Errorf("base: %w", errBase)
	}
	editedModels, errEdited := parseModelDocument(editedRaw)
	if errEdited != nil {
		return savedPatches{}, fmt.Errorf("preview: %w", errEdited)
	}
	baseBySlug := map[string]json.RawMessage{}
	var baseOrder []string
	for _, raw := range baseModels {
		obj, ok := asObject(raw)
		if !ok {
			continue
		}
		slug := stringField(obj, "slug")
		if slug == "" {
			continue
		}
		baseBySlug[slug] = bytes.TrimSpace(raw)
		baseOrder = append(baseOrder, slug)
	}
	seen := map[string]struct{}{}
	var models []json.RawMessage
	for _, raw := range editedModels {
		obj, ok := asObject(raw)
		if !ok {
			return savedPatches{}, fmt.Errorf("preview model must be an object")
		}
		slug := stringField(obj, "slug")
		if slug == "" {
			return savedPatches{}, fmt.Errorf("preview model needs slug")
		}
		seen[slug] = struct{}{}
		baseRawModel, exists := baseBySlug[slug]
		if !exists {
			obj.delete("_mode")
			obj.delete("_insert")
			obj.delete("_match")
			obj.delete("_delete")
			obj.set("_insert", json.RawMessage("true"))
			models = append(models, obj.marshal())
			continue
		}
		patch, errPatch := diffModel(baseRawModel, bytes.TrimSpace(raw))
		if errPatch != nil {
			return savedPatches{}, fmt.Errorf("%s: %w", slug, errPatch)
		}
		if patch == nil {
			continue
		}
		models = append(models, patch)
	}
	var removes []string
	for _, slug := range baseOrder {
		if _, ok := seen[slug]; !ok {
			removes = append(removes, slug)
		}
	}
	return savedPatches{Removes: removes, Models: models}, nil
}

func diffModel(baseRaw, editedRaw json.RawMessage) (json.RawMessage, error) {
	base, okBase := asObject(baseRaw)
	edited, okEdited := asObject(editedRaw)
	if !okBase || !okEdited {
		return nil, fmt.Errorf("model is not an object")
	}
	patch := newOrdered()
	patch.set("slug", mustRaw(json.Marshal(stringField(edited, "slug"))))
	var deletes []string
	changed := diffInto(patch, deletes, "", base, edited)
	deletes = changed.deletes
	if len(patch.keys) == 1 && len(deletes) == 0 {
		return nil, nil
	}
	if len(deletes) > 0 {
		raw, errMarshal := json.Marshal(deletes)
		if errMarshal != nil {
			return nil, errMarshal
		}
		patch.set("_delete", raw)
	}
	patch.set("_mode", json.RawMessage(`"merge"`))
	return patch.marshal(), nil
}

type diffResult struct {
	deletes []string
}

func diffInto(patch *orderedObject, deletes []string, prefix string, base, edited *orderedObject) diffResult {
	for _, key := range edited.keys {
		if strings.HasPrefix(key, "_") {
			continue
		}
		next := edited.vals[key]
		prev, exists := base.get(key)
		path := key
		if prefix != "" {
			path = prefix + "." + key
		}
		if !exists || isNull(next) {
			if !exists || !rawEqual(prev, next) {
				patch.set(key, bytes.TrimSpace(next))
			}
			continue
		}
		baseObj, baseOK := asObject(prev)
		editObj, editOK := asObject(next)
		if baseOK && editOK {
			nested := newOrdered()
			result := diffInto(nested, nil, path, baseObj, editObj)
			deletes = append(deletes, result.deletes...)
			if len(nested.keys) > 0 {
				patch.set(key, nested.marshal())
			}
			continue
		}
		if !rawEqual(prev, next) {
			patch.set(key, bytes.TrimSpace(next))
		}
	}
	for _, key := range base.keys {
		if _, ok := edited.get(key); ok {
			continue
		}
		path := key
		if prefix != "" {
			path = prefix + "." + key
		}
		deletes = append(deletes, path)
	}
	return diffResult{deletes: deletes}
}

func pathKey(prefix, key string) string {
	if prefix == "" {
		return key
	}
	return prefix + "." + key
}

func rawEqual(left, right json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(left), bytes.TrimSpace(right))
}

type modelView struct {
	Slug         string          `json:"slug"`
	DisplayName  string          `json:"display_name"`
	Status       string          `json:"status"`
	Upstream     json.RawMessage `json:"upstream,omitempty"`
	Issued       json.RawMessage `json:"issued"`
	PatchedPaths []string        `json:"patched_paths,omitempty"`
}

func (s *runtimeState) modelViews() (issued []modelView, removed []modelView, source string) {
	base, source := s.deliveryBase()
	preview, _ := s.preview()
	baseModels, _ := parseModelDocument(base)
	issuedModels, _ := parseModelDocument(preview)
	baseBySlug := indexModels(baseModels)
	s.mu.Lock()
	patches := append([]json.RawMessage(nil), s.patches.Models...)
	removes := append([]string(nil), s.patches.Removes...)
	s.mu.Unlock()
	patchBySlug := indexModels(patches)
	seen := map[string]struct{}{}
	for _, raw := range issuedModels {
		obj, ok := asObject(raw)
		if !ok {
			continue
		}
		slug := stringField(obj, "slug")
		if slug == "" {
			continue
		}
		seen[slug] = struct{}{}
		upstream := baseBySlug[slug]
		patch := patchBySlug[slug]
		status := "upstream"
		if len(upstream) == 0 {
			status = "appended"
		} else if len(patch) > 0 {
			status = "patched"
		}
		issued = append(issued, modelView{
			Slug: slug, DisplayName: stringField(obj, "display_name"), Status: status,
			Upstream: upstream, Issued: bytes.TrimSpace(raw), PatchedPaths: patchedPaths(patch),
		})
	}
	for _, slug := range removes {
		if _, ok := seen[slug]; ok {
			continue
		}
		raw := baseBySlug[slug]
		name := slug
		if obj, ok := asObject(raw); ok && stringField(obj, "display_name") != "" {
			name = stringField(obj, "display_name")
		}
		removed = append(removed, modelView{Slug: slug, DisplayName: name, Status: "removed", Upstream: raw})
	}
	return issued, removed, source
}

func (s *runtimeState) editModel(action, slug string, model json.RawMessage) error {
	slug = strings.TrimSpace(slug)
	switch action {
	case "reset-all":
		return s.savePatches(savedPatches{})
	case "reset-model", "restore":
		if slug == "" {
			return fmt.Errorf("缺少 slug")
		}
		return s.savePatches(s.dropSlug(slug, true))
	case "remove":
		if slug == "" {
			return fmt.Errorf("缺少 slug")
		}
		saved := s.dropSlug(slug, false)
		saved.Removes = append(uniqueStrings(saved.Removes), slug)
		return s.savePatches(saved)
	case "set-model", "append":
		if len(model) == 0 {
			return fmt.Errorf("缺少模型内容")
		}
		obj, ok := asObject(model)
		if !ok || stringField(obj, "slug") == "" {
			return fmt.Errorf("模型需要 slug")
		}
		slug = stringField(obj, "slug")
		base, _ := s.deliveryBase()
		baseModels, _ := parseModelDocument(base)
		upstream := indexModels(baseModels)[slug]
		saved := s.dropSlug(slug, true)
		if len(upstream) == 0 {
			obj.delete("_mode")
			obj.delete("_match")
			obj.delete("_delete")
			obj.set("_insert", json.RawMessage("true"))
			saved.Models = append(saved.Models, obj.marshal())
			return s.savePatches(saved)
		}
		patch, errPatch := diffModel(upstream, obj.marshal())
		if errPatch != nil {
			return errPatch
		}
		if patch != nil {
			saved.Models = append(saved.Models, patch)
		}
		return s.savePatches(saved)
	default:
		return fmt.Errorf("未知操作")
	}
}

func (s *runtimeState) dropSlug(slug string, clearRemove bool) savedPatches {
	s.mu.Lock()
	saved := s.patches
	s.mu.Unlock()
	next := savedPatches{}
	for _, raw := range saved.Models {
		obj, ok := asObject(raw)
		if ok && stringField(obj, "slug") == slug {
			continue
		}
		next.Models = append(next.Models, raw)
	}
	for _, item := range saved.Removes {
		if clearRemove && item == slug {
			continue
		}
		if !clearRemove && item == slug {
			continue
		}
		next.Removes = append(next.Removes, item)
	}
	return next
}

func indexModels(models []json.RawMessage) map[string]json.RawMessage {
	out := map[string]json.RawMessage{}
	for _, raw := range models {
		obj, ok := asObject(raw)
		if !ok {
			continue
		}
		slug := stringField(obj, "slug")
		if slug != "" {
			out[slug] = bytes.TrimSpace(raw)
		}
	}
	return out
}

func patchedPaths(raw json.RawMessage) []string {
	obj, ok := asObject(raw)
	if !ok {
		return nil
	}
	var paths []string
	var walk func(prefix string, node *orderedObject)
	walk = func(prefix string, node *orderedObject) {
		for _, key := range node.keys {
			if strings.HasPrefix(key, "_") || key == "slug" && prefix == "" {
				if key == "_delete" {
					list, errList := jsonStringList(node.vals[key])
					if errList == nil {
						paths = append(paths, list...)
					}
				}
				continue
			}
			path := key
			if prefix != "" {
				path = prefix + "." + key
			}
			if child, okChild := asObject(node.vals[key]); okChild {
				walk(path, child)
				continue
			}
			paths = append(paths, path)
		}
	}
	walk("", obj)
	return paths
}

func mustRaw(raw []byte, err error) json.RawMessage {
	if err != nil {
		return json.RawMessage(`""`)
	}
	return raw
}
