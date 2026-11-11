package worker

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const toolkitDirectoryVariable = "HOTMOUSE_PENTEST_TOOLKIT_DIR"

// The large offline toolkit stays shared on disk rather than being embedded
// or copied into every worker directory. An explicit provider setting wins.
func toolkitDirectory(configured string, starts ...string) (string, error) {
	if configured != "" {
		root, err := filepath.Abs(configured)
		if err != nil {
			return "", err
		}
		info, err := os.Stat(root)
		if err != nil {
			return "", fmt.Errorf("%s: %w", toolkitDirectoryVariable, err)
		}
		if !info.IsDir() {
			return "", fmt.Errorf("%s is not a directory: %s", toolkitDirectoryVariable, root)
		}
		return root, nil
	}
	seen := map[string]bool{}
	for _, start := range starts {
		if start == "" {
			continue
		}
		dir, err := filepath.Abs(start)
		if err != nil {
			continue
		}
		for ; !seen[dir]; dir = filepath.Dir(dir) {
			seen[dir] = true
			for _, relative := range []string{"pentest_toolkit", "backend/pentest_toolkit", "share/hotmouse/pentest_toolkit"} {
				root := filepath.Join(dir, filepath.FromSlash(relative))
				if info, err := os.Stat(root); err == nil && info.IsDir() {
					return root, nil
				}
			}
		}
	}
	return "", nil
}

func bindToolkit(vars map[string]string, cfg workerConfig, task string) error {
	if task != "decide" && task != "execute" && task != "hosted" {
		return nil
	}
	cwd, _ := os.Getwd()
	executable, _ := os.Executable()
	root, err := toolkitDirectory(configEnvironment(cfg, toolkitDirectoryVariable), cwd, filepath.Dir(executable), vars["HOTMOUSE_WORKSPACE_DIR"])
	if err != nil || root == "" {
		return err
	}
	vars[toolkitDirectoryVariable] = root
	var out strings.Builder
	fmt.Fprintf(&out, "Shared local pentest toolkit: %s\nResolve the toolkit's relative examples against this root. Read the phenomenon index for a new observed signal, then search only the relevant local sources with rg. Reuse existing knowledge_refs; a template/CVE match requires verification against the actual target evidence.\n", root)
	for _, item := range []struct{ path, description string }{
		{"README.md", "tool inventory, usage and dependencies"},
		{"kb/INDEX.template.md", "phenomenon to first check and reference path"},
		{"kb/sapper/INDEX.md", "sapper technique index and local documentation links"},
		{"knowledges/hacktricks/src", "offline HackTricks Markdown"},
		{"knowledges/nuclei-templates", "offline YAML references; check nuclei availability before execution"},
		{"exploitdb/files_exploits.csv", "exploit index; inspect referenced source before adapting it"},
		{"exploitdb/exploits", "indexed exploit source files"},
		{"weapons", "documented Python/Bash tools; read usage before calling a specific tool"},
		{"scripts", "scripts by technique; inspect target placeholders and imports before use"},
		{"playbooks", "methodology and prerequisites"},
		{"wordlists", "local input dictionaries; choose only bounded evidence-based candidates"},
		{"bash-helpers", "optional shell helper definitions"},
	} {
		location := filepath.Join(root, filepath.FromSlash(item.path))
		if _, err := os.Stat(location); err == nil {
			fmt.Fprintf(&out, "- %s: %s\n", location, item.description)
		}
	}
	out.WriteString("Keep the shared toolkit unchanged. Use absolute tool paths from the worker cwd, the configured HOTMOUSE_PYTHON (or verified python3) for Python, and bash for shell scripts. Save outputs and auth_queue databases under the current writable workspace; use the toolkit's frozen-queue authentication workflow where applicable. Read needed tool source rather than automatically running or sourcing the whole collection.\n")
	vars["HOTMOUSE_PENTEST_TOOLKIT_PROMPT"] = out.String()
	vars["HOTMOUSE_WORKER_PROMPT"] += "\n\n" + out.String()
	return nil
}
