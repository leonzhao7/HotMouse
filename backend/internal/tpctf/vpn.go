package tpctf

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Only wg-quick's declarative settings are accepted. Hooks execute shell code
// and are unnecessary for a participant tunnel. DNS needs resolvconf (in image).
func validateVPN(data []byte) error {
	section := ""
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") {
			section = line
			if section != "[Interface]" && section != "[Peer]" {
				return fmt.Errorf("unexpected WireGuard section")
			}
			continue
		}
		key, _, ok := strings.Cut(line, "=")
		if !ok {
			return fmt.Errorf("invalid WireGuard config")
		}
		key = strings.TrimSpace(key)
		allowed := map[string]bool{"PrivateKey": true, "Address": true, "ListenPort": true, "MTU": true, "DNS": true}
		if section == "[Peer]" {
			allowed = map[string]bool{"PublicKey": true, "PresharedKey": true, "AllowedIPs": true, "Endpoint": true, "PersistentKeepalive": true}
		}
		if !allowed[key] {
			return fmt.Errorf("unsupported WireGuard setting %s", key)
		}
	}
	return scanner.Err()
}

type vpnTunnel struct{ path string }

func (v vpnTunnel) close() error {
	if v.path == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	err := exec.CommandContext(ctx, "wg-quick", "down", v.path).Run()
	if err == nil {
		err = os.Remove(v.path)
	}
	return err
}
func connectVPN(ctx context.Context, c *Client, cfg Config, instance Instance) (vpnTunnel, error) {
	if cfg.VPNMode == "external" {
		return vpnTunnel{}, nil
	}
	// TPCTF requires VPN for every interactive target, even when an older server
	// omits network_topology. An empty service list can be an attachment-only task.
	if instance.VPN == nil || instance.VPN.ConfigURL == "" {
		if instance.Topology == "engagement" || len(instance.Services) > 0 || len(instance.URLs) > 0 {
			return vpnTunnel{}, fmt.Errorf("interactive instance has no vpn_endpoint.config_url; configure VPN externally with CTFY_VPN_MODE=external if already connected")
		}
		return vpnTunnel{}, nil
	}
	if os.Geteuid() != 0 {
		return vpnTunnel{}, fmt.Errorf("automatic WireGuard needs root and Docker --cap-add NET_ADMIN")
	}
	if _, err := exec.LookPath("wg-quick"); err != nil {
		return vpnTunnel{}, fmt.Errorf("wg-quick is not installed")
	}
	// A killed process can leave a tunnel in a still-running container network
	// namespace. Remove only interfaces recorded in our own private directory.
	stale, err := filepath.Glob(filepath.Join(cfg.WorkDir, "vpn", "hm*.conf"))
	if err != nil {
		return vpnTunnel{}, err
	}
	for _, path := range stale {
		name := strings.TrimSuffix(filepath.Base(path), ".conf")
		if exec.CommandContext(ctx, "ip", "link", "show", "dev", name).Run() == nil {
			if err := (vpnTunnel{path}).close(); err != nil {
				return vpnTunnel{}, fmt.Errorf("remove previous WireGuard tunnel: %w", err)
			}
		} else if err := os.Remove(path); err != nil {
			return vpnTunnel{}, err
		}
	}
	data, err := c.Download(ctx, instance.VPN.ConfigURL, 1<<20, true)
	if err != nil {
		return vpnTunnel{}, err
	}
	if err = validateVPN(data); err != nil {
		return vpnTunnel{}, err
	}
	dir := filepath.Join(cfg.WorkDir, "vpn")
	if err = os.MkdirAll(dir, 0700); err != nil {
		return vpnTunnel{}, err
	}
	path := filepath.Join(dir, "hm"+digest(instance.ID)[:10]+".conf")
	if err = os.WriteFile(path, data, 0600); err != nil {
		return vpnTunnel{}, err
	}
	// Recover a tunnel left in this network namespace by a killed agent process.
	name := strings.TrimSuffix(filepath.Base(path), ".conf")
	if exec.CommandContext(ctx, "ip", "link", "show", "dev", name).Run() == nil {
		if err = (vpnTunnel{path}).close(); err != nil {
			return vpnTunnel{}, err
		}
		if err = os.WriteFile(path, data, 0600); err != nil {
			return vpnTunnel{}, err
		}
	}
	command := exec.CommandContext(ctx, "wg-quick", "up", path)
	if err = command.Run(); err != nil {
		_ = (vpnTunnel{path}).close()
		// Debian's wg-quick supports a userspace implementation through this
		// variable when the host kernel has no WireGuard module.
		fallback := exec.CommandContext(ctx, "wg-quick", "up", path)
		fallback.Env = append(os.Environ(), "WG_QUICK_USERSPACE_IMPLEMENTATION=wireguard-go")
		if fallbackErr := fallback.Run(); fallbackErr != nil {
			_ = (vpnTunnel{path}).close()
			return vpnTunnel{}, fmt.Errorf("WireGuard setup failed: check NET_ADMIN, host WireGuard support and /dev/net/tun")
		}
	}
	return vpnTunnel{path}, nil
}
