package main

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"
)

func TestPublishedModelFilesDiffAgainstCapturedCatalog(t *testing.T) {
	if os.Getenv("SYNC_LIVE") == "" {
		t.Skip("set SYNC_LIVE=1 to fetch the published per-model files")
	}
	captured, err := os.ReadFile("/root/CLIPROXYAPI/logs/.plugins/models-cache-override/captured.json")
	if err != nil {
		t.Fatal(err)
	}
	var runtime runtimeState
	runtime.captured = captured
	for _, slug := range []string{"deepseek-v4.1-flash", "MiniMax-M3", "MiniMax-M2.7"} {
		sourceURL, errURL := modelOverridesURL(slug)
		if errURL != nil {
			t.Fatal(errURL)
		}
		body, errFetch := fetchURL(sourceURL)
		if errFetch != nil {
			t.Fatalf("%s: %v", slug, errFetch)
		}
		preview, errPreview := runtime.previewRemoteOverrides(body, sourceURL)
		if errPreview != nil {
			t.Fatal(errPreview)
		}
		if len(preview.Ignored) != 0 {
			t.Fatalf("%s ignored %#v", slug, preview.Ignored)
		}
		t.Logf("%s new=%d conflict=%d", slug, preview.New, preview.Conflict)
		if slug == "MiniMax-M3" {
			found := false
			for _, item := range preview.Items {
				if item.Path == "context_window" && bytes.Contains(item.Remote, []byte("1000000")) {
					found = true
				}
			}
			if !found {
				t.Fatal("MiniMax-M3 context_window override was not detected")
			}
		}
	}
}

func TestModelOverridesURLIsPerSlug(t *testing.T) {
	got, err := modelOverridesURL("deepseek-v4.1-flash")
	if err != nil {
		t.Fatal(err)
	}
	if got != overridesBaseURL+"/deepseek-v4.1-flash.json" {
		t.Fatalf("url = %s", got)
	}
	if _, err := modelOverridesURL("../secret"); err == nil {
		t.Fatal("path traversal was accepted")
	}
	models, err := parseOverrideFile([]byte(`{"slug":"m","display_name":"Mine"}`))
	if err != nil || len(models) != 1 {
		t.Fatalf("single model file = %d %v", len(models), err)
	}
}

func TestSyncSkipsMissingModelAndAppliesChosenFields(t *testing.T) {
	var runtime runtimeState
	runtime.dir = t.TempDir()
	runtime.cfg = resolvedConfig{Mode: modeMerge, MatchBase: true}
	runtime.captured = []byte(`{"models":[{"slug":"m","display_name":"Up","context_window":1}]}`)
	if err := runtime.usePatches(savedPatches{Models: []json.RawMessage{
		[]byte(`{"slug":"m","display_name":"Local","_mode":"merge"}`),
		[]byte(`{"slug":"extra","display_name":"Extra","_insert":true}`),
	}}); err != nil {
		t.Fatal(err)
	}
	preview, err := runtime.previewRemoteOverrides([]byte(`{"models":[
		{"slug":"missing","display_name":"No"},
		{"slug":"m","display_name":"Remote","context_window":9}
	]}`), "mem://overrides.json")
	if err != nil {
		t.Fatal(err)
	}
	if len(preview.Ignored) != 1 || preview.Ignored[0].Slug != "missing" {
		t.Fatalf("ignored = %#v", preview.Ignored)
	}
	var contextID, nameID string
	for _, item := range preview.Items {
		switch item.Path {
		case "context_window":
			if item.Kind != "new" {
				t.Fatalf("context kind = %s", item.Kind)
			}
			contextID = item.ID
		case "display_name":
			if item.Kind != "conflict" {
				t.Fatalf("name kind = %s", item.Kind)
			}
			nameID = item.ID
		}
	}
	if contextID == "" || nameID == "" {
		t.Fatalf("items = %#v", preview.Items)
	}
	if err := runtime.applyRemoteOverrides([]string{contextID}); err != nil {
		t.Fatal(err)
	}
	got, ok := runtime.applyTo(runtime.captured)
	if !ok || !bytes.Contains(got, []byte(`"display_name":"Local"`)) || !bytes.Contains(got, []byte(`"context_window":9`)) {
		t.Fatalf("applied = %s ok=%v", got, ok)
	}
	if bytes.Contains(got, []byte(`"extra"`)) {
		t.Fatalf("unknown model was appended: %s", got)
	}
}
