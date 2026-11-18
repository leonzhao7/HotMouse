package tpctf

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type Quota struct {
	Pending    int `json:"pending_count"`
	PendingCap int `json:"pending_cap"`
	Window     *struct {
		StartsAt  string `json:"starts_at"`
		EndsAt    string `json:"ends_at"`
		Remaining int    `json:"remaining"`
	} `json:"upload_window"`
	Rate *struct {
		Remaining int `json:"remaining"`
	} `json:"upload_rate"`
	SmokeCharge    string `json:"smoke_charged_to"`
	SmokeRemaining int    `json:"smoke_remaining"`
}
type Authored struct {
	ID         string `json:"id"`
	Slug       string `json:"slug"`
	Review     string `json:"review_status"`
	ReviewNote string `json:"review_note"`
	Smoke      string `json:"smoke_verdict"`
	InFlight   string `json:"smoke_in_flight"`
	Problem    string `json:"smoke_problem"`
	Refusals   any    `json:"tree_refusals"`
	Revision   any    `json:"revision"`
}

func (r *Runner) author(ctx context.Context) error {
	c := r.AuthorClient
	cid := r.Config.CompetitionID
	quota, err := c.Quota(ctx, cid)
	if err != nil {
		return err
	}
	if quota.Window == nil || quota.Rate != nil && quota.Rate.Remaining <= 0 || quota.SmokeCharge != "off" && quota.SmokeRemaining <= 0 {
		return nil
	}
	topics, err := c.Topics(ctx, cid)
	if err != nil {
		return err
	}
	var theme struct {
		Topics []struct {
			ID       string `json:"id"`
			Released bool   `json:"released"`
		} `json:"topics"`
	}
	if err = json.Unmarshal(topics, &theme); err != nil {
		return err
	}
	topic := ""
	for _, t := range theme.Topics {
		if t.Released {
			topic = t.ID
		}
	}
	if topic == "" {
		return nil
	}
	windowKey := quota.Window.StartsAt + ":" + topic
	s := r.State.Authors[windowKey]
	if s == nil {
		s = &authorState{Slug: "hotmouse-" + digest([]string{cid, windowKey})[:16]}
		r.State.Authors[windowKey] = s
	}
	if time.Now().Before(s.RetryAt) {
		return nil
	}
	authored, err := c.Authored(ctx, cid)
	if err != nil {
		return err
	}
	var existing *Authored
	for i := range authored {
		if authored[i].Slug == s.Slug {
			existing = &authored[i]
			s.ID = existing.ID
			s.Pending = false
			break
		}
	}
	if err = r.State.save(r.Config.WorkDir); err != nil {
		return err
	}
	if s.Pending {
		r.Log.Info("authoring.upload.unconfirmed", "slug", s.Slug)
		return nil
	}
	var audit json.RawMessage
	if existing != nil {
		audit, err = c.Audit(ctx, cid, existing.ID)
		if err != nil {
			return err
		}
		var checks struct {
			Errors int `json:"errors"`
		}
		if err = json.Unmarshal(audit, &checks); err != nil {
			return err
		}
		r.Log.Info("authoring.status", "id", existing.ID, "review", existing.Review, "smoke", existing.Smoke, "in_flight", existing.InFlight, "audit_errors", checks.Errors)
		if existing.InFlight != "" || existing.Revision != nil {
			return nil
		}
		if checks.Errors == 0 && (existing.Smoke == "solved" || existing.Smoke == "" || existing.Smoke == "stale") && existing.Review != "rejected" {
			return nil
		}
	}
	if existing == nil && (quota.Window.Remaining <= 0 || quota.PendingCap > 0 && quota.Pending >= quota.PendingCap) {
		return nil
	}
	if s.Uploads >= 3 {
		return nil
	}
	workDir := filepath.Join(r.Config.WorkDir, "authoring", s.Slug)
	if err = os.MkdirAll(workDir, 0700); err != nil {
		return err
	}
	templateDir := filepath.Join(workDir, "template")
	if _, err = os.Stat(templateDir); os.IsNotExist(err) {
		template, e := c.Template(ctx, cid, r.Config.AuthorCategory, r.Config.AuthorStack)
		if e != nil {
			return e
		}
		if e = unpackTemplate(templateDir, template); e != nil {
			return e
		}
	}
	task := map[string]any{"topic_id": topic, "topics": json.RawMessage(topics), "quota": quota, "previous_submission": existing, "audit": audit, "category": r.Config.AuthorCategory, "stack": r.Config.AuthorStack, "slug": s.Slug}
	if err = saveJSON(filepath.Join(workDir, "authoring.json"), task); err != nil {
		return err
	}
	agent := r.Config.Agent
	agent.WorkDir = workDir
	agent.Logger = r.Log.With("authoring", s.Slug)
	agent.Prompt = "你正在参加TPCTF 2026智能体赛道的自动出题阶段。读取authoring.json的当前主题、配额、审核及验题反馈，以及template/下官方模板及编写说明。围绕当前topic_id设计一题，严格遵守模板中元数据、docker-compose、资源及端口规范，只接受Web/Pwn。保持平台动态Flag注入机制，禁止固定Flag或参考解直接读取/answers等平台答案目录。solution/exploit.py必须从公开服务利用你设计的漏洞，按模板协议输出新实例的动态Flag，并包含solution/WRITEUP.md。若已有审核反馈，修复原题并保留原slug。将完整题目放到当前工作目录下单一challenge/目录，可以复制模板后修改。不要上传或调用平台接口，主进程会打包、上传、检查并在后续自动修复。不要在题包或日志中包含任何API密钥。完成后简要描述题目和验证情况。"
	authorCtx, cancel := context.WithTimeout(ctx, r.Config.ChallengeTimeout)
	defer cancel()
	s.RetryAt = time.Now().Add(r.Config.RetryInterval)
	if err = r.State.save(r.Config.WorkDir); err != nil {
		return err
	}
	if _, err = r.RunAgent(authorCtx, agent); err != nil {
		return err
	}
	archive := filepath.Join(workDir, "challenge.tar.gz")
	if err = packChallenge(filepath.Join(workDir, "challenge"), archive, r.Config.Token, r.Config.AuthorToken, r.Config.Agent.Environment["LLM_API_KEY"]); err != nil {
		return err
	}
	data, err := os.ReadFile(archive)
	if err != nil {
		return err
	}
	hash := digest(data)
	if hash == s.ArchiveHash {
		return fmt.Errorf("unchanged authoring archive; skipping duplicate upload")
	}
	fields := map[string]string{"slug": s.Slug, "license": r.Config.License, "provenance": "original", "ai_assisted": "yes", "credit": "anonymous"}
	if r.Config.Terms != "" {
		fields["accepted_terms_version"] = r.Config.Terms
	}
	revision := ""
	if existing != nil && existing.Review == "approved" {
		revision = existing.ID
		fields = map[string]string{"note": "Fix static audit and reference exploit verification"}
	}
	s.Uploads++
	s.Pending = true
	s.ArchiveHash = hash
	if err = r.State.save(r.Config.WorkDir); err != nil {
		return err
	}
	response, err := c.Upload(ctx, cid, revision, archive, fields)
	if err != nil {
		if errorCode(err) != "" {
			s.Pending = false
		}
		if e := r.State.save(r.Config.WorkDir); e != nil {
			return e
		}
		return err
	}
	s.Pending = false
	if revision == "" {
		s.ID = response.ID
	}
	if err = r.State.save(r.Config.WorkDir); err != nil {
		return err
	}
	r.Log.Info("authoring.uploaded", "slug", s.Slug, "id", s.ID, "revision", revision != "")
	return nil
}
func unpackTemplate(destination string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(destination), 0700); err != nil {
		return err
	}
	temp, err := os.MkdirTemp(filepath.Dir(destination), ".template-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(temp)
	source, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return err
	}
	defer source.Close()
	reader := tar.NewReader(source)
	var total int64
	top := ""
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		name := filepath.Clean(header.Name)
		if filepath.IsAbs(name) || name == ".." || strings.HasPrefix(name, ".."+string(filepath.Separator)) {
			return fmt.Errorf("unsafe template path")
		}
		first := strings.Split(name, string(filepath.Separator))[0]
		if top == "" {
			top = first
		}
		if first != top {
			return fmt.Errorf("template must have a single top-level directory")
		}
		path := filepath.Join(temp, name)
		switch header.Typeflag {
		case tar.TypeDir:
			if err = os.MkdirAll(path, 0700); err != nil {
				return err
			}
		case tar.TypeReg, tar.TypeRegA:
			total += header.Size
			if total > 128<<20 || header.Size < 0 {
				return fmt.Errorf("template expands beyond 128 MiB")
			}
			if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				return err
			}
			file, e := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, os.FileMode(header.Mode)&0700)
			if e != nil {
				return e
			}
			_, e = io.Copy(file, reader)
			closeErr := file.Close()
			if e != nil {
				return e
			}
			if closeErr != nil {
				return closeErr
			}
		default:
			return fmt.Errorf("template contains unsupported links or special files")
		}
	}
	if top == "" {
		return fmt.Errorf("empty template")
	}
	return os.Rename(temp, destination)
}
func packChallenge(root, destination string, secrets ...string) error {
	for _, required := range []string{"solution/exploit.py", "solution/WRITEUP.md", "metadata.yaml", "docker-compose.yml"} {
		info, err := os.Lstat(filepath.Join(root, required))
		if err != nil || !info.Mode().IsRegular() {
			return fmt.Errorf("challenge missing regular file %s", required)
		}
	}
	file, err := os.Create(destination)
	if err != nil {
		return err
	}
	defer file.Close()
	zipped := gzip.NewWriter(file)
	writer := tar.NewWriter(zipped)
	var total int64
	files := 0
	err = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() && !info.Mode().IsRegular() {
			return fmt.Errorf("challenge contains links or special files")
		}
		files++
		total += info.Size()
		if files > 10000 || total > 128<<20 {
			return fmt.Errorf("challenge archive is too large")
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if strings.HasPrefix(info.Name(), ".env") || info.Name() == "state.json" {
			return fmt.Errorf("unexpected private file in challenge archive: %s", rel)
		}
		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		header.Name = filepath.ToSlash(filepath.Join("challenge", rel))
		header.ModTime = time.Time{}
		header.AccessTime = time.Time{}
		header.ChangeTime = time.Time{}
		if err = writer.WriteHeader(header); err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, secret := range secrets {
			if secret != "" && bytes.Contains(data, []byte(secret)) {
				return fmt.Errorf("credential found in challenge file %s", rel)
			}
		}
		_, err = writer.Write(data)
		return err
	})
	if closeErr := writer.Close(); err == nil {
		err = closeErr
	}
	if closeErr := zipped.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return file.Sync()
}
