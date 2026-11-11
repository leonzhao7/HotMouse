package worker

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

const stepOutputDirectory = "${HOTMOUSE_WORKSPACE_DIR}"

// Workspace locations belong to the invocation, rather than a model-generated
// fact: every worker can discover artifacts produced by its peers.
func workspaceAccess(projectDir, workspace, taskType string) (string, error) {
	type artifact struct {
		Path string `yaml:"path"`
		Size int64  `yaml:"size_bytes"`
	}
	view := struct {
		ProjectDir string     `yaml:"project_directory"`
		Writable   string     `yaml:"writable_directory,omitempty"`
		StepOutput string     `yaml:"step_output_directory,omitempty"`
		Workers    []string   `yaml:"worker_directories"`
		Files      []artifact `yaml:"existing_files,omitempty"`
		Limited    bool       `yaml:"file_list_limited,omitempty"`
	}{ProjectDir: projectDir}
	guidance := "Workspace storage convention: each worker leaves all new task artifacts in its own project subdirectory. Browse project_directory, including other workers' directories, to discover and reuse existing artifacts before extracting again. Treat other workers' artifacts as inputs and leave them unchanged. Record actual absolute artifact paths in result.detail/evidence so subsequent steps can read them. Hidden provider state is excluded from this bounded file list.\n"
	switch taskType {
	case "decide":
		view.StepOutput = stepOutputDirectory
		guidance += "You are planning for a future execution worker; its writable directory is not known yet. For every new output, extraction directory or script in a step description, use the literal placeholder ${HOTMOUSE_WORKSPACE_DIR}/<relative-path>. HotMouse resolves this placeholder when the step is assigned to an execution worker. Do not expand it to the dispatcher's current directory or select any directory from worker_directories as the output destination. Existing artifact paths are read-only inputs; keep their actual absolute paths for reuse.\n"
	case "execute":
		view.Writable = workspace
		guidance += "Save all new extracted data, scripts, evidence and outputs under writable_directory. Do not write into another worker's directory or leave new task outputs elsewhere. The step description may contain an output path chosen by a dispatcher or another worker before assignment; relocate that new output under your own writable_directory, keeping its relative filename/subpath. Keep existing input and evidence paths unchanged when reading them. Report the actual output path, not the originally planned destination.\n"
	case "target_probe":
		guidance += "Analyze the supplied initial scope for background target monitoring. Planning and execution run concurrently in their own workspaces. Read only the evidence needed to identify challenge services; preserve existing artifacts and perform no network requests or challenge-solving actions.\n"
	default:
		// Recovery summaries use saved evidence, including legacy sessions whose
		// cwd differs from the directory used for new provider runtime state.
		guidance += "This is a recovery summary of the saved execution session. Preserve recorded artifact paths exactly; do not relocate evidence, create outputs, or perform new actions.\n"
	}
	entries, err := os.ReadDir(filepath.Join(projectDir, "workers"))
	if err != nil {
		return "", err
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		dir := filepath.Join(projectDir, "workers", entry.Name())
		view.Workers = append(view.Workers, dir)
		count := 0
		err := filepath.WalkDir(dir, func(path string, item fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				// Another worker may remove a temporary artifact during the scan.
				return nil
			}
			if path != dir && (strings.HasPrefix(item.Name(), ".") || item.Name() == "node_modules") {
				if item.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if item.IsDir() {
				return nil
			}
			if count >= 20 || len(view.Files) >= 80 {
				view.Limited = true
				return filepath.SkipAll
			}
			info, err := item.Info()
			if err == nil && info.Mode().IsRegular() {
				view.Files = append(view.Files, artifact{Path: path, Size: info.Size()})
				count++
			}
			return nil
		})
		if err != nil {
			return "", err
		}
	}
	data, err := yaml.Marshal(view)
	if err != nil {
		return "", err
	}
	return "\n\nProject workspace access:\n" + guidance + string(data), nil
}

func projectDirectory(workspace string) (string, error) {
	if filepath.Base(filepath.Dir(workspace)) != "workers" {
		return "", fmt.Errorf("worker workspace is not a project subdirectory")
	}
	return filepath.Dir(filepath.Dir(workspace)), nil
}
