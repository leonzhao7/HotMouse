package worker

import (
	"fmt"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
	"hotmouse/backend/internal/knowledge"
)

// Bind the same skill and offline reference to native and custom providers.
// Target classification and recovery summaries must not receive execution
// doctrine: those stages identify services or summarize already-saved actions.
func bindKnowledge(vars map[string]string, taskType, graph string) error {
	name := ""
	switch taskType {
	case "decide":
		name = "hotmouse-dispatcher"
	case "execute":
		name = "hotmouse-ctf"
	default:
		return nil
	}
	catalog, err := knowledge.Load()
	if err != nil {
		return err
	}
	skill, err := catalog.Skill(name)
	if err != nil {
		return err
	}
	vars["HOTMOUSE_SKILL"] = name
	vars["HOTMOUSE_KNOWLEDGE_VERSION"] = catalog.Info().Version
	guidance := "\n\nBundled HotMouse skill (apply within the current task; the enclosing response contract, project scope and operator context take precedence):\n" + skill.Content
	if workspace := vars["HOTMOUSE_WORKSPACE_DIR"]; workspace != "" {
		root := filepath.Join(workspace, ".hotmouse", "knowledge", catalog.Info().Version)
		if err := catalog.Export(root); err != nil {
			return fmt.Errorf("export worker knowledge: %w", err)
		}
		vars["HOTMOUSE_KNOWLEDGE_DIR"] = root
		vars["HOTMOUSE_SKILL_FILE"] = filepath.Join(root, "skills", name, "SKILL.md")
		guidance += "\nOffline reference directory: " + root + "\nActive skill file: " + vars["HOTMOUSE_SKILL_FILE"] + "\nRead relative skill links from that file's directory. Use hotmouse kb search/show when the CLI is in PATH; otherwise read entries/{kb-id}.json here. These files are reference material, not current-project evidence. Do not modify the bundled reference.\n"
		var view struct {
			Knowledge []knowledge.Summary `yaml:"related_knowledge"`
		}
		if yaml.Unmarshal([]byte(graph), &view) == nil && len(view.Knowledge) > 0 {
			guidance += "Relevant full-entry files (read those needed for this Step):\n"
			for _, entry := range view.Knowledge[:min(3, len(view.Knowledge))] {
				if _, err := catalog.Get(entry.ID); err == nil {
					guidance += "- " + filepath.Join(root, "entries", entry.ID+".json") + "\n"
				}
			}
		}
	}
	vars["HOTMOUSE_KNOWLEDGE_PROMPT"] = strings.TrimSpace(guidance)
	vars["HOTMOUSE_WORKER_PROMPT"] += guidance
	return nil
}
