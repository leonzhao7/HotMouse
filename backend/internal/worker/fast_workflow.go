package worker

import (
	"bytes"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
	"hotmouse/backend/internal/ctfplatform"
)

//go:embed ctf_fast.py
var fastWorkflowScript []byte

var capabilityCache = struct {
	sync.Mutex
	data map[string][]byte
}{data: map[string][]byte{}}

func configEnvironment(cfg workerConfig, key string) string {
	if value, ok := cfg.env[key]; ok {
		return value
	}
	return os.Getenv(key)
}

// Do not resolve venv symlinks: the invoked bin/python path selects sys.prefix.
func workflowPython(cfg workerConfig, root string) (string, error) {
	if configured := configEnvironment(cfg, "HOTMOUSE_PYTHON"); configured != "" {
		if !filepath.IsAbs(configured) {
			return "", fmt.Errorf("HOTMOUSE_PYTHON must be an absolute interpreter path")
		}
		if _, err := os.Stat(configured); err != nil {
			return "", err
		}
		return configured, nil
	}
	cwd, _ := os.Getwd()
	for _, start := range []string{root, cwd} {
		for dir := start; ; dir = filepath.Dir(dir) {
			for _, binary := range []string{"python", "python3"} {
				path := filepath.Join(dir, ".venv", "bin", binary)
				if info, err := os.Stat(path); err == nil && !info.IsDir() {
					return path, nil
				}
			}
			if filepath.Dir(dir) == dir {
				break
			}
		}
	}
	path, err := exec.LookPath("python3")
	if err != nil {
		return "", err
	}
	return filepath.Abs(path)
}

func writeWorkflowFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if existing, err := os.ReadFile(path); err == nil && bytes.Equal(existing, data) {
		return nil
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".pending-")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	_, err = file.Write(data)
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(file.Name(), path)
}

func capabilityKey(python string, cfg workerConfig) string {
	// Package installation changes the venv site-packages timestamp. An operator
	// can also invalidate a pre-game inventory via HOTMOUSE_CAPABILITY_REVISION.
	stamps := []any{python, string(fastWorkflowScript)}
	for _, key := range []string{"PATH", "PYTHONPATH", "PYTHONHOME", "LD_LIBRARY_PATH", "HOTMOUSE_CAPABILITY_REVISION"} {
		stamps = append(stamps, key, configEnvironment(cfg, key))
	}
	paths := []string{python, filepath.Join(filepath.Dir(filepath.Dir(python)), "pyvenv.cfg")}
	sites, _ := filepath.Glob(filepath.Join(filepath.Dir(filepath.Dir(python)), "lib", "python*", "site-packages"))
	paths = append(paths, sites...)
	for _, path := range paths {
		if info, err := os.Stat(path); err == nil {
			stamps = append(stamps, path, info.ModTime().UnixNano(), info.Size())
		}
	}
	data, _ := json.Marshal(stamps)
	return fmt.Sprintf("%x", sha256.Sum256(data))[:24]
}

func (r *Runner) bindFastWorkflow(ctx context.Context, vars map[string]string, cfg workerConfig, graph, task string) error {
	if task != "decide" && task != "execute" || vars["HOTMOUSE_WORKSPACE_DIR"] == "" {
		return nil
	}
	workspace := vars["HOTMOUSE_WORKSPACE_DIR"]
	root, err := r.workspaceRoot(cfg)
	if err != nil {
		return err
	}
	python, err := workflowPython(cfg, root)
	if err != nil {
		vars["HOTMOUSE_WORKER_PROMPT"] += "\nFixed preparation unavailable: " + err.Error() + ". Check only capabilities required by the current evidence.\n"
		return nil
	}
	vars["HOTMOUSE_PYTHON"] = python
	helper := filepath.Join(workspace, ".hotmouse", "ctf-fast.py")
	if err := writeWorkflowFile(helper, fastWorkflowScript); err != nil {
		return err
	}
	vars["HOTMOUSE_CTF_HELPER"] = helper
	vars["HOTMOUSE_RESULT_INBOX"] = filepath.Join(workspace, ".hotmouse", "results", vars["HOTMOUSE_STEP_ID"])
	if cfg.driver == "mock" && cfg.command == "" {
		return nil
	}
	capabilityCache.Lock()
	key := capabilityKey(python, cfg)
	capPath := filepath.Join(root, ".hotmouse", "capabilities", key+".json")
	data := capabilityCache.data[key]
	if len(data) == 0 {
		data, _ = os.ReadFile(capPath)
	}
	if !json.Valid(data) {
		process := runProcess(ctx, []string{python, helper, "capabilities", "--output", capPath}, cfg.env, workspace, nil, 15*time.Second, nil)
		data, _ = os.ReadFile(capPath)
		if process.err != nil || !json.Valid(data) {
			capabilityCache.Unlock()
			vars["HOTMOUSE_WORKER_PROMPT"] += "\nCapability inventory failed; use the explicit interpreter " + python + " and check only required imports.\n"
			return nil
		}
	}
	capabilityCache.data[key] = data
	err = writeWorkflowFile(capPath, data)
	capabilityCache.Unlock()
	if err != nil {
		return err
	}
	vars["HOTMOUSE_CAPABILITIES_FILE"] = capPath
	vars["HOTMOUSE_WORKER_PROMPT"] += "\n\nFixed capability inventory (reuse; no separate environment-inventory Step):\n" + string(data) + "\nCapability evidence: " + capPath + "\nExplicit root Python: " + python + "; helper: " + helper + "\nUse <venv>/bin/python -m pip and the SAME <venv>/bin/python for subsequent scripts/imports. For a dedicated venv, run this helper's capabilities --output once with that interpreter and reuse the saved report. Never resolve its symlink to the system interpreter. After acquiring/extracting new inputs in an execution Step, batch their fixed inspection with: \"$HOTMOUSE_PYTHON\" \"$HOTMOUSE_CTF_HELPER\" inspect /absolute/input1 /absolute/input2 --output \"$HOTMOUSE_WORKSPACE_DIR/inspection.json\".\n"
	var view struct {
		Platform *struct {
			Delivery ctfplatform.Delivery `yaml:"delivery"`
		} `yaml:"platform"`
	}
	// Platform delivery is already supplied by import; never ask the LLM to fetch
	// the catalog again merely to recover addresses, attachment URLs or counts.
	if err := yaml.Unmarshal([]byte(graph), &view); err != nil || view.Platform == nil {
		return nil
	}
	manifest, _ := json.Marshal(map[string]any{"attachments": view.Platform.Delivery.Attachments})
	signature := append(append([]byte{}, manifest...), fastWorkflowScript...)
	for _, attachment := range view.Platform.Delivery.Attachments {
		if info, err := os.Stat(attachment.Path); err == nil {
			signature = append(signature, []byte(fmt.Sprint(info.ModTime().UnixNano(), info.Size()))...)
		}
	}
	manifestKey := fmt.Sprintf("%x", sha256.Sum256(signature))[:24]
	unlock := r.lockWorkspace("preflight/" + vars["HOTMOUSE_PROJECT_ID"])
	defer unlock()
	paths, _ := filepath.Glob(filepath.Join(vars["HOTMOUSE_PROJECT_DIR"], "workers", "*", ".hotmouse", "preflight", manifestKey, "report.json"))
	var report []byte
	var reportPath string
	for _, path := range paths {
		if value, err := os.ReadFile(path); err == nil && reusablePreparation(value) {
			report, reportPath = value, path
			break
		}
	}
	if len(report) == 0 {
		dir := filepath.Join(workspace, ".hotmouse", "preflight", manifestKey)
		manifestPath := filepath.Join(dir, "manifest.json")
		if err := writeWorkflowFile(manifestPath, manifest); err != nil {
			return err
		}
		reportPath = filepath.Join(dir, "report.json")
		process := runProcess(ctx, []string{python, helper, "prepare", "--manifest", manifestPath, "--directory", filepath.Join(dir, "attachments"), "--output", reportPath}, cfg.env, workspace, nil, 30*time.Second, nil)
		report, _ = os.ReadFile(reportPath)
		if process.err != nil || !json.Valid(report) {
			vars["HOTMOUSE_WORKER_PROMPT"] += "\nFixed attachment preparation incomplete. Reuse downloaded files under " + dir + "; run the helper prepare command once for the missing evidence.\n"
			return nil
		}
	}
	vars["HOTMOUSE_PREFLIGHT_FILE"] = reportPath
	vars["HOTMOUSE_WORKER_PROMPT"] += "\n\nFixed project preparation: " + reportPath + "\n" + briefWorkflowReport(report) + "\nRead this saved report for remaining symbols/strings; reuse its local attachment paths. Do not repeat VPN/platform catalog, download, file/strings/readelf or capability checks already recorded. Failed checks are evidence of an unresolved prerequisite.\n"
	return nil
}

func reusablePreparation(data []byte) bool {
	var report struct {
		Attachments []struct {
			Path string `json:"path"`
			Size int64  `json:"size_bytes"`
		} `json:"attachments"`
	}
	if json.Unmarshal(data, &report) != nil {
		return false
	}
	for _, attachment := range report.Attachments {
		if attachment.Path == "" {
			continue // Preserve failed checks; an executor can fix the missing prerequisite.
		}
		info, err := os.Stat(attachment.Path)
		if err != nil || !info.Mode().IsRegular() || info.Size() != attachment.Size {
			return false
		}
	}
	return true
}

func briefWorkflowReport(raw []byte) string {
	var report map[string]any
	_ = json.Unmarshal(raw, &report)
	files, _ := report["attachments"].([]any)
	for _, item := range files {
		file, _ := item.(map[string]any)
		for _, key := range []string{"symbols", "strings", "headers"} {
			if scan, ok := file[key].(map[string]any); ok {
				if output, ok := scan["output"].(string); ok && len(output) > 800 {
					scan["output"] = output[:800] + "… (see full saved report)"
				}
			}
		}
	}
	data, _ := json.Marshal(report)
	return strings.TrimSpace(string(data))
}
