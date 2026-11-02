package httpapi

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/kataras/iris/v12"
	"hotmouse/backend/internal/core"
)

// Campaign is a read-only project synthesis. Keep the scoring rules here
// separate from the HTTP handler so they can be exercised without changing
// the graph protocol itself.
type campaignVulnerability struct {
	id, factID, title, description, severity, status, discoveredAt string
}

var (
	positiveFactPatterns = []struct {
		pattern *regexp.Regexp
		weight  int
	}{
		{regexp.MustCompile(`(?i)\buid=0\b`), 120},
		{regexp.MustCompile(`(?i)\buid=\d+\([^)]+\)`), 110},
		{regexp.MustCompile(`(?i)\bwww-data\b`), 90},
		{regexp.MustCompile(`(?i)\bwhoami\b`), 80},
		{regexp.MustCompile(`(?i)远程(?:代码|命令)?执行|命令执行|\brce\b`), 75},
		{regexp.MustCompile(`(?i)未授权\s*(?:读取|访问)|越权读取|跨作业读取`), 70},
		{regexp.MustCompile(`(?i)路径穿越|directory traversal|path traversal`), 65},
		{regexp.MustCompile(`(?i)返回\s*200|Content-Type|正文为|解包确认`), 35},
		{regexp.MustCompile(`(?i)已确认|真实存在|证明攻击者可|证明可|直接返回|暴露|泄露`), 30},
	}
	negativeFactPatterns = []struct {
		pattern *regexp.Regexp
		weight  int
	}{
		{regexp.MustCompile(`(?i)未观察到(?:任何)?真实(?:执行|利用|命令执行)证据`), 90},
		{regexp.MustCompile(`(?i)未发现.{0,80}(?:uid=|www-data|whoami|id\s*输出|副作用)`), 80},
		{regexp.MustCompile(`(?i)尚不能(?:据此)?确认|不能(?:据此)?确认`), 70},
		{regexp.MustCompile(`(?i)无命令执行|不可利用|未触发|未命中|无可利用`), 60},
		{regexp.MustCompile(`(?i)失败|超时|未获得|未收到`), 30},
	}
)

func (h *Handler) campaign(ctx context.Context, projectID string) (iris.Map, error) {
	detail, err := h.DB.GetProject(ctx, projectID)
	if err != nil {
		return nil, err
	}

	rows, err := h.DB.SQL.QueryContext(ctx, `SELECT id,fact_id,title,description,severity,status,discovered_at FROM vulnerabilities WHERE project_id=? ORDER BY CASE severity WHEN 'critical' THEN 0 WHEN 'high' THEN 1 WHEN 'medium' THEN 2 WHEN 'low' THEN 3 ELSE 4 END,discovered_at DESC,id DESC`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	vulnerabilities := []campaignVulnerability{}
	for rows.Next() {
		var item campaignVulnerability
		if err := rows.Scan(&item.id, &item.factID, &item.title, &item.description, &item.severity, &item.status, &item.discoveredAt); err != nil {
			return nil, err
		}
		vulnerabilities = append(vulnerabilities, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	openDescriptions := []string{}
	for _, step := range detail.Steps {
		if step.To == nil && !step.Abandoned && strings.TrimSpace(step.Description) != "" {
			openDescriptions = append(openDescriptions, strings.TrimSpace(step.Description))
		}
	}
	openSteps := len(openDescriptions)
	if len(openDescriptions) > 5 {
		openDescriptions = openDescriptions[:5]
	}

	goalStatus := campaignGoalStatus(detail.Project.Status, detail.Goals, detail.Facts, openSteps, vulnerabilities)
	topFindings := campaignTopFindings(detail.Facts, detail.Hints, vulnerabilities)
	blockers := campaignBlockers(detail.Facts)
	lead := campaignLead(detail.Project.Title, goalStatus, vulnerabilities, topFindings, openDescriptions)
	highValue := 0
	for _, vulnerability := range vulnerabilities {
		if vulnerability.status == "confirmed" && (vulnerability.severity == "critical" || vulnerability.severity == "high") {
			highValue++
		}
	}

	origin := ""
	for _, fact := range detail.Facts {
		if fact.ID == "origin" {
			origin = fact.Description
			break
		}
	}
	goals := append([]core.Goal(nil), detail.Goals...)
	sort.SliceStable(goals, func(i, j int) bool {
		if goals[i].Priority != goals[j].Priority {
			return goals[i].Priority > goals[j].Priority
		}
		return goals[i].CreatedAt < goals[j].CreatedAt
	})
	goalText := []string{}
	for _, goal := range goals {
		if goal.ParentGoalID == nil && strings.TrimSpace(goal.Description) != "" {
			goalText = append(goalText, strings.TrimSpace(goal.Description))
		}
	}

	counts := iris.Map{
		"facts": len(detail.Facts), "hints": len(detail.Hints), "goals": len(detail.Goals),
		"steps": len(detail.Steps), "open_steps": openSteps, "intents": len(detail.Steps),
		"open_intents": openSteps, "vulnerabilities": len(vulnerabilities),
		"high_value_vulnerabilities": highValue,
	}
	return iris.Map{
		"project_id":     projectID,
		"project_name":   detail.Project.Title,
		"project_status": detail.Project.Status,
		"goal_status":    goalStatus,
		"origin":         origin,
		"goal":           strings.Join(goalText, "\n"),
		"lead":           lead,
		"summary":        campaignSummary(detail.Project.Title, detail.Project.Status, goalStatus, counts, lead, blockers),
		"counts":         counts,
		"top_findings":   topFindings,
		"open_steps":     openDescriptions,
		"open_intents":   openDescriptions,
		"blockers":       blockers,
		"next_steps":     campaignNextSteps(goalStatus, counts, openDescriptions, detail.Hints, vulnerabilities),
	}, nil
}

func campaignGoalStatus(projectStatus string, goals []core.Goal, facts []core.Fact, openSteps int, vulnerabilities []campaignVulnerability) string {
	if len(goals) > 0 {
		allCompleted := true
		for _, goal := range goals {
			if goal.Status != "completed" {
				allCompleted = false
				break
			}
		}
		if allCompleted || projectStatus == "completed" {
			return "achieved"
		}
		if openSteps > 0 || len(facts) > 1 || len(vulnerabilities) > 0 {
			return "in_progress"
		}
		return "blocked"
	}

	text := ""
	for _, fact := range facts {
		text += "\n" + fact.Description
	}
	positive := regexp.MustCompile(`(?i)\buid=0\b|uid=\d+\((?:root|www-data|daemon|nobody)\)|\bwhoami\b[^.\n]{0,20}\b(?:root|www-data|daemon|nobody)\b|connected\s+as\s+(?:root|www-data)|interactive\s+shell\s+ready|\bifconfig\b[^.\n]{0,80}\b(?:inet|ether|HWaddr|flags)\b|(?:目标已达成|goal\s*已达成|已拿到\s*(?:webshell|shell|权限))|\b(?:webshell|shell)\s*(?:已部署|部署成功|可访问|ready)`)
	if positive.MatchString(text) {
		return "achieved"
	}
	for _, vulnerability := range vulnerabilities {
		if vulnerability.status == "confirmed" && (vulnerability.severity == "critical" || vulnerability.severity == "high") {
			return "in_progress"
		}
	}
	if campaignFactScore(text) >= 70 {
		return "in_progress"
	}
	return "blocked"
}

func campaignFactScore(text string) int {
	score := 0
	for _, item := range positiveFactPatterns {
		if item.pattern.MatchString(text) {
			score += item.weight
		}
	}
	for _, item := range negativeFactPatterns {
		if item.pattern.MatchString(text) {
			score -= item.weight
		}
	}
	return score
}

func campaignFirstLine(text string, limit int) string {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		runes := []rune(line)
		if len(runes) <= limit {
			return line
		}
		return string(runes[:limit-3]) + "..."
	}
	return ""
}

func campaignFactRank(id string) int {
	for i := 0; i < len(id); i++ {
		if id[i] < '0' || id[i] > '9' {
			continue
		}
		n := 0
		for ; i < len(id) && id[i] >= '0' && id[i] <= '9'; i++ {
			n = n*10 + int(id[i]-'0')
		}
		return n
	}
	return -1
}

func campaignTopFindings(facts []core.Fact, hints []core.Hint, vulnerabilities []campaignVulnerability) []iris.Map {
	findings := []iris.Map{}
	covered := map[string]bool{}
	for _, vulnerability := range vulnerabilities {
		covered[vulnerability.factID] = true
	}
	for i, vulnerability := range vulnerabilities {
		if i == 3 {
			break
		}
		confidence := "supported"
		if vulnerability.status == "confirmed" {
			confidence = "confirmed"
		}
		severity := any(nil)
		if vulnerability.severity == "critical" || vulnerability.severity == "high" || vulnerability.severity == "medium" || vulnerability.severity == "low" {
			severity = vulnerability.severity
		}
		findings = append(findings, iris.Map{"source_type": "vulnerability", "source_id": vulnerability.id, "title": vulnerability.title, "summary": campaignFirstLine(vulnerability.description, 180), "severity": severity, "confidence": confidence})
	}
	type scoredFact struct {
		fact  core.Fact
		score int
		rank  int
	}
	scored := []scoredFact{}
	for _, fact := range facts {
		if fact.ID == "origin" || fact.ID == "goal" || covered[fact.ID] {
			continue
		}
		score := campaignFactScore(fact.Description)
		if score >= 25 {
			scored = append(scored, scoredFact{fact: fact, score: score, rank: campaignFactRank(fact.ID)})
		}
	}
	sort.SliceStable(scored, func(i, j int) bool {
		if scored[i].score != scored[j].score {
			return scored[i].score > scored[j].score
		}
		return scored[i].rank > scored[j].rank
	})
	for _, item := range scored {
		if len(findings) >= 5 {
			break
		}
		findings = append(findings, iris.Map{"source_type": "fact", "source_id": item.fact.ID, "title": campaignFirstLine(item.fact.Description, 84), "summary": campaignFirstLine(item.fact.Description, 180), "confidence": campaignFactConfidence(item.score)})
	}
	if len(findings) == 0 && len(hints) > 0 {
		hint := hints[len(hints)-1]
		findings = append(findings, iris.Map{"source_type": "hint", "source_id": hint.ID, "title": "最近 Hint", "summary": campaignFirstLine(hint.Content, 180), "confidence": "tentative"})
	}
	return findings
}

func campaignFactConfidence(score int) string {
	if score >= 90 {
		return "confirmed"
	}
	if score >= 40 {
		return "supported"
	}
	return "tentative"
}

func campaignBlockers(facts []core.Fact) []string {
	type blocker struct {
		rank int
		text string
	}
	items := []blocker{}
	for _, fact := range facts {
		if fact.ID == "origin" || fact.ID == "goal" {
			continue
		}
		matched := false
		for _, pattern := range negativeFactPatterns {
			if pattern.pattern.MatchString(fact.Description) {
				matched = true
				break
			}
		}
		if matched {
			items = append(items, blocker{rank: campaignFactRank(fact.ID), text: campaignFirstLine(fact.Description, 180)})
		}
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].rank > items[j].rank })
	result := []string{}
	seen := map[string]bool{}
	for _, item := range items {
		if item.text == "" || seen[item.text] {
			continue
		}
		seen[item.text] = true
		result = append(result, item.text)
		if len(result) == 3 {
			break
		}
	}
	return result
}

func campaignLead(projectName, goalStatus string, vulnerabilities []campaignVulnerability, topFindings []iris.Map, openSteps []string) string {
	if goalStatus == "achieved" {
		return projectName + " 已具备足以证明目标达成的结果。"
	}
	if len(vulnerabilities) > 0 {
		severity := map[string]string{"critical": "严重", "high": "高危", "medium": "中危", "low": "低危"}[vulnerabilities[0].severity]
		if severity == "" {
			severity = vulnerabilities[0].severity
		}
		return fmt.Sprintf("当前主线已收敛到 %s 发现：%s", severity, vulnerabilities[0].title)
	}
	if len(topFindings) > 0 {
		return fmt.Sprintf("当前最强信号来自 %v：%v", topFindings[0]["source_id"], topFindings[0]["title"])
	}
	if len(openSteps) > 0 {
		return "当前仍以 " + openSteps[0] + " 作为主要推进步骤。"
	}
	return fmt.Sprintf("%s 当前缺少足够强的项目级信号。", projectName)
}

func campaignSummary(projectName, projectStatus, goalStatus string, counts iris.Map, lead string, blockers []string) string {
	parts := []string{fmt.Sprintf("项目 %s 当前状态为 %s，目标判定为 %s。", projectName, projectStatus, goalStatus), lead}
	parts = append(parts, fmt.Sprintf("已累计 %d 条事实、%d 个目标、%d 个开放步骤、%d 条发现记录，其中高价值发现 %d 条。", counts["facts"], counts["goals"], counts["open_steps"], counts["vulnerabilities"], counts["high_value_vulnerabilities"]))
	if projectStatus == "completed" && goalStatus != "achieved" {
		parts = append(parts, "项目状态虽然已标记为 completed，但当前事实中仍缺少足以直接证明目标达成的正向成功信号。")
	}
	if len(blockers) > 0 {
		parts = append(parts, "最近主要阻塞点："+blockers[0])
	}
	return strings.Join(parts, " ")
}

func campaignNextSteps(goalStatus string, counts iris.Map, openSteps []string, hints []core.Hint, vulnerabilities []campaignVulnerability) []string {
	if goalStatus == "achieved" {
		return []string{"固定当前项目的关键证据和最小复现实验，避免后续事实改写覆盖成功结论。", "将项目级结论与单条事实分开维护，保证最终结论和过程证据都可追溯。"}
	}
	steps := []string{}
	if len(vulnerabilities) > 0 {
		steps = append(steps, "把已确认高价值发现与未确认提权路径分开维护，避免同一事实同时承载正反两类结论。")
	} else {
		steps = append(steps, "优先把已有强信号拆成独立项目结论，避免零散事实长期停留在图里而无法形成主线判断。")
	}
	if len(openSteps) > 0 {
		steps = append(steps, "优先收敛现有开放步骤，减少在同一候选方向上重复记录相似阴性结果。")
	} else {
		steps = append(steps, "当前没有开放步骤，建议由 Decide 围绕目标和最新事实生成少量互不重叠的后续步骤。")
	}
	if len(hints) > 0 {
		steps = append(steps, "对关键 hint 保留原文与原始请求细节，避免语义改写导致后续复现实验漂移。")
	}
	if counts["high_value_vulnerabilities"] == 0 {
		steps = append(steps, "在项目级视图中优先沉淀可复现的边界、证据和失败条件，而不是过早把目标缩成单一成功标准。")
	}
	if len(steps) > 4 {
		steps = steps[:4]
	}
	return steps
}
