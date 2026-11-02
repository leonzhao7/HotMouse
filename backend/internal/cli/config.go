package cli

import (
	"github.com/spf13/cobra"
	"hotmouse/backend/internal/clientconfig"
)

func (a *app) configCommand() *cobra.Command {
	root := group("config", "管理客户端配置和服务 Profile")
	root.AddCommand(a.leaf("path", "显示配置路径", 0, 0, "none", func(_ *cobra.Command, _ []string) (any, error) { return a.store.Path, nil }))
	show := a.leaf("show", "显示选中 Profile；effective 包含配置来源", 0, 0, "none", func(c *cobra.Command, _ []string) (any, error) {
		effective, _ := c.Flags().GetBool("effective")
		if effective {
			return map[string]any{"profile": a.profile, "values": a.values, "sources": a.sources}, nil
		}
		return a.store.Config.Profiles[a.profile], nil
	})
	show.Flags().Bool("effective", false, "显示合并后的配置和各字段来源")
	root.AddCommand(show)
	root.AddCommand(a.leaf("get <KEY>", "读取 Profile 配置键", 1, 1, "none", func(_ *cobra.Command, args []string) (any, error) {
		v, ok := a.store.Config.Profiles[a.profile][args[0]]
		if !ok {
			return nil, invalid("key is not set")
		}
		return v, nil
	}))
	root.AddCommand(a.leaf("set <KEY> <VALUE>", "修改 Profile 的一个配置键", 2, 2, "none", func(_ *cobra.Command, args []string) (any, error) {
		key, value := args[0], args[1]
		if e := clientconfig.Validate(key, value); e != nil {
			return nil, invalid("%s", e)
		}
		p := a.store.Config.Profiles[a.profile]
		if key == "server" {
			value, _ = clientconfig.NormalizeServer(value)
			old := p[key]
			if old == "" {
				old = clientconfig.DefaultServer
			}
			old, _ = clientconfig.NormalizeServer(old)
			if old != value {
				delete(p, "project")
				delete(p, "token_file")
			}
		}
		p[key] = value
		if e := a.store.Save(); e != nil {
			return nil, failed("save configuration: %s", e)
		}
		return p, nil
	}))
	root.AddCommand(a.leaf("unset <KEY>", "移除 Profile 配置键并恢复默认值", 1, 1, "none", func(_ *cobra.Command, args []string) (any, error) {
		known := false
		for _, k := range clientconfig.Keys {
			known = known || k == args[0]
		}
		if !known {
			return nil, invalid("unknown configuration key")
		}
		p := a.store.Config.Profiles[a.profile]
		delete(p, args[0])
		if args[0] == "server" {
			delete(p, "project")
			delete(p, "token_file")
		}
		if e := a.store.Save(); e != nil {
			return nil, failed("save configuration: %s", e)
		}
		return p, nil
	}))
	profiles := group("profile", "管理多个服务连接")
	profiles.AddCommand(a.leaf("list", "列出 Profile", 0, 0, "none", func(_ *cobra.Command, _ []string) (any, error) {
		return map[string]any{"current_profile": a.store.Config.CurrentProfile, "profiles": a.store.Config.Profiles}, nil
	}))
	profiles.AddCommand(a.leaf("add <NAME>", "添加 Profile，使用 --server 指定服务", 1, 1, "none", func(_ *cobra.Command, args []string) (any, error) {
		name := args[0]
		if !clientconfig.ValidName(name) {
			return nil, invalid("invalid profile name")
		}
		if _, ok := a.store.Config.Profiles[name]; ok {
			return nil, invalid("profile already exists")
		}
		p := map[string]string{"server": a.values["server"]}
		a.store.Config.Profiles[name] = p
		if e := a.store.Save(); e != nil {
			return nil, failed("save configuration: %s", e)
		}
		return p, nil
	}))
	profiles.AddCommand(a.leaf("use <NAME>", "选择当前 Profile", 1, 1, "none", func(_ *cobra.Command, args []string) (any, error) {
		if _, ok := a.store.Config.Profiles[args[0]]; !ok {
			return nil, invalid("profile does not exist")
		}
		a.store.Config.CurrentProfile = args[0]
		if e := a.store.Save(); e != nil {
			return nil, failed("save configuration: %s", e)
		}
		return map[string]any{"current_profile": args[0]}, nil
	}))
	remove := a.leaf("remove <NAME>", "删除非当前 Profile 及其本地凭据；不会吊销服务会话", 1, 1, "none", func(c *cobra.Command, args []string) (any, error) {
		name := args[0]
		if name == a.store.Config.CurrentProfile {
			return nil, invalid("switch away from the current profile before removing it")
		}
		if _, ok := a.store.Config.Profiles[name]; !ok {
			return nil, invalid("profile does not exist")
		}
		if e := a.confirm(c, "删除 Profile "+name+" 及其本地凭据"); e != nil {
			return nil, e
		}
		delete(a.store.Config.Profiles, name)
		if e := a.store.Save(); e != nil {
			return nil, failed("save configuration: %s", e)
		}
		if e := a.store.RemoveCredentials(name); e != nil {
			return nil, failed("profile removed, but removing credentials failed: %s", e)
		}
		return map[string]any{"deleted": true, "id": name}, nil
	})
	remove.Annotations["confirm"] = "true"
	profiles.AddCommand(remove)
	root.AddCommand(profiles)
	return root
}
