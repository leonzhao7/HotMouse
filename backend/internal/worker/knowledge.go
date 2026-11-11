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
		vars["HOTMOUSE_SKILLS_DIR"] = filepath.Join(root, "skills")
		guidance += "\nOffline reference directory: " + root + "\nActive skill file: " + vars["HOTMOUSE_SKILL_FILE"] + "\nThe active skill content is already included above; do not read it again. Read relative reference links only for a concrete missing detail. Take a real sample first and reuse working_state knowledge_refs before another search. Use hotmouse kb search/show when the CLI is in PATH; otherwise read entries/{kb-id}.json here. These files are reference material, not current-project evidence. Do not modify the bundled reference.\n"
		var view struct {
			Knowledge []knowledge.Summary `yaml:"related_knowledge"`
		}
		if yaml.Unmarshal([]byte(graph), &view) == nil && len(view.Knowledge) > 0 {
			guidance += "Relevant full-entry files (batch needed entries in one read round; retain their useful conclusions and knowledge_refs for reuse):\n"
			for _, entry := range view.Knowledge[:min(3, len(view.Knowledge))] {
				if _, err := catalog.Get(entry.ID); err == nil {
					guidance += "- " + filepath.Join(root, "entries", entry.ID+".json") + "\n"
				}
			}
		}
	}
	vars["HOTMOUSE_SKILLS_PROMPT"] = specialistSkillGuidance(catalog, vars["HOTMOUSE_SKILLS_DIR"], taskType)
	guidance += "\n\n" + vars["HOTMOUSE_SKILLS_PROMPT"]
	vars["HOTMOUSE_KNOWLEDGE_PROMPT"] = strings.TrimSpace(guidance)
	vars["HOTMOUSE_WORKER_PROMPT"] += guidance
	return nil
}

// Native CLIs and command adapters receive the same skill index, without
// depending on provider-specific automatic skill discovery.
func specialistSkillGuidance(catalog *knowledge.Catalog, directory, task string) string {
	var out strings.Builder
	out.WriteString("Imported CTF skill routing: use the current evidence to select a specialist skill and read its SKILL.md before applying its method. Reuse previously read methods and knowledge_refs; do not load every skill or repeat checks already recorded.\n")
	if directory != "" {
		fmt.Fprintf(&out, "Skill directory: %s\n", directory)
	}
	for _, skill := range catalog.Skills() {
		if skill.Name == "hotmouse-dispatcher" || skill.Name == "hotmouse-ctf" {
			continue
		}
		location := "hotmouse kb skill " + skill.Name + " --select content"
		if directory != "" {
			location = filepath.Join(directory, skill.Name, "SKILL.md")
		}
		fmt.Fprintf(&out, "- %s: %s\n  Read: %s\n", skill.Name, skill.Description, location)
	}
	if task == "execute" || task == "hosted" {
		if skill, err := catalog.Skill("flag-hunting"); err == nil {
			out.WriteString("\nActive routing skill (already included; do not read it again):\n" + skill.Content + "\n")
		}
	}
	out.WriteString("\nHotMouse runtime adaptation for imported skills: the enclosing task, response schema, authorization and workspace rules take precedence. PI means the current agent; driver means the task coordinator. Cairn fact and memory/tree/deadends references mean evidence saved under the current writable workspace and returned using this task's existing result/finding contract; reuse the supplied graph and artifacts. Do not invoke an absent PI or Cairn service. Resolve pentest_toolkit paths against HOTMOUSE_PENTEST_TOOLKIT_DIR. Tools named in skills are examples: reuse the capability report or check only the required command/import before using it. The toolkit and skills are reference files; put outputs, authentication ledgers and modifications in the current workspace.\n")
	switch task {
	case "decide":
		out.WriteString("Dispatcher: use the skill routing table to plan concrete steps from saved evidence. Read references only; leave target requests, script execution and new output creation to execution Workers.\n")
	case "execute":
		out.WriteString("Worker: complete only the assigned bounded Step, return confirmed flags through Finding/result and preserve their source. The HotMouse coordinator owns submission and project scheduling.\n")
	case "hosted":
		out.WriteString("Hosted agent: follow the platform briefing's result and flag submission contract. The skill's driver-only submission and HotMouse Step conventions do not replace that contract.\n")
	}
	return out.String()
}
