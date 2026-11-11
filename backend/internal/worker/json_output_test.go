package worker

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"hotmouse/backend/internal/core"
)

func TestDecodeObjectAcceptsJSONWithExplanation(t *testing.T) {
	const object = `{"description":"confirmed {value} [item] ` + "`code`" + `","result":{"type":"progress","evidence":{"items":["中文", "quote: \"", "brace: }"]}}}`
	for name, raw := range map[string]string{
		"raw":                    object,
		"prose_before_and_after": "Result:\n" + object + "\n说明：[web] 附件 `source.py`，flag 格式 HTB{...}。",
		"brackets_before_json":   "说明：[web] 离线附件\n" + object,
		"inline_json_examples":   object + "\n说明：字段 `{}` 和数组 `[]` 是示例。",
		"fenced":                 "```json\n" + object + "\n```\n说明：`source.py` + `output.txt` 离线分析，格式 HTB{...}。",
		"unlabelled_fence":       "说明：[web] 分析结果\n```\n" + object + "\n```\n附件 `output.txt`。",
		"explanation_in_fence":   "```json\n" + object + "\n说明：[web]，格式 HTB{...}。\n```",
		"code_after_json":        object + "\n复核命令：\n```sh\ncat source.py\n```",
		"plain_fence_after_json": "```json\n" + object + "\n```\n复核命令：\n```\ncat source.py\n```",
	} {
		t.Run(name, func(t *testing.T) {
			var got, want map[string]json.RawMessage
			if err := json.Unmarshal([]byte(object), &want); err != nil {
				t.Fatal(err)
			}
			if err := decodeObject([]byte(raw), &got); err != nil {
				t.Fatal(err)
			}
			if len(got) != len(want) || string(got["description"]) != string(want["description"]) || string(got["result"]) != string(want["result"]) {
				t.Fatalf("explanation changed the JSON: %s", got)
			}
		})
	}
}

func TestDecodeObjectRejectsAdditionalJSONResponses(t *testing.T) {
	const object = `{"description":"confirmed"}`
	for _, raw := range []string{
		object + ` {"description":"another"}`,
		object + "\n说明：\n{\"description\":\"another\"}",
		object + "\n[{}]",
		"```json\n" + object + "\n```\n" + object,
		"```json\n" + object + "\n```\n```json\n" + object + "\n```",
	} {
		var got map[string]json.RawMessage
		if err := decodeObject([]byte(raw), &got); err == nil {
			t.Fatalf("multiple JSON responses accepted: %s", raw)
		}
	}
}

func TestDecodeObjectRejectsDamagedOrNonObjectJSONWithExplanation(t *testing.T) {
	for _, raw := range []string{
		`{"description":"partial"`,
		`{"description":"first","description":"second"}`,
		`{"description":"confirmed","result":{"type":"progress","type":"negative"}}`,
		"[{\"description\":\"nested\"}]\n说明：附件 `source.py`。",
		"Result: [{\"description\":\"nested\"}]\n说明：离线任务。",
		"Result:\n[\n{\"description\":\"nested\"}\n]\n说明：离线任务。",
		"```json\n{\"description\":\"confirmed\"}",
	} {
		var got map[string]json.RawMessage
		if err := decodeObject([]byte(raw), &got); err == nil {
			t.Fatalf("invalid response accepted: %s", raw)
		}
	}
}

func TestTargetProbeAnalysisAcceptsProject068Responses(t *testing.T) {
	const object = `{"accepted":true,"data":{"required":false,"reason":"local/offline challenge or no remote CTF service","targets":[]}}`
	for _, raw := range []string{
		object + "\n\nReasoning summary:\n- Challenge g-07 is an offline crypto task: the description only involves downloading an attachment containing `source.py` and `output.txt` and analyzing the encryption algorithm locally. No remote challenge service is required to solve it.",
		"```json\n" + object + "\n```\n\nReasoning: challenge g-07 is a pure offline crypto task — the description states the deliverables are downloadable attachments (`source.py` and `output.txt`), and the flag is recovered by analyzing the encryption algorithm locally with no live challenge service interaction.",
		object + "\n\n说明：题目 g-07 是纯附件类密码题（source.py + output.txt 离线分析，flag 格式 HTB{...}），实例状态为 stopped 且 addresses 为空，解题不需要在线靶场服务。",
	} {
		analysis, err := parseTargetProbeAnalysis([]byte(raw))
		if err != nil || analysis.Required || len(analysis.Targets) != 0 || analysis.Reason != "local/offline challenge or no remote CTF service" {
			t.Fatalf("valid offline analysis rejected: %+v err=%v", analysis, err)
		}
	}
}

func TestDecideAcceptsJSONWithExplanation(t *testing.T) {
	db, err := core.Open(filepath.Join(t.TempDir(), "decide.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.SQL.Close()
	project, err := db.CreateProject(t.Context(), core.CreateProject{Title: "offline", Origin: "attachment", Goal: "capture flag"})
	if err != nil {
		t.Fatal(err)
	}
	raw := "```json\n" + `{"accepted":true,"data":{"steps":[{"from":["origin"],"description":"inspect attachment","goal_id":"g001"}]}}` + "\n```\n说明：离线分析 `source.py`，检查 HTB{...} 格式。"
	if err := applyDecide(db, t.Context(), project.Project.ID, project, []byte(raw), "dispatcher"); err != nil {
		t.Fatal(err)
	}
	after, err := db.GetProject(t.Context(), project.Project.ID)
	if err != nil || len(after.Steps) != 1 || after.Steps[0].Description != "inspect attachment" {
		t.Fatalf("valid decision was not committed: %+v err=%v", after.Steps, err)
	}
}
