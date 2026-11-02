package cli

import (
	"fmt"
	"io"
	"math"
	"strings"
	"unicode/utf8"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"gopkg.in/yaml.v3"
)

type field struct {
	Name, Flag, Kind, Default string
	Required                  bool
}

func str(name string, required bool) field {
	return field{Name: name, Flag: strings.ReplaceAll(name, "_", "-"), Kind: "string", Required: required}
}
func alias(name, flag string) field         { f := str(name, false); f.Flag = flag; return f }
func number(name string) field              { f := str(name, false); f.Kind = "int"; return f }
func list(name string, required bool) field { f := str(name, required); f.Kind = "strings"; return f }
func (a *app) bodyFlags(c *cobra.Command, fields []field) {
	c.Flags().String("file", "", "单个 JSON/YAML 请求对象（- 表示 stdin）；与业务参数互斥")
	for _, f := range fields {
		description := f.Name + "；可使用对应文件参数"
		if f.Required {
			description += "；必填，可由请求文件或默认身份提供"
		}
		switch f.Kind {
		case "string":
			c.Flags().String(f.Flag, "", description)
			c.Flags().String(f.Flag+"-file", "", f.Name+" 的 UTF-8 文件（- 表示 stdin）")
		case "int":
			c.Flags().Int(f.Flag, 0, f.Name+" 整数；只修改明确指定的值")
		case "strings":
			c.Flags().StringArray(f.Flag, nil, description+"，可重复；from 支持逗号分隔")
		}
		if f.Required && c.Flags().Lookup(f.Flag) != nil {
			_ = c.Flags().SetAnnotation(f.Flag, "required-input", []string{"true"})
		}
	}
}
func decodeObject(data []byte) (map[string]any, error) {
	if !utf8.Valid(data) {
		return nil, invalid("input must be UTF-8")
	}
	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	var node yaml.Node
	if e := dec.Decode(&node); e != nil {
		return nil, invalid("invalid JSON/YAML input")
	}
	if len(node.Content) != 1 || node.Content[0].Kind != yaml.MappingNode {
		return nil, invalid("input must be a single object")
	}
	var extra yaml.Node
	if e := dec.Decode(&extra); e != io.EOF {
		return nil, invalid("input must contain exactly one document")
	}
	var result map[string]any
	if node.Decode(&result) != nil {
		return nil, invalid("invalid or duplicate input fields")
	}
	return result, nil
}
func (a *app) inputObject(c *cobra.Command) (map[string]any, error) {
	path, _ := c.Flags().GetString("file")
	if path == "" {
		return nil, invalid("--file is required")
	}
	b, e := a.readFile(path)
	if e != nil {
		return nil, e
	}
	return decodeObject(b)
}
func (a *app) body(c *cobra.Command, fields []field) (map[string]any, error) {
	var result map[string]any
	if c.Flags().Changed("file") {
		conflict := ""
		c.LocalNonPersistentFlags().VisitAll(func(f *pflag.Flag) {
			if f.Changed && f.Name != "file" && f.Name != "use" {
				conflict = f.Name
			}
		})
		if conflict != "" {
			return nil, invalid("--file conflicts with --%s", conflict)
		}
		var e error
		result, e = a.inputObject(c)
		if e != nil {
			return nil, e
		}
	} else {
		result = map[string]any{}
		for _, f := range fields {
			switch f.Kind {
			case "string":
				if c.Flags().Changed(f.Flag) && c.Flags().Changed(f.Flag+"-file") {
					return nil, invalid("--%s conflicts with --%s-file", f.Flag, f.Flag)
				}
				if c.Flags().Changed(f.Flag) {
					value, _ := c.Flags().GetString(f.Flag)
					result[f.Name] = value
				}
				if c.Flags().Changed(f.Flag + "-file") {
					path, _ := c.Flags().GetString(f.Flag + "-file")
					b, e := a.readFile(path)
					if e != nil {
						return nil, e
					}
					if !utf8.Valid(b) {
						return nil, invalid("text file must be UTF-8")
					}
					result[f.Name] = string(b)
				}
			case "int":
				if c.Flags().Changed(f.Flag) {
					v, _ := c.Flags().GetInt(f.Flag)
					result[f.Name] = v
				}
			case "strings":
				if c.Flags().Changed(f.Flag) {
					v, _ := c.Flags().GetStringArray(f.Flag)
					if f.Name == "from" {
						v = splitFacts(v)
					}
					result[f.Name] = v
				}
			}
		}
	}
	for _, f := range fields {
		if _, ok := result[f.Name]; !ok {
			switch {
			case f.Name == "creator":
				value := a.values["creator"]
				if f.Default != "" && a.sources["creator"] == "default" {
					value = f.Default
				}
				result[f.Name] = value
			case f.Name == "worker" && f.Required:
				if a.values["worker"] != "" {
					result[f.Name] = a.values["worker"]
				}
			case f.Default != "":
				result[f.Name] = f.Default
			}
		}
	}
	for _, hint := range array(result["hints"]) {
		if m := object(hint); m != nil {
			if _, ok := m["creator"]; !ok {
				creator := a.values["creator"]
				if c.Parent().Name() == "template" && a.sources["creator"] == "default" {
					creator = "template"
				}
				m["creator"] = creator
			}
		}
	}
	if e := validateObject(result, fields); e != nil {
		return nil, e
	}
	return result, nil
}
func splitFacts(values []string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, v := range values {
		for _, id := range strings.Split(v, ",") {
			id = strings.TrimSpace(id)
			if !seen[id] {
				out = append(out, id)
				seen[id] = true
			}
		}
	}
	return out
}
func validateObject(value map[string]any, fields []field) error {
	known := map[string]field{}
	for _, f := range fields {
		known[f.Name] = f
	}
	for k, v := range value {
		f, ok := known[k]
		if !ok {
			return invalid("unknown request field %q", k)
		}
		if v == nil {
			return invalid("%s cannot be null or cleared", k)
		}
		switch f.Kind {
		case "string":
			s, ok := v.(string)
			if !ok || strings.TrimSpace(s) == "" {
				return invalid("%s must be a non-empty string", k)
			}
		case "int":
			if _, ok := integer(v); !ok {
				return invalid("%s must be an integer", k)
			}
		case "strings":
			ss, ok := stringsList(v)
			if !ok || len(ss) == 0 {
				return invalid("%s must be a non-empty string array", k)
			}
			for _, s := range ss {
				if strings.TrimSpace(s) == "" {
					return invalid("%s contains an empty value", k)
				}
			}
		case "object":
			if _, ok := v.(map[string]any); !ok {
				return invalid("%s must be an object", k)
			}
		case "finding":
			if m, ok := v.(map[string]any); !ok {
				return invalid("finding must be an object")
			} else if e := validateObject(m, findingSchema(false)); e != nil {
				return e
			}
		case "hints":
			ss, ok := v.([]any)
			if !ok {
				return invalid("hints must be an array")
			}
			for _, hint := range ss {
				m, ok := hint.(map[string]any)
				if !ok {
					return invalid("each hint must be an object")
				}
				if e := validateObject(m, []field{str("content", true), str("creator", true)}); e != nil {
					return e
				}
			}
		}
	}
	for _, f := range fields {
		if f.Required {
			if _, ok := value[f.Name]; !ok {
				return invalid("%s is required", f.Name)
			}
		}
	}
	if severity, ok := value["severity"]; ok {
		if !oneOf(textValue(severity), "critical", "high", "medium", "low", "info") {
			return invalid("invalid severity")
		}
	}
	return nil
}
func integer(v any) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case int64:
		return int(n), int64(int(n)) == n
	case float64:
		if math.IsNaN(n) || math.IsInf(n, 0) || math.Trunc(n) != n || n >= float64(math.MaxInt) || n < float64(math.MinInt) {
			return 0, false
		}
		return int(n), true
	}
	return 0, false
}
func stringsList(v any) ([]string, bool) {
	if s, ok := v.([]string); ok {
		return s, true
	}
	if a, ok := v.([]any); ok {
		s := []string{}
		for _, v := range a {
			t, ok := v.(string)
			if !ok {
				return nil, false
			}
			s = append(s, t)
		}
		return s, true
	}
	return nil, false
}
func oneOf(value string, allowed ...string) bool {
	for _, v := range allowed {
		if v == value {
			return true
		}
	}
	return false
}
func requiredChanged(c *cobra.Command, names ...string) error {
	for _, name := range names {
		if c.Flags().Changed(name) {
			return nil
		}
	}
	return invalid("provide at least one field to update")
}
func findingSchema(fact bool) []field {
	fs := []field{str("title", true), str("description", true), str("severity", false), str("kind", false), {Name: "data", Kind: "object"}}
	if fact {
		fs = append(fs, alias("fact_id", "fact"))
	}
	return fs
}
func (a *app) findingExtra(c *cobra.Command, body map[string]any) error {
	if c.Flags().Changed("data-file") {
		path, _ := c.Flags().GetString("data-file")
		b, e := a.readFile(path)
		if e != nil {
			return e
		}
		m, e := decodeObject(b)
		if e != nil {
			return e
		}
		body["data"] = m
	}
	if c.Flags().Changed("evidence") {
		values, _ := c.Flags().GetStringArray("evidence")
		data := object(body["data"])
		if data == nil {
			data = map[string]any{}
		}
		existing := []string{}
		if v, ok := data["evidence"]; ok {
			var valid bool
			existing, valid = stringsList(v)
			if !valid {
				return invalid("data.evidence must be a string array before appending")
			}
		}
		for _, v := range values {
			if strings.TrimSpace(v) == "" {
				return invalid("evidence must not be empty")
			}
		}
		data["evidence"] = append(existing, values...)
		body["data"] = data
	}
	return nil
}
func printable(v any) string {
	b, e := yaml.Marshal(v)
	if e != nil {
		return fmt.Sprint(v)
	}
	return strings.TrimSpace(string(b))
}
