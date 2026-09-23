package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

var maintenanceMu sync.Mutex

func maintenanceCommand(action string) ([]string, error) {
	if action != "app-update" && action != "core-install" {
		return nil, fmt.Errorf("不支持的维护操作")
	}
	return []string{"--unit=fanout-maintenance", "--no-block", "--collect", "--property=Type=oneshot", "--property=RuntimeMaxSec=1800", "--property=UMask=0077", "/bin/bash", "/usr/local/lib/fanout-integrated/maintenance.sh", action}, nil
}
func maintenanceAvailable() bool {
	_, e := os.Stat("/usr/local/lib/fanout-integrated/maintenance.sh")
	return runtime.GOOS == "linux" && os.Geteuid() == 0 && e == nil
}

func maintenanceStatus(dir string) map[string]any {
	result := map[string]any{"status": "idle", "available": maintenanceAvailable(), "recommended_core": "1.14.1", "core_installed": subscriptionCoreReady()}
	path := filepath.Join(dir, "maintenance.json")
	if b, e := os.ReadFile(path); e == nil && len(b) < 8192 {
		var record map[string]any
		if json.Unmarshal(b, &record) == nil {
			for _, k := range []string{"status", "action", "finished", "exit_code"} {
				if v, ok := record[k]; ok {
					result[k] = v
				}
			}
		}
	}
	if result["status"] == "running" && maintenanceAvailable() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		b, e := exec.CommandContext(ctx, "systemctl", "show", "fanout-maintenance.service", "--property=ActiveState", "--value").Output()
		state := strings.TrimSpace(string(b))
		if e == nil && state != "active" && state != "activating" {
			result["status"] = "interrupted"
		}
	}
	if f, e := os.Open(filepath.Join(dir, "maintenance.log")); e == nil {
		defer f.Close()
		if st, e := f.Stat(); e == nil {
			offset := st.Size() - 12000
			if offset < 0 {
				offset = 0
			}
			f.Seek(offset, 0)
			b, _ := io.ReadAll(io.LimitReader(f, 12000))
			result["log"] = string(b)
		}
	}
	return result
}

func apiMaintenance(m *Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			writeJSON(w, 200, maintenanceStatus(m.workDir))
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "请使用 GET 或 POST", 405)
			return
		}
		var req struct {
			Action string `json:"action"`
		}
		if json.NewDecoder(r.Body).Decode(&req) != nil {
			http.Error(w, "JSON 无效", 400)
			return
		}
		args, e := maintenanceCommand(req.Action)
		if e != nil {
			http.Error(w, e.Error(), 400)
			return
		}
		if !maintenanceAvailable() {
			http.Error(w, "请先用新版完整安装包升级一次以启用面板维护", 409)
			return
		}
		if !maintenanceMu.TryLock() {
			http.Error(w, "维护任务正在启动", 409)
			return
		}
		defer maintenanceMu.Unlock()
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		if e = exec.CommandContext(ctx, "systemd-run", args...).Run(); e != nil {
			http.Error(w, "维护任务未启动：可能已有任务正在运行，请查看维护状态", 409)
			return
		}
		writeJSON(w, 202, map[string]any{"status": "starting", "message": "维护任务已提交，进度保存在 VPS；更新重启后会自动恢复显示"})
	}
}
