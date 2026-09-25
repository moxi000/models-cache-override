package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

type pluginSettings struct {
	File      string `yaml:"file"`
	Mode      string `yaml:"mode"`
	Insert    bool   `yaml:"insert"`
	MatchBase *bool  `yaml:"match-base"`
	Models    []any  `yaml:"models"`
}

type resolvedConfig struct {
	File      string
	Mode      string
	Insert    bool
	MatchBase bool
	Inline    []json.RawMessage
}

type runtimeState struct {
	mu          sync.Mutex
	cfg         resolvedConfig
	dir         string
	modTime     int64
	size        int64
	snap        *snapshot
	loadErr     string
	patches     savedPatches
	captured    []byte
	capturedAt  time.Time
	upstream    []byte
	upstreamAt  time.Time
	upstreamURL string
	syncPreview syncPreview
}

func (s *runtimeState) configure(configYAML []byte) error {
	cfg, errResolve := resolveConfig(configYAML)
	if errResolve != nil {
		return errResolve
	}
	s.mu.Lock()
	s.cfg = cfg
	s.dir = defaultDataDir
	s.mu.Unlock()
	if strings.TrimSpace(cfg.File) == "" && len(cfg.Inline) == 0 {
		s.loadCachedFiles()
		return s.loadPatches()
	}
	snap, errSnap := buildSnapshot(cfg)
	if errSnap != nil {
		return errSnap
	}
	info, errStat := statFile(cfg.File)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.snap = snap
	s.loadErr = ""
	if errStat != nil {
		s.modTime = 0
		s.size = 0
		if cfg.File != "" {
			s.loadErr = errStat.Error()
		}
	} else if info != nil {
		s.modTime = info.modUnix
		s.size = info.size
	} else {
		s.modTime = 0
		s.size = 0
	}
	return nil
}

func (s *runtimeState) current() *snapshot {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reloadLocked()
	return s.snap
}

func (s *runtimeState) reloadLocked() {
	if strings.TrimSpace(s.cfg.File) == "" {
		return
	}
	info, errStat := statFile(s.cfg.File)
	if errStat != nil {
		s.loadErr = errStat.Error()
		return
	}
	if info == nil || (info.modUnix == s.modTime && info.size == s.size && s.snap != nil && s.loadErr == "") {
		return
	}
	snap, errSnap := buildSnapshot(s.cfg)
	if errSnap != nil {
		s.loadErr = errSnap.Error()
		return
	}
	s.snap = snap
	s.modTime = info.modUnix
	s.size = info.size
	s.loadErr = ""
}

func (s *runtimeState) status() (int, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	count := 0
	if s.snap != nil {
		count = len(s.snap.overrides)
	}
	return count, s.loadErr
}

type fileInfo struct {
	modUnix int64
	size    int64
}

func statFile(path string) (*fileInfo, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, nil
	}
	info, errStat := os.Stat(path)
	if errStat != nil {
		return nil, errStat
	}
	return &fileInfo{modUnix: info.ModTime().UnixNano(), size: info.Size()}, nil
}

func resolveConfig(configYAML []byte) (resolvedConfig, error) {
	var settings pluginSettings
	if len(bytes.TrimSpace(configYAML)) > 0 {
		if errDecode := yaml.Unmarshal(configYAML, &settings); errDecode != nil {
			return resolvedConfig{}, errDecode
		}
	}
	mode := strings.TrimSpace(settings.Mode)
	if mode == "" {
		mode = modeMerge
	}
	if mode != modeMerge && mode != modeReplace {
		return resolvedConfig{}, fmt.Errorf("mode must be merge or replace")
	}
	matchBase := true
	if settings.MatchBase != nil {
		matchBase = *settings.MatchBase
	}
	inline := make([]json.RawMessage, 0, len(settings.Models))
	for i, model := range settings.Models {
		raw, errEncode := json.Marshal(model)
		if errEncode != nil {
			return resolvedConfig{}, fmt.Errorf("models[%d]: %w", i, errEncode)
		}
		inline = append(inline, raw)
	}
	return resolvedConfig{
		File:      strings.TrimSpace(settings.File),
		Mode:      mode,
		Insert:    settings.Insert,
		MatchBase: matchBase,
		Inline:    inline,
	}, nil
}

func buildSnapshot(cfg resolvedConfig) (*snapshot, error) {
	raws := make([]json.RawMessage, 0)
	if cfg.File != "" {
		fileRaw, errRead := os.ReadFile(cfg.File)
		if errRead != nil {
			return nil, fmt.Errorf("read override file: %w", errRead)
		}
		fileModels, errParse := parseModelDocument(fileRaw)
		if errParse != nil {
			return nil, fmt.Errorf("parse override file: %w", errParse)
		}
		raws = append(raws, fileModels...)
	}
	raws = append(raws, cfg.Inline...)
	overrides := make([]compiledOverride, 0, len(raws))
	for i, raw := range raws {
		ov, errCompile := compileOverride(raw, i, cfg.Mode)
		if errCompile != nil {
			return nil, fmt.Errorf("models[%d]: %w", i, errCompile)
		}
		overrides = append(overrides, ov)
	}
	return &snapshot{
		overrides: overrides,
		insert:    cfg.Insert,
		matchBase: cfg.MatchBase,
	}, nil
}
