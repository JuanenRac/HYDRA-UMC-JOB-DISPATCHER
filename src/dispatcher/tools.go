// HYDRA-UMC-JOB-DISPATCHER - src/dispatcher/tools.go
// Copyright (C) 2026 JuanenRac (Electro Hobby 3D) <electrohobby3d@gmail.com>
// GPL-3.0-or-later - see LICENSE
//
// The catalog of tool names the installation knows (the TOOL_* profiles of the tool heads). It only warns:
// a job that needs a tool nobody has declared, or a robot reporting one, is still accepted, because the
// tool a robot carries is whatever it says it carries. The warning tells the operator that a misspelt
// RequiredTool will never match any robot.
package dispatcher

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"sort"
	"strings"
	"sync"
)

// ToolCatalog is a set of known tool names. A nil catalog knows everything and never warns.
type ToolCatalog struct {
	mu     sync.Mutex
	known  map[string]struct{}
	folded map[string]string // lower-case name -> the name as declared
	warned map[string]struct{}
	logf   func(format string, args ...any)
}

// NewToolCatalog builds a catalog from tool names (blank names are ignored).
func NewToolCatalog(names []string) *ToolCatalog {
	c := &ToolCatalog{
		known:  map[string]struct{}{},
		folded: map[string]string{},
		warned: map[string]struct{}{},
		logf:   func(format string, args ...any) { log.Printf(format, args...) },
	}
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		c.known[name] = struct{}{}
		c.folded[strings.ToLower(name)] = name
	}
	return c
}

// ParseToolNames reads tool names from a JSON document: an array whose items are strings or objects
// with a "name", "id", "tool" or "profile" field, or an object holding such an array under "tools".
func ParseToolNames(data []byte) ([]string, error) {
	var root any
	if err := json.Unmarshal(data, &root); err != nil {
		return nil, fmt.Errorf("tool catalog is not valid JSON: %w", err)
	}
	if object, ok := root.(map[string]any); ok {
		root = object["tools"]
	}
	items, ok := root.([]any)
	if !ok {
		return nil, fmt.Errorf("tool catalog must be a JSON array of names or profiles (or an object with a \"tools\" array)")
	}
	var names []string
	for i, item := range items {
		switch v := item.(type) {
		case string:
			names = append(names, v)
		case map[string]any:
			found := false
			for _, key := range []string{"name", "id", "tool", "profile"} {
				if s, ok := v[key].(string); ok && strings.TrimSpace(s) != "" {
					names = append(names, s)
					found = true
					break
				}
			}
			if !found {
				return nil, fmt.Errorf("tool catalog item %d has no name, id, tool or profile", i)
			}
		default:
			return nil, fmt.Errorf("tool catalog item %d is neither a string nor an object", i)
		}
	}
	return names, nil
}

// LoadToolCatalogFile reads a catalog from a JSON file (see ParseToolNames).
func LoadToolCatalogFile(path string) (*ToolCatalog, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading the tool catalog: %w", err)
	}
	names, err := ParseToolNames(data)
	if err != nil {
		return nil, err
	}
	return NewToolCatalog(names), nil
}

// Names returns the declared tool names, sorted.
func (c *ToolCatalog) Names() []string {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, 0, len(c.known))
	for name := range c.known {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// Knows reports whether the tool is declared (an empty tool, meaning "none" or "any", always is).
func (c *ToolCatalog) Knows(tool string) bool {
	if c == nil || tool == "" {
		return true
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.known[tool]
	return ok
}

// Check logs one warning per unknown tool name (never an error, never a refusal) and reports whether it is known.
// `what` says where the name came from, for example `job "j1" RequiredTool`.
func (c *ToolCatalog) Check(tool, what string) bool {
	if c == nil || tool == "" {
		return true
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.known[tool]; ok {
		return true
	}
	if _, done := c.warned[tool]; done {
		return false
	}
	c.warned[tool] = struct{}{}
	hint := ""
	if declared, ok := c.folded[strings.ToLower(tool)]; ok {
		hint = fmt.Sprintf(" (did you mean %q? tools match exactly, including case)", declared)
	}
	c.logf("[job-dispatcher] WARNING: %s is %q, which is not in the tool catalog%s", what, tool, hint)
	return false
}
