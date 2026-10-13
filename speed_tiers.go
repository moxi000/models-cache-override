package main

import (
	"bytes"
	"encoding/json"
)

// speedPresets are the Codex catalog pairs from the official models cache.
// fast is stored as additional_speed_tiers "fast" and service tier id "priority".
// ultrafast uses the same id in both fields. The two arrays are always rewritten together.
var speedPresets = []speedPreset{
	{speed: "fast", id: "priority", name: "Fast", description: "2x speed, increased usage"},
	{speed: "ultrafast", id: "ultrafast", name: "Ultrafast", description: "The fastest available responses for latency-sensitive work."},
}

type speedPreset struct {
	speed       string
	id          string
	name        string
	description string
}

type serviceTier struct {
	id          string
	name        string
	description string
}

func bindSpeedTiers(model *orderedObject) {
	if model == nil {
		return
	}
	_, hasSpeed := model.get("additional_speed_tiers")
	_, hasTiers := model.get("service_tiers")
	if !hasSpeed && !hasTiers {
		return
	}
	speeds := parseSpeedList(model)
	tiers := parseServiceTiers(model)
	chosen := make([]speedPreset, 0, len(speedPresets))
	descriptions := make([]string, 0, len(speedPresets))
	for _, preset := range speedPresets {
		if !containsString(speeds, preset.speed) && !hasTierID(tiers, preset.id) {
			continue
		}
		chosen = append(chosen, preset)
		descriptions = append(descriptions, tierDescription(tiers, preset))
	}
	if sameSpeedBinding(speeds, tiers, chosen, descriptions) {
		return
	}
	model.set("additional_speed_tiers", marshalSpeedList(chosen))
	model.set("service_tiers", marshalServiceTiers(chosen, descriptions))
}

func parseSpeedList(model *orderedObject) []string {
	raw, ok := model.get("additional_speed_tiers")
	if !ok || isNull(raw) {
		return nil
	}
	speeds, errList := jsonStringList(raw)
	if errList != nil {
		return nil
	}
	return speeds
}

func parseServiceTiers(model *orderedObject) []serviceTier {
	raw, ok := model.get("service_tiers")
	if !ok || isNull(raw) {
		return nil
	}
	var items []json.RawMessage
	if errDecode := json.Unmarshal(bytes.TrimSpace(raw), &items); errDecode != nil {
		return nil
	}
	tiers := make([]serviceTier, 0, len(items))
	for _, item := range items {
		obj, okObj := asObject(item)
		if !okObj {
			continue
		}
		tiers = append(tiers, serviceTier{
			id:          stringField(obj, "id"),
			name:        stringField(obj, "name"),
			description: stringField(obj, "description"),
		})
	}
	return tiers
}

func tierDescription(tiers []serviceTier, preset speedPreset) string {
	for _, tier := range tiers {
		if tier.id == preset.id && tier.name == preset.name && tier.description != "" {
			return tier.description
		}
	}
	return preset.description
}

func hasTierID(tiers []serviceTier, id string) bool {
	for _, tier := range tiers {
		if tier.id == id {
			return true
		}
	}
	return false
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func sameSpeedBinding(speeds []string, tiers []serviceTier, chosen []speedPreset, descriptions []string) bool {
	if len(speeds) != len(chosen) || len(tiers) != len(chosen) {
		return false
	}
	for i, preset := range chosen {
		if speeds[i] != preset.speed || tiers[i].id != preset.id || tiers[i].name != preset.name || tiers[i].description != descriptions[i] {
			return false
		}
	}
	return true
}

func marshalSpeedList(chosen []speedPreset) json.RawMessage {
	speeds := make([]string, len(chosen))
	for i, preset := range chosen {
		speeds[i] = preset.speed
	}
	raw, errMarshal := json.Marshal(speeds)
	if errMarshal != nil {
		return json.RawMessage("[]")
	}
	return raw
}

func marshalServiceTiers(chosen []speedPreset, descriptions []string) json.RawMessage {
	items := make([]json.RawMessage, len(chosen))
	for i, preset := range chosen {
		obj := newOrdered()
		obj.set("id", rawJSONString(preset.id))
		obj.set("name", rawJSONString(preset.name))
		obj.set("description", rawJSONString(descriptions[i]))
		items[i] = obj.marshal()
	}
	return marshalArray(items)
}

func rawJSONString(value string) json.RawMessage {
	raw, errMarshal := json.Marshal(value)
	if errMarshal != nil {
		return json.RawMessage(`""`)
	}
	return raw
}
