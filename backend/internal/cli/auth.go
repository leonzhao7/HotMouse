package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"
	"hotmouse/backend/internal/apiclient"
)

func (a *app) prompt(c *cobra.Command, label string, secret bool) (string, error) {
	if c.Context().Err() != nil {
		return "", c.Context().Err()
	}
	if a.nonInteractive || !isTerminal(a.in) {
		return "", invalid("%s requires interactive input or an explicit stdin/file option", label)
	}
	fmt.Fprint(a.errOut, label+": ")
	type answer struct {
		value string
		err   error
	}
	done := make(chan answer, 1)
	if secret {
		file := a.in.(*os.File)
		fd := int(file.Fd())
		state, e := term.GetState(fd)
		if e != nil {
			return "", e
		}
		defer term.Restore(fd, state)
		go func() { b, e := term.ReadPassword(fd); done <- answer{string(b), e} }()
	} else {
		go func() {
			line, e := a.input.ReadString('\n')
			if e == io.EOF && line != "" {
				e = nil
			}
			done <- answer{strings.TrimRight(line, "\r\n"), e}
		}()
	}
	select {
	case <-c.Context().Done():
		return "", c.Context().Err()
	case result := <-done:
		if secret {
			fmt.Fprintln(a.errOut)
		}
		if result.err != nil {
			return "", invalid("read %s failed", label)
		}
		return result.value, nil
	}
}
func (a *app) authCommand() *cobra.Command {
	root := group("auth", "管理算术验证码、会话和认证状态")
	root.AddCommand(a.leaf("captcha", "获取一次性算术验证码，5 分钟有效", 0, 0, "none", func(c *cobra.Command, _ []string) (any, error) { return a.get(c, "/api/auth/captcha") }))
	for _, action := range []string{"register", "login"} {
		c := a.leaf(action, "读取密码和验证码，保存服务会话", 0, 0, "none", func(c *cobra.Command, _ []string) (any, error) {
			username, _ := c.Flags().GetString("username")
			if strings.TrimSpace(username) == "" {
				return nil, invalid("--username is required")
			}
			captchaID, _ := c.Flags().GetString("captcha-id")
			answer, _ := c.Flags().GetString("captcha-answer")
			stdin, _ := c.Flags().GetBool("password-stdin")
			if (captchaID == "") != (answer == "") {
				return nil, invalid("provide both captcha-id and captcha-answer")
			}
			if captchaID == "" {
				if stdin || a.nonInteractive || !isTerminal(a.in) {
					return nil, invalid("non-interactive authentication requires captcha-id and captcha-answer")
				}
				challenge, e := a.get(c, "/api/auth/captcha")
				if e != nil {
					return nil, e
				}
				m := object(challenge)
				captchaID = textValue(m["captcha_id"])
				answer, e = a.prompt(c, textValue(m["question"]), false)
				if e != nil {
					return nil, e
				}
			}
			var password string
			if stdin {
				b, e := a.readFile("-")
				if e != nil {
					return nil, e
				}
				password = strings.TrimRight(string(b), "\r\n")
			} else {
				var e error
				password, e = a.prompt(c, "密码", true)
				if e != nil {
					return nil, e
				}
			}
			if password == "" {
				return nil, invalid("password must not be empty")
			}
			return a.request(c, "POST", "/api/auth/"+c.Name(), map[string]any{"username": username, "password": password, "captcha_id": captchaID, "captcha_answer": answer})
		})
		c.Flags().String("username", "", "用户名")
		c.Flags().String("captcha-id", "", "auth captcha 返回的挑战 ID")
		c.Flags().String("captcha-answer", "", "算术挑战答案")
		c.Flags().Bool("password-stdin", false, "从 stdin 读取密码，不能与其他 stdin 输入组合")
		c.Example = "  hotmouse auth " + action + " --username operator"
		root.AddCommand(c)
	}
	root.AddCommand(a.leaf("whoami", "查询当前会话用户；内部令牌没有普通用户身份", 0, 0, "none", func(c *cobra.Command, _ []string) (any, error) {
		client, e := a.api(c)
		if e != nil {
			return nil, e
		}
		if client.Cookie == nil {
			return nil, apiclient.Failure(3, "no session; run auth login")
		}
		return a.get(c, "/api/auth/me")
	}))
	root.AddCommand(a.leaf("status", "查询认证模式；未认证状态也成功返回", 0, 0, "none", func(c *cobra.Command, _ []string) (any, error) {
		client, e := a.api(c)
		if e != nil {
			return nil, e
		}
		none := map[string]any{"authenticated": false, "mode": "none", "user": nil}
		if client.Token != "" {
			v, e := a.get(c, "/api/settings/health")
			if e != nil {
				var f *apiclient.Error
				if errors.As(e, &f) && f.HTTPStatus != nil && *f.HTTPStatus == 401 {
					return none, nil
				}
				return nil, e
			}
			for _, check := range array(object(v)["checks"]) {
				m := object(check)
				if m["key"] == "auth" && m["status"] == "warning" {
					return map[string]any{"authenticated": false, "mode": "disabled", "user": nil}, nil
				}
			}
			return map[string]any{"authenticated": true, "mode": "internal", "user": nil}, nil
		}
		if client.Cookie == nil {
			return none, nil
		}
		v, e := a.get(c, "/api/auth/me")
		if e != nil {
			var f *apiclient.Error
			if errors.As(e, &f) && f.HTTPStatus != nil && *f.HTTPStatus == 401 {
				return none, nil
			}
			return nil, e
		}
		return map[string]any{"authenticated": true, "mode": "session", "user": v}, nil
	}))
	root.AddCommand(a.leaf("logout", "吊销当前服务会话并删除本地 cookie", 0, 0, "none", func(c *cobra.Command, _ []string) (any, error) {
		client, e := a.api(c)
		if e != nil {
			return nil, e
		}
		if client.Cookie == nil {
			return nil, apiclient.Failure(3, "no session to log out")
		}
		v, e := a.request(c, "POST", "/api/auth/logout", nil)
		if e != nil {
			return nil, e
		}
		if e = client.SaveCookie(nil); e != nil {
			return nil, failed("session revoked, but removing local cookie failed: %s", e)
		}
		client.Cookie = nil
		return v, nil
	}))
	password := a.leaf("password", "修改当前会话密码；新密码必须包含大小写、数字和特殊字符", 0, 0, "none", func(c *cobra.Command, _ []string) (any, error) {
		client, e := a.api(c)
		if e != nil {
			return nil, e
		}
		if client.Cookie == nil {
			return nil, apiclient.Failure(3, "password change requires a session")
		}
		var body map[string]any
		if c.Flags().Changed("file") {
			body, e = a.inputObject(c)
			if e != nil {
				return nil, e
			}
		} else {
			old, e := a.prompt(c, "当前密码", true)
			if e != nil {
				return nil, e
			}
			newPassword, e := a.prompt(c, "新密码", true)
			if e != nil {
				return nil, e
			}
			body = map[string]any{"current_password": old, "new_password": newPassword}
		}
		if e = validateObject(body, []field{str("current_password", true), str("new_password", true)}); e != nil {
			return nil, e
		}
		return a.request(c, "PUT", "/api/auth/password", body)
	})
	password.Flags().String("file", "", "包含 current_password/new_password 的 JSON/YAML 对象（- 表示 stdin）")
	root.AddCommand(password)
	return root
}
