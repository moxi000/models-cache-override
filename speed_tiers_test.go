package main

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"
)

func TestBindSpeedTiersAddsUltrafastPreset(t *testing.T) {
	model := objectFromJSON(t, `{
		"slug":"gpt-6-astra",
		"additional_speed_tiers":["fast","ultrafast"],
		"service_tiers":[{"description":"2x speed, increased usage","id":"priority","name":"Fast"}]
	}`)
	bindSpeedTiers(model)
	got := string(model.marshal())
	wantSpeed := `"additional_speed_tiers":["fast","ultrafast"]`
	wantFast := `"service_tiers":[{"id":"priority","name":"Fast","description":"2x speed, increased usage"},{"id":"ultrafast","name":"Ultrafast","description":"The fastest available responses for latency-sensitive work."}]`
	if !bytes.Contains([]byte(got), []byte(wantSpeed)) || !bytes.Contains([]byte(got), []byte(wantFast)) {
		t.Fatalf("bound catalog = %s", got)
	}
}

func TestBindSpeedTiersDropsHandWrittenTier(t *testing.T) {
	model := objectFromJSON(t, `{
		"slug":"gpt-6-sol",
		"additional_speed_tiers":["fast","custom"],
		"service_tiers":[{"id":"","name":"Typed","description":"no"}]
	}`)
	bindSpeedTiers(model)
	var decoded map[string]any
	if err := json.Unmarshal(model.marshal(), &decoded); err != nil {
		t.Fatal(err)
	}
	speeds := decoded["additional_speed_tiers"].([]any)
	if len(speeds) != 1 || speeds[0] != "fast" {
		t.Fatalf("speeds = %#v", speeds)
	}
	tiers := decoded["service_tiers"].([]any)
	if len(tiers) != 1 {
		t.Fatalf("tiers = %#v", tiers)
	}
	tier := tiers[0].(map[string]any)
	if tier["id"] != "priority" || tier["name"] != "Fast" || tier["description"] != "2x speed, increased usage" {
		t.Fatalf("tier = %#v", tier)
	}
}

func TestBindSpeedTiersKeepsExistingFastDescription(t *testing.T) {
	raw := []byte(`{"slug":"gpt-6-sol","additional_speed_tiers":["fast"],"service_tiers":[{"id":"priority","name":"Fast","description":"1.5x speed"}]}`)
	model := objectFromJSON(t, string(raw))
	before := model.marshal()
	bindSpeedTiers(model)
	if !bytes.Equal(before, model.marshal()) {
		t.Fatalf("unchanged binding was rewritten: %s", model.marshal())
	}
}

func TestLiveAstraUltrafastPatch(t *testing.T) {
	raw, errRead := os.ReadFile("/tmp/issued-models.json")
	if errRead != nil {
		t.Skip(errRead)
	}
	var doc struct {
		Models []json.RawMessage `json:"models"`
	}
	if errDecode := json.Unmarshal(raw, &doc); errDecode != nil {
		t.Fatal(errDecode)
	}
	var upstream json.RawMessage
	for _, model := range doc.Models {
		obj, ok := asObject(model)
		if ok && stringField(obj, "slug") == "gpt-6-astra" {
			upstream = bytes.TrimSpace(model)
			break
		}
	}
	if len(upstream) == 0 {
		t.Fatal("gpt-6-astra missing")
	}
	edited, ok := asObject(upstream)
	if !ok {
		t.Fatal("astra is not an object")
	}
	edited.set("additional_speed_tiers", json.RawMessage(`["fast","ultrafast"]`))
	bindSpeedTiers(edited)
	patch, errPatch := diffModel(upstream, edited.marshal())
	if errPatch != nil {
		t.Fatal(errPatch)
	}
	if !bytes.Contains(patch, []byte(`"ultrafast"`)) || bytes.Contains(patch, []byte(`"base_instructions"`)) {
		t.Fatalf("patch = %s", patch)
	}
}

func objectFromJSON(t *testing.T, raw string) *orderedObject {
	t.Helper()
	obj, ok := asObject(json.RawMessage(raw))
	if !ok {
		t.Fatal("not an object")
	}
	return obj
}
