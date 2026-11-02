package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

func cleanText(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 32 || r == 127 {
			return ' '
		}
		return r
	}, s)
}
func (a *app) emit(value any) error {
	if a.quiet {
		return nil
	}
	// Normalize local typed maps/slices to the same shapes returned by HTTP.
	data, e := json.Marshal(value)
	if e != nil {
		return failed("encode output: %s", e)
	}
	if e = json.Unmarshal(data, &value); e != nil {
		return failed("encode output: %s", e)
	}
	if a.selectPath != "" {
		current := value
		for _, part := range strings.Split(a.selectPath, ".") {
			if part == "" {
				return invalid("invalid select path")
			}
			m, ok := current.(map[string]any)
			if !ok {
				return invalid("select path does not resolve to an object field")
			}
			var exists bool
			current, exists = m[part]
			if !exists {
				return invalid("selected field does not exist")
			}
		}
		switch current.(type) {
		case string, bool, int, int64, float64:
			_, e := fmt.Fprintln(a.out, current)
			if e != nil {
				return failed("write output: %s", e)
			}
			return nil
		default:
			return invalid("selected value must be a non-null scalar")
		}
	}
	switch a.output {
	case "json", "ndjson":
		enc := json.NewEncoder(a.out)
		enc.SetEscapeHTML(false)
		if a.output == "json" {
			enc.SetIndent("", "  ")
		}
		if e := enc.Encode(value); e != nil {
			return failed("write output: %s", e)
		}
	case "yaml":
		b, e := yaml.Marshal(value)
		if e != nil {
			return failed("encode output: %s", e)
		}
		if _, e = a.out.Write(b); e != nil {
			return failed("write output: %s", e)
		}
	default:
		return a.table(value)
	}
	return nil
}
func (a *app) table(value any) error {
	w := tabwriter.NewWriter(a.out, 0, 4, 2, ' ', 0)
	if list, ok := value.([]any); ok {
		if len(list) == 0 {
			_, e := fmt.Fprintln(a.out, "(empty)")
			return e
		}
		columns := []string{}
		for _, k := range []string{"id", "title", "description", "name", "status", "severity", "worker", "task_type", "created_at", "timestamp", "content", "count"} {
			for _, v := range list {
				if _, exists := object(v)[k]; exists {
					columns = append(columns, k)
					break
				}
			}
		}
		if len(columns) == 0 {
			columns = []string{"value"}
		}
		header := strings.ToUpper(strings.Join(columns, "\t"))
		if os.Getenv("NO_COLOR") == "" && (a.color == "always" || a.color == "auto" && isTerminal(a.out)) {
			header = "\x1b[1m" + header + "\x1b[0m"
		}
		fmt.Fprintln(w, header)
		for _, v := range list {
			row := []string{}
			for _, k := range columns {
				var cell string
				if k == "value" {
					cell = printable(v)
				} else {
					cell = textValue(object(v)[k])
				}
				cell = cleanText(cell)
				runes := []rune(cell)
				if len(runes) > 100 {
					cell = string(runes[:97]) + "..."
				}
				row = append(row, cell)
			}
			fmt.Fprintln(w, strings.Join(row, "\t"))
		}
	} else if m, ok := value.(map[string]any); ok {
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			cell := textValue(m[k])
			if _, ok := m[k].(map[string]any); ok {
				cell = printable(m[k])
			}
			if _, ok := m[k].([]any); ok {
				cell = printable(m[k])
			}
			if strings.Contains(cell, "\n") {
				fmt.Fprintf(w, "%s\t\n", k)
				for _, line := range strings.Split(cell, "\n") {
					fmt.Fprintf(w, "  %s\n", cleanText(line))
				}
			} else {
				fmt.Fprintf(w, "%s\t%s\n", k, cleanText(cell))
			}
		}
	} else {
		if value == nil {
			value = "unclaimed"
		}
		fmt.Fprintln(w, cleanText(textValue(value)))
	}
	if e := w.Flush(); e != nil {
		return failed("write output: %s", e)
	}
	return nil
}
func watchFlags(c *cobra.Command) {
	c.Flags().Bool("watch", false, "持续显示完整查询快照；请求错误时停止")
	c.Flags().Duration("interval", 2*time.Second, "轮询间隔，最小 1s")
}
func (a *app) watch(c *cobra.Command, args []string, run func(*cobra.Command, []string) (any, error)) error {
	interval, _ := c.Flags().GetDuration("interval")
	for {
		value, e := run(c, args)
		if e != nil {
			return e
		}
		if a.output == "ndjson" {
			value = map[string]any{"type": "snapshot", "resource": strings.TrimPrefix(c.CommandPath(), "hotmouse "), "observed_at": time.Now().UTC().Format(time.RFC3339), "data": value}
		} else if isTerminal(a.out) {
			if _, e = io.WriteString(a.out, "\x1b[H\x1b[2J"); e != nil {
				return e
			}
		}
		if e = a.emit(value); e != nil {
			return e
		}
		timer := time.NewTimer(interval)
		select {
		case <-c.Context().Done():
			timer.Stop()
			return c.Context().Err()
		case <-timer.C:
		}
	}
}
func filterList(c *cobra.Command, value any, exact map[string]string, queryFields []string) (any, error) {
	query, _ := c.Flags().GetString("query")
	query = strings.ToLower(query)
	since, _ := c.Flags().GetString("since")
	var at time.Time
	if since != "" {
		var e error
		at, e = time.Parse(time.RFC3339, since)
		if e != nil {
			return nil, invalid("since must be RFC3339")
		}
	}
	result := []any{}
	for _, v := range array(value) {
		m := object(v)
		match := true
		for flag, key := range exact {
			f := c.Flags().Lookup(flag)
			if f == nil {
				continue
			}
			val := f.Value.String()
			if val != "" && textValue(m[key]) != val {
				match = false
			}
		}
		if query != "" {
			has := false
			for _, k := range queryFields {
				has = has || strings.Contains(strings.ToLower(textValue(m[k])), query)
			}
			match = match && has
		}
		if since != "" {
			timestamp := textValue(m["timestamp"])
			if timestamp == "" {
				timestamp = textValue(m["created_at"])
			}
			t, e := time.Parse(time.RFC3339, timestamp)
			match = match && e == nil && !t.Before(at)
		}
		if match {
			result = append(result, v)
		}
	}
	if f := c.Flags().Lookup("limit"); f != nil {
		limit, _ := c.Flags().GetInt("limit")
		if c.Flags().Changed("limit") && limit < 1 {
			return nil, invalid("limit must be positive")
		}
		if limit > 0 && len(result) > limit {
			result = result[:limit]
		}
	}
	return result, nil
}
func listFlags(c *cobra.Command, filters ...string) {
	c.Flags().Int("limit", 0, "最大匹配项数；省略时不额外截取（仍受服务历史窗口限制）")
	for _, f := range filters {
		c.Flags().String(f, "", f+" 过滤条件")
	}
}
