package worker

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

func readContextDocument(path string) (map[string]any, error) {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read project context: %w", err)
	}
	var doc map[string]any
	decoder := yaml.NewDecoder(bytes.NewReader(b))
	if err := decoder.Decode(&doc); err != nil && err != io.EOF {
		return nil, fmt.Errorf("invalid project context YAML in %s", path)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, fmt.Errorf("expected one project context document in %s", path)
	}
	return doc, nil
}

func mergeContextMaps(base, override map[string]any) map[string]any {
	merged := make(map[string]any, len(base)+len(override))
	for key, value := range base {
		merged[key] = value
	}
	for key, value := range override {
		baseMap, baseOK := merged[key].(map[string]any)
		overrideMap, overrideOK := value.(map[string]any)
		if baseOK && overrideOK {
			merged[key] = mergeContextMaps(baseMap, overrideMap)
		} else {
			merged[key] = value
		}
	}
	return merged
}

func applyContextDocument(context, document map[string]any, allowOverride bool) map[string]any {
	merged := mergeContextMaps(context, nil)
	for _, section := range []string{"authorization", "scope", "output", "project"} {
		if value, ok := document[section].(map[string]any); ok {
			current, _ := merged[section].(map[string]any)
			merged[section] = mergeContextMaps(current, value)
		}
	}
	if preamble, ok := document["prompt_preamble"].(string); ok && preamble != "" {
		merged["prompt_preamble"] = preamble
	}
	if allowOverride {
		if overrides, ok := document["override"].(map[string]any); ok {
			for _, section := range []string{"authorization", "scope", "output"} {
				if value, ok := overrides[section].(map[string]any); ok {
					current, _ := merged[section].(map[string]any)
					merged[section] = mergeContextMaps(current, value)
				}
			}
		}
	}
	return merged
}

func loadProjectContext(root, project, origin, goal string) (string, error) {
	if root == "" {
		return "", nil
	}
	root, err := filepath.Abs(filepath.Clean(root))
	if err != nil {
		return "", err
	}
	defaultPath := filepath.Join(root, "default.project-context.yaml")
	projectPath := filepath.Join(root, "projects", project+".project-context.yaml")
	context := map[string]any{
		"schema_version": 1,
		"project":        map[string]any{"project_id": project, "target_summary": origin, "goal": goal, "notes": ""},
		"authorization":  map[string]any{}, "scope": map[string]any{}, "output": map[string]any{}, "prompt_preamble": "",
	}
	defaults, err := readContextDocument(defaultPath)
	if err != nil {
		return "", err
	}
	override, err := readContextDocument(projectPath)
	if err != nil {
		return "", err
	}
	context = applyContextDocument(context, defaults, false)
	context = applyContextDocument(context, override, true)
	if projectSection, ok := context["project"].(map[string]any); ok {
		if projectSection["project_id"] == "" || projectSection["project_id"] == nil {
			projectSection["project_id"] = project
		}
		if projectSection["target_summary"] == "" || projectSection["target_summary"] == nil {
			projectSection["target_summary"] = origin
		}
		if projectSection["goal"] == "" || projectSection["goal"] == nil {
			projectSection["goal"] = goal
		}
	}
	b, err := yaml.Marshal(context)
	if err != nil {
		return "", err
	}
	return string(b), nil
}
