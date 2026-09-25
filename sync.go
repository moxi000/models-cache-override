package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

const overridesBaseURL = "https://raw.githubusercontent.com/moxi000/models-cache-override/main/overrides"

type syncItem struct {
	ID       string          `json:"id"`
	Slug     string          `json:"slug"`
	Path     string          `json:"path"`
	Kind     string          `json:"kind"`
	Upstream json.RawMessage `json:"upstream,omitempty"`
	Local    json.RawMessage `json:"local,omitempty"`
	Remote   json.RawMessage `json:"remote"`
}

type syncIgnored struct {
	Slug   string `json:"slug"`
	Reason string `json:"reason"`
}

type syncPreview struct {
	URL      string        `json:"url"`
	Ignored  []syncIgnored `json:"ignored"`
	Items    []syncItem    `json:"items"`
	New      int           `json:"new"`
	Conflict int           `json:"conflict"`
}

func modelOverridesURL(slug string) (string, error) {
	slug = strings.TrimSpace(slug)
	if slug == "" || strings.Contains(slug, "/") || strings.Contains(slug, `\`) || strings.Contains(slug, "..") {
		return "", fmt.Errorf("无效的模型名")
	}
	return overridesBaseURL + "/" + strings.ReplaceAll(slug, " ", "%20") + ".json", nil
}

func parseOverrideFile(raw []byte) ([]json.RawMessage, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) > 0 && raw[0] == '{' {
		obj, ok := asObject(raw)
		if ok {
			if _, hasModels := obj.get("models"); !hasModels && stringField(obj, "slug") != "" {
				return []json.RawMessage{append(json.RawMessage(nil), raw...)}, nil
			}
		}
	}
	return parseModelDocument(raw)
}

func (s *runtimeState) previewRemoteOverrides(raw []byte, sourceURL string) (syncPreview, error) {
	remoteModels, errParse := parseOverrideFile(raw)
	if errParse != nil {
		return syncPreview{}, fmt.Errorf("仓库配置: %w", errParse)
	}
	base, _ := s.deliveryBase()
	baseModels, _ := parseModelDocument(base)
	upstreamBySlug := indexModels(baseModels)
	s.mu.Lock()
	localBySlug := indexModels(s.patches.Models)
	s.mu.Unlock()

	preview := syncPreview{URL: sourceURL, Ignored: []syncIgnored{}, Items: []syncItem{}}
	for _, remoteRaw := range remoteModels {
		remoteObj, ok := asObject(remoteRaw)
		if !ok {
			continue
		}
		slug := stringField(remoteObj, "slug")
		if slug == "" {
			continue
		}
		upstreamRaw := upstreamBySlug[slug]
		if len(upstreamRaw) == 0 {
			preview.Ignored = append(preview.Ignored, syncIgnored{Slug: slug, Reason: "上游目录没有这个模型，已忽略"})
			continue
		}
		upstreamFlat := flattenModel(upstreamRaw)
		localFlat := flattenModel(localBySlug[slug])
		for path, remoteValue := range flattenModel(remoteRaw) {
			upstreamValue := upstreamFlat[path]
			localValue, localSet := localFlat[path]
			if !localSet {
				localValue = upstreamValue
			}
			if rawEqual(remoteValue, localValue) {
				continue
			}
			kind := "new"
			if localSet && !rawEqual(localValue, upstreamValue) && !rawEqual(localValue, remoteValue) {
				kind = "conflict"
			} else if localSet && rawEqual(remoteValue, upstreamValue) && !rawEqual(localValue, upstreamValue) {
				kind = "conflict"
			}
			if kind == "new" {
				preview.New++
			} else {
				preview.Conflict++
			}
			preview.Items = append(preview.Items, syncItem{
				ID:       syncID(slug, path),
				Slug:     slug,
				Path:     path,
				Kind:     kind,
				Upstream: cloneRaw(upstreamValue),
				Local:    cloneRaw(localValue),
				Remote:   bytes.TrimSpace(remoteValue),
			})
		}
	}
	s.mu.Lock()
	s.syncPreview = preview
	s.mu.Unlock()
	return preview, nil
}

func (s *runtimeState) fetchRemoteOverrides(slug string) (syncPreview, error) {
	sourceURL, errURL := modelOverridesURL(slug)
	if errURL != nil {
		return syncPreview{}, errURL
	}
	body, errFetch := fetchURL(sourceURL)
	if errFetch != nil {
		return syncPreview{}, fmt.Errorf("读取 %s 失败：%w", slug, errFetch)
	}
	return s.previewRemoteOverrides(body, sourceURL)
}

func (s *runtimeState) applyRemoteOverrides(ids []string) error {
	s.mu.Lock()
	preview := s.syncPreview
	s.mu.Unlock()
	if preview.URL == "" {
		return fmt.Errorf("请先拉取仓库配置")
	}
	selected := map[string]struct{}{}
	for _, id := range ids {
		selected[id] = struct{}{}
	}
	bySlug := map[string][]syncItem{}
	for _, item := range preview.Items {
		if _, ok := selected[item.ID]; !ok {
			continue
		}
		bySlug[item.Slug] = append(bySlug[item.Slug], item)
	}
	base, _ := s.deliveryBase()
	upstreamBySlug := indexModels(mustModels(base))
	for slug, items := range bySlug {
		upstream := upstreamBySlug[slug]
		if len(upstream) == 0 {
			continue
		}
		edited := mergePatch(upstream, s.patchFor(slug))
		obj, ok := asObject(edited)
		if !ok {
			return fmt.Errorf("%s 不是对象", slug)
		}
		for _, item := range items {
			setDotted(obj, item.Path, item.Remote)
		}
		if errSave := s.editModel("set-model", slug, obj.marshal()); errSave != nil {
			return errSave
		}
	}
	return nil
}

func (s *runtimeState) patchFor(slug string) json.RawMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, raw := range s.patches.Models {
		obj, ok := asObject(raw)
		if ok && stringField(obj, "slug") == slug {
			return raw
		}
	}
	return nil
}

func mergePatch(baseRaw, patchRaw json.RawMessage) json.RawMessage {
	base, okBase := asObject(baseRaw)
	patch, okPatch := asObject(patchRaw)
	if !okBase {
		return baseRaw
	}
	if !okPatch {
		return base.marshal()
	}
	patch.delete("_mode")
	patch.delete("_insert")
	patch.delete("_match")
	patch.delete("_delete")
	return mergeObjects(base, patch).marshal()
}

func flattenModel(raw json.RawMessage) map[string]json.RawMessage {
	obj, ok := asObject(raw)
	if !ok {
		return map[string]json.RawMessage{}
	}
	out := map[string]json.RawMessage{}
	var walk func(prefix string, node *orderedObject)
	walk = func(prefix string, node *orderedObject) {
		for _, key := range node.keys {
			if prefix == "" && (key == "slug" || strings.HasPrefix(key, "_")) {
				continue
			}
			path := key
			if prefix != "" {
				path = prefix + "." + key
			}
			value := node.vals[key]
			if child, okChild := asObject(value); okChild {
				walk(path, child)
				continue
			}
			out[path] = bytes.TrimSpace(value)
		}
	}
	walk("", obj)
	return out
}

func syncID(slug, path string) string {
	sum := sha256.Sum256([]byte(slug + "\n" + path))
	return hex.EncodeToString(sum[:8])
}

func setDotted(obj *orderedObject, path string, value json.RawMessage) {
	head, tail, nested := strings.Cut(path, ".")
	if !nested {
		obj.set(head, value)
		return
	}
	childRaw, _ := obj.get(head)
	child, okChild := asObject(childRaw)
	if !okChild {
		child = newOrdered()
	}
	setDotted(child, tail, value)
	obj.set(head, child.marshal())
}

func cloneRaw(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	return append(json.RawMessage(nil), bytes.TrimSpace(raw)...)
}

func mustModels(raw []byte) []json.RawMessage {
	models, err := parseModelDocument(raw)
	if err != nil {
		return nil
	}
	return models
}
