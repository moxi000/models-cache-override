package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

const (
	modeMerge   = "merge"
	modeReplace = "replace"
)

// compiledOverride is one models_cache entry after control keys are removed.
// keys are matched against the catalog slug. body is applied as JSON so null,
// numbers, and nested objects stay exact.
type compiledOverride struct {
	index  int
	keys   []string
	mode   string
	insert *bool
	delete []string
	body   *orderedObject
}

type snapshot struct {
	overrides []compiledOverride
	insert    bool
	matchBase bool
}

func rewriteCatalog(body []byte, snap *snapshot) ([]byte, bool) {
	if snap == nil || len(snap.overrides) == 0 || !maybeCatalog(body) {
		return nil, false
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	doc, errDecode := decodeOrdered(dec)
	if errDecode != nil {
		return nil, false
	}
	modelsRaw, ok := doc.get("models")
	if !ok {
		return nil, false
	}
	var models []json.RawMessage
	if errModels := json.Unmarshal(modelsRaw, &models); errModels != nil {
		return nil, false
	}
	if !isCodexCatalog(models) {
		return nil, false
	}

	matched := make([]bool, len(snap.overrides))
	changed := false
	outModels := make([]json.RawMessage, len(models))
	for i, model := range models {
		obj, okObj := asObject(model)
		slug := ""
		if okObj {
			slug = stringField(obj, "slug")
		}
		if !okObj || slug == "" {
			outModels[i] = bytes.TrimSpace(model)
			continue
		}
		ov, idx := bestOverride(snap, slug)
		if ov == nil {
			outModels[i] = bytes.TrimSpace(model)
			continue
		}
		matched[idx] = true
		rewritten := applyOne(obj, slug, ov)
		if !bytes.Equal(bytes.TrimSpace(model), bytes.TrimSpace(rewritten)) {
			changed = true
		}
		outModels[i] = rewritten
	}
	for i, ov := range snap.overrides {
		if matched[i] || !ov.shouldInsert(snap.insert) {
			continue
		}
		slug := ""
		if len(ov.keys) > 0 {
			slug = ov.keys[0]
		}
		outModels = append(outModels, applyOne(nil, slug, &ov))
		changed = true
	}
	if !changed {
		return nil, false
	}
	doc.set("models", marshalArray(outModels))
	return doc.marshal(), true
}

func maybeCatalog(body []byte) bool {
	body = bytes.TrimLeft(body, " \t\r\n")
	if len(body) == 0 || body[0] != '{' {
		return false
	}
	window := body
	if len(window) > 256 {
		window = window[:256]
	}
	return bytes.Contains(window, []byte(`"models"`))
}

func isCodexCatalog(models []json.RawMessage) bool {
	for _, model := range models {
		obj, ok := asObject(model)
		if !ok {
			continue
		}
		if stringField(obj, "slug") != "" {
			return true
		}
	}
	return false
}

func bestOverride(snap *snapshot, slug string) (*compiledOverride, int) {
	var best *compiledOverride
	bestRank := 0
	bestIndex := -1
	for i := range snap.overrides {
		ov := &snap.overrides[i]
		rank := ov.rank(slug, snap.matchBase)
		if rank == 0 {
			continue
		}
		if rank > bestRank || (rank == bestRank && ov.index > bestIndex) {
			best = ov
			bestRank = rank
			bestIndex = ov.index
		}
	}
	if best == nil {
		return nil, -1
	}
	return best, best.index
}

func (o compiledOverride) rank(slug string, matchBase bool) int {
	best := 0
	for _, key := range o.keys {
		if key == slug {
			return 2
		}
		if matchBase && baseSlug(key) == baseSlug(slug) {
			best = 1
		}
	}
	return best
}

func (o compiledOverride) shouldInsert(global bool) bool {
	if o.insert != nil {
		return *o.insert
	}
	return global
}

func baseSlug(slug string) string {
	slug = strings.TrimSpace(slug)
	if i := strings.LastIndex(slug, "/"); i >= 0 {
		slug = strings.TrimSpace(slug[i+1:])
	}
	return slug
}

func applyOne(base *orderedObject, catalogSlug string, ov *compiledOverride) json.RawMessage {
	var result *orderedObject
	if ov.mode == modeReplace || base == nil {
		result = ov.body.clone()
	} else {
		result = mergeObjects(base, ov.body)
	}
	if stringField(result, "slug") == "" && catalogSlug != "" {
		raw, errMarshal := json.Marshal(catalogSlug)
		if errMarshal == nil {
			result.set("slug", raw)
		}
	}
	for _, path := range ov.delete {
		deletePath(result, path)
	}
	return result.marshal()
}

func compileOverride(raw json.RawMessage, index int, defaultMode string) (compiledOverride, error) {
	obj, ok := asObject(raw)
	if !ok {
		return compiledOverride{}, fmt.Errorf("model override must be a JSON object")
	}
	obj = obj.clone()
	mode := defaultMode
	if mode == "" {
		mode = modeMerge
	}
	if rawMode, okMode := obj.get("_mode"); okMode {
		parsed, errMode := jsonString(rawMode)
		if errMode != nil {
			return compiledOverride{}, fmt.Errorf("_mode: %w", errMode)
		}
		mode = parsed
		obj.delete("_mode")
	}
	if mode != modeMerge && mode != modeReplace {
		return compiledOverride{}, fmt.Errorf("_mode must be merge or replace")
	}

	keys := make([]string, 0, 2)
	if slug := stringField(obj, "slug"); slug != "" {
		keys = append(keys, slug)
	}
	if rawMatch, okMatch := obj.get("_match"); okMatch {
		extra, errMatch := matchKeys(rawMatch)
		if errMatch != nil {
			return compiledOverride{}, errMatch
		}
		keys = append(keys, extra...)
		obj.delete("_match")
	}
	keys = uniqueStrings(keys)
	if len(keys) == 0 {
		return compiledOverride{}, fmt.Errorf("model override needs slug or _match")
	}

	var insert *bool
	if rawInsert, okInsert := obj.get("_insert"); okInsert {
		value, errInsert := jsonBool(rawInsert)
		if errInsert != nil {
			return compiledOverride{}, fmt.Errorf("_insert: %w", errInsert)
		}
		insert = &value
		obj.delete("_insert")
	}

	var deletes []string
	if rawDelete, okDelete := obj.get("_delete"); okDelete {
		parsed, errDelete := jsonStringList(rawDelete)
		if errDelete != nil {
			return compiledOverride{}, fmt.Errorf("_delete: %w", errDelete)
		}
		deletes = parsed
		obj.delete("_delete")
	}

	return compiledOverride{
		index:  index,
		keys:   keys,
		mode:   mode,
		insert: insert,
		delete: deletes,
		body:   obj,
	}, nil
}

func matchKeys(raw json.RawMessage) ([]string, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return nil, fmt.Errorf("_match is empty")
	}
	if raw[0] == '"' {
		value, errValue := jsonString(raw)
		if errValue != nil {
			return nil, fmt.Errorf("_match: %w", errValue)
		}
		if value == "" {
			return nil, fmt.Errorf("_match is empty")
		}
		return []string{value}, nil
	}
	keys, errKeys := jsonStringList(raw)
	if errKeys != nil {
		return nil, fmt.Errorf("_match: %w", errKeys)
	}
	if len(keys) == 0 {
		return nil, fmt.Errorf("_match is empty")
	}
	return keys, nil
}

func mergeObjects(base, overlay *orderedObject) *orderedObject {
	out := base.clone()
	if overlay == nil {
		return out
	}
	for _, key := range overlay.keys {
		value := overlay.vals[key]
		if isNull(value) {
			out.set(key, json.RawMessage("null"))
			continue
		}
		if prev, ok := out.get(key); ok {
			baseObj, baseOK := asObject(prev)
			overlayObj, overlayOK := asObject(value)
			if baseOK && overlayOK {
				out.set(key, mergeObjects(baseObj, overlayObj).marshal())
				continue
			}
		}
		out.set(key, bytes.TrimSpace(value))
	}
	return out
}

func deletePath(obj *orderedObject, path string) {
	if obj == nil {
		return
	}
	path = strings.TrimSpace(path)
	if path == "" {
		return
	}
	head, tail, nested := strings.Cut(path, ".")
	if !nested {
		obj.delete(head)
		return
	}
	raw, ok := obj.get(head)
	child, okChild := asObject(raw)
	if !ok || !okChild {
		return
	}
	deletePath(child, tail)
	obj.set(head, child.marshal())
}

type orderedObject struct {
	keys []string
	vals map[string]json.RawMessage
}

func newOrdered() *orderedObject {
	return &orderedObject{vals: map[string]json.RawMessage{}}
}

func (o *orderedObject) clone() *orderedObject {
	if o == nil {
		return newOrdered()
	}
	out := &orderedObject{
		keys: append([]string(nil), o.keys...),
		vals: make(map[string]json.RawMessage, len(o.vals)),
	}
	for key, value := range o.vals {
		out.vals[key] = append(json.RawMessage(nil), value...)
	}
	return out
}

func (o *orderedObject) get(key string) (json.RawMessage, bool) {
	if o == nil {
		return nil, false
	}
	value, ok := o.vals[key]
	return value, ok
}

func (o *orderedObject) set(key string, value json.RawMessage) {
	if o == nil {
		return
	}
	if _, ok := o.vals[key]; !ok {
		o.keys = append(o.keys, key)
	}
	o.vals[key] = append(json.RawMessage(nil), bytes.TrimSpace(value)...)
}

func (o *orderedObject) delete(key string) {
	if o == nil {
		return
	}
	if _, ok := o.vals[key]; !ok {
		return
	}
	delete(o.vals, key)
	keys := make([]string, 0, len(o.keys))
	for _, existing := range o.keys {
		if existing != key {
			keys = append(keys, existing)
		}
	}
	o.keys = keys
}

func (o *orderedObject) marshal() json.RawMessage {
	var buf bytes.Buffer
	buf.WriteByte('{')
	first := true
	for _, key := range o.keys {
		value, ok := o.vals[key]
		if !ok {
			continue
		}
		encodedKey, errKey := json.Marshal(key)
		if errKey != nil {
			continue
		}
		if !first {
			buf.WriteByte(',')
		}
		first = false
		buf.Write(encodedKey)
		buf.WriteByte(':')
		buf.Write(bytes.TrimSpace(value))
	}
	buf.WriteByte('}')
	return buf.Bytes()
}

func decodeOrdered(dec *json.Decoder) (*orderedObject, error) {
	tok, errTok := dec.Token()
	if errTok != nil {
		return nil, errTok
	}
	delim, ok := tok.(json.Delim)
	if !ok || delim != '{' {
		return nil, fmt.Errorf("expected JSON object")
	}
	obj := newOrdered()
	for dec.More() {
		keyTok, errKey := dec.Token()
		if errKey != nil {
			return nil, errKey
		}
		key, okKey := keyTok.(string)
		if !okKey {
			return nil, fmt.Errorf("expected string key")
		}
		var raw json.RawMessage
		if errValue := dec.Decode(&raw); errValue != nil {
			return nil, errValue
		}
		obj.set(key, raw)
	}
	end, errEnd := dec.Token()
	if errEnd != nil {
		return nil, errEnd
	}
	endDelim, okEnd := end.(json.Delim)
	if !okEnd || endDelim != '}' {
		return nil, fmt.Errorf("expected end of JSON object")
	}
	return obj, nil
}

func asObject(raw json.RawMessage) (*orderedObject, bool) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '{' {
		return nil, false
	}
	obj, errObj := decodeOrdered(json.NewDecoder(bytes.NewReader(raw)))
	if errObj != nil {
		return nil, false
	}
	return obj, true
}

func marshalArray(items []json.RawMessage) json.RawMessage {
	var buf bytes.Buffer
	buf.WriteByte('[')
	for i, item := range items {
		if i > 0 {
			buf.WriteByte(',')
		}
		buf.Write(bytes.TrimSpace(item))
	}
	buf.WriteByte(']')
	return buf.Bytes()
}

func isNull(raw json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}

func stringField(obj *orderedObject, key string) string {
	if obj == nil {
		return ""
	}
	raw, ok := obj.get(key)
	if !ok {
		return ""
	}
	value, errValue := jsonString(raw)
	if errValue != nil {
		return ""
	}
	return strings.TrimSpace(value)
}

func jsonString(raw json.RawMessage) (string, error) {
	var value string
	if errDecode := json.Unmarshal(bytes.TrimSpace(raw), &value); errDecode != nil {
		return "", errDecode
	}
	return strings.TrimSpace(value), nil
}

func jsonBool(raw json.RawMessage) (bool, error) {
	var value bool
	if errDecode := json.Unmarshal(bytes.TrimSpace(raw), &value); errDecode != nil {
		return false, errDecode
	}
	return value, nil
}

func jsonStringList(raw json.RawMessage) ([]string, error) {
	var values []string
	if errDecode := json.Unmarshal(bytes.TrimSpace(raw), &values); errDecode != nil {
		return nil, errDecode
	}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			return nil, fmt.Errorf("list contains an empty string")
		}
		out = append(out, value)
	}
	return out, nil
}

func uniqueStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func parseModelDocument(raw []byte) ([]json.RawMessage, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return nil, nil
	}
	if raw[0] == '[' {
		var models []json.RawMessage
		if errDecode := json.Unmarshal(raw, &models); errDecode != nil {
			return nil, errDecode
		}
		return models, nil
	}
	doc, errDoc := decodeOrdered(json.NewDecoder(bytes.NewReader(raw)))
	if errDoc != nil {
		return nil, errDoc
	}
	modelsRaw, ok := doc.get("models")
	if !ok {
		return nil, fmt.Errorf("override file needs a models array")
	}
	var models []json.RawMessage
	if errDecode := json.Unmarshal(modelsRaw, &models); errDecode != nil {
		return nil, fmt.Errorf("models: %w", errDecode)
	}
	return models, nil
}
