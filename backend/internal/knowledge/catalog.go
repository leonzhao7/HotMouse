// Package knowledge provides the offline, versioned CTF reference bundled with
// HotMouse. It is independent of project facts and the learned experience store.
package knowledge

import (
	"crypto/sha256"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"math"
	"path"
	"sort"
	"strings"
	"sync"
	"unicode"

	"gopkg.in/yaml.v3"
)

//go:embed entries/*.json skills/*/SKILL.md skills/*/references/*.md provenance.json NOTICE.md LICENSE.sapper-v2 DISCLAIMER.sapper-v2.md
var resources embed.FS

type Entry struct {
	ID          string   `json:"id" yaml:"id"`
	Title       string   `json:"title" yaml:"title"`
	Description string   `json:"description" yaml:"description"`
	Detail      string   `json:"detail" yaml:"detail"`
	Commands    []string `json:"commands" yaml:"commands"`
	Tags        []string `json:"tags" yaml:"tags"`
	Source      string   `json:"source" yaml:"source"`
	CreatedAt   string   `json:"created_at" yaml:"created_at"`
}

type Summary struct {
	ID          string   `json:"id" yaml:"id"`
	Title       string   `json:"title" yaml:"title"`
	Description string   `json:"description" yaml:"description"`
	Tags        []string `json:"tags" yaml:"tags"`
	Source      string   `json:"source" yaml:"source"`
	Score       float64  `json:"score,omitempty" yaml:"score,omitempty"`
}

type Skill struct {
	Name        string `json:"name" yaml:"name"`
	Description string `json:"description" yaml:"description"`
	Content     string `json:"content" yaml:"-"`
}

type Info struct {
	Version    string         `json:"version"`
	Upstream   string         `json:"upstream"`
	Commit     string         `json:"commit"`
	License    string         `json:"license"`
	EntryCount int            `json:"entry_count"`
	SkillCount int            `json:"skill_count"`
	Tags       map[string]int `json:"tags"`
}

type document struct {
	entry   Entry
	weights map[string]float64
}

type Catalog struct {
	documents []document
	skills    []Skill
	idf       map[string]float64
	info      Info
}

var cached struct {
	sync.Once
	catalog *Catalog
	err     error
}

// Load caches immutable embedded resources. Returned records use copied slices
// so callers cannot alter later searches or another worker's reference.
func Load() (*Catalog, error) {
	cached.Do(func() { cached.catalog, cached.err = load() })
	return cached.catalog, cached.err
}

func load() (*Catalog, error) {
	c := &Catalog{info: Info{Tags: map[string]int{}}, idf: map[string]float64{}}
	manifest, err := resources.ReadFile("provenance.json")
	if err != nil {
		return nil, err
	}
	var provenance struct {
		Upstream   string `json:"upstream"`
		Commit     string `json:"commit"`
		License    string `json:"license"`
		EntryCount int    `json:"entry_count"`
		Entries    []struct {
			ID   string `json:"id"`
			Hash string `json:"bundled_sha256"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(manifest, &provenance); err != nil {
		return nil, fmt.Errorf("knowledge provenance: %w", err)
	}
	hashes := map[string]string{}
	for _, e := range provenance.Entries {
		if _, exists := hashes[e.ID]; exists {
			return nil, fmt.Errorf("duplicate knowledge provenance ID %s", e.ID)
		}
		hashes[e.ID] = e.Hash
	}
	files, err := fs.Glob(resources, "entries/*.json")
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	frequency := map[string]int{}
	for _, name := range files {
		data, err := resources.ReadFile(name)
		if err != nil {
			return nil, err
		}
		var entry Entry
		if err := json.Unmarshal(data, &entry); err != nil {
			return nil, fmt.Errorf("knowledge %s: %w", name, err)
		}
		if entry.ID == "" || entry.Title == "" || entry.Detail == "" || seen[entry.ID] || path.Base(name) != entry.ID+".json" {
			return nil, fmt.Errorf("invalid knowledge entry %s", name)
		}
		if fmt.Sprintf("%x", sha256.Sum256(data)) != hashes[entry.ID] {
			return nil, fmt.Errorf("knowledge provenance hash mismatch for %s", entry.ID)
		}
		seen[entry.ID] = true
		weights := map[string]float64{}
		for _, field := range []struct {
			value  string
			weight float64
		}{{entry.Title, 5}, {strings.Join(entry.Tags, " "), 8}, {entry.Description, 3}, {entry.Detail, 1}} {
			for token := range tokenize(field.value) {
				weights[token] += field.weight
			}
		}
		for token := range weights {
			frequency[token]++
		}
		for _, tag := range entry.Tags {
			c.info.Tags[tag]++
		}
		c.documents = append(c.documents, document{entry: entry, weights: weights})
	}
	if len(files) != provenance.EntryCount || len(hashes) != len(files) {
		return nil, fmt.Errorf("knowledge entry count does not match provenance")
	}
	for token, count := range frequency {
		c.idf[token] = math.Log(1 + float64(len(files))/float64(count))
	}
	skillFiles, err := fs.Glob(resources, "skills/*/SKILL.md")
	if err != nil {
		return nil, err
	}
	for _, name := range skillFiles {
		data, err := resources.ReadFile(name)
		if err != nil {
			return nil, err
		}
		parts := strings.SplitN(string(data), "---", 3)
		var skill Skill
		if len(parts) != 3 || strings.TrimSpace(parts[0]) != "" {
			return nil, fmt.Errorf("invalid skill frontmatter %s", name)
		}
		if err := yaml.Unmarshal([]byte(parts[1]), &skill); err != nil {
			return nil, fmt.Errorf("skill %s: %w", name, err)
		}
		if skill.Name != path.Base(path.Dir(name)) || skill.Description == "" {
			return nil, fmt.Errorf("invalid skill metadata %s", name)
		}
		skill.Content = string(data)
		c.skills = append(c.skills, skill)
	}
	digest := sha256.New()
	if err := fs.WalkDir(resources, ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		data, err := resources.ReadFile(name)
		if err == nil {
			fmt.Fprintf(digest, "%s\x00%d\x00", name, len(data))
			_, _ = digest.Write(data)
		}
		return err
	}); err != nil {
		return nil, err
	}
	c.info.Version = fmt.Sprintf("%x", digest.Sum(nil))[:16]
	c.info.Upstream, c.info.Commit, c.info.License = provenance.Upstream, provenance.Commit, provenance.License
	c.info.EntryCount, c.info.SkillCount = len(c.documents), len(c.skills)
	return c, nil
}

func (c *Catalog) Info() Info {
	info := c.info
	info.Tags = map[string]int{}
	for tag, count := range c.info.Tags {
		info.Tags[tag] = count
	}
	return info
}

func summary(e Entry) Summary {
	return Summary{ID: e.ID, Title: e.Title, Description: e.Description, Tags: append([]string{}, e.Tags...), Source: e.Source}
}

func (c *Catalog) List(tag string) []Summary {
	out := []Summary{}
	for _, d := range c.documents {
		if hasTag(d.entry, tag) {
			out = append(out, summary(d.entry))
		}
	}
	return out
}

func hasTag(e Entry, tag string) bool {
	tag = strings.ToLower(strings.TrimSpace(tag))
	if tag == "" {
		return true
	}
	for _, t := range e.Tags {
		if t == tag {
			return true
		}
	}
	return false
}

func (c *Catalog) Get(id string) (Entry, error) {
	for _, d := range c.documents {
		if d.entry.ID == id {
			e := d.entry
			e.Commands, e.Tags = append([]string{}, e.Commands...), append([]string{}, e.Tags...)
			return e, nil
		}
	}
	return Entry{}, fmt.Errorf("knowledge entry %q not found", id)
}

func (c *Catalog) Skills() []Skill {
	return append([]Skill{}, c.skills...)
}

func (c *Catalog) Skill(name string) (Skill, error) {
	for _, skill := range c.skills {
		if skill.Name == name {
			return skill, nil
		}
	}
	return Skill{}, fmt.Errorf("knowledge skill %q not found", name)
}

func (c *Catalog) Search(query, tag string, limit int) []Summary {
	return c.search(queryTokens(query), tag, limit)
}

// Recall ignores generic goal words so a goal like "capture a CTF flag" does
// not crowd out the technique evidenced by the current step's prerequisites.
func (c *Catalog) Recall(query string, limit int) []Summary {
	tokens := queryTokens(query)
	for _, token := range strings.Fields("ctf flag flags web service services scan challenge authorized assess assessment security local remote origin capture collect http https") {
		delete(tokens, token)
	}
	return c.search(tokens, "", limit)
}

func queryTokens(query string) map[string]bool {
	for _, pair := range [][2]string{{"sql injection", "sqli"}, {"sql注入", "sqli"}, {"权限提升", "提权 privesc"}, {"反序列化", "deser"}, {"密码学", "crypto"}, {"二进制", "pwn"}, {"模板注入", "ssti"}} {
		if strings.Contains(strings.ToLower(query), pair[0]) {
			query += " " + pair[1]
		}
	}
	return tokenize(query)
}

func (c *Catalog) search(tokens map[string]bool, tag string, limit int) []Summary {
	if limit <= 0 {
		limit = 5
	}
	limit = min(limit, 50)
	out := []Summary{}
	for _, d := range c.documents {
		if !hasTag(d.entry, tag) {
			continue
		}
		score := 0.0
		for token := range tokens {
			score += d.weights[token] * c.idf[token]
		}
		if score > 0 {
			s := summary(d.entry)
			s.Score = math.Round(score*1000) / 1000
			out = append(out, s)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score == out[j].Score {
			return out[i].ID < out[j].ID
		}
		return out[i].Score > out[j].Score
	})
	return out[:min(limit, len(out))]
}

// Chinese bigrams retain technique phrases without matching unrelated entries
// merely because they share one common character. Non-Han words remain whole.
func tokenize(text string) map[string]bool {
	out := map[string]bool{}
	var word []rune
	han := false
	flush := func() {
		if han && len(word) > 1 {
			for i := 0; i+1 < len(word); i++ {
				out[string(word[i:i+2])] = true
			}
		} else if len(word) > 1 {
			out[string(word)] = true
		}
		word = nil
	}
	for _, r := range strings.ToLower(text) {
		isHan := unicode.Is(unicode.Han, r)
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			flush()
			continue
		}
		if len(word) > 0 && han != isHan {
			flush()
		}
		han = isHan
		word = append(word, r)
	}
	flush()
	for _, token := range strings.Fields("the and for with from this that into only current step project target verify inspect result evidence read use return file path 目标 步骤 验证 确认 测试 结果 证据 文件 读取 当前 项目 获取 分析 检查 执行 记录") {
		delete(out, token)
	}
	return out
}
