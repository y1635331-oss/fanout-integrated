package main

import (
	"fmt"
	"sync"
	"time"
)

const (
	healthInterval = 10 * time.Second
	healthFailures = 2
	healthTimeout  = 15 * time.Second
)

func (m *Manager) WatchHealth() {
	fails := map[int]int{}
	ticker := time.NewTicker(healthInterval)
	defer ticker.Stop()
	for range ticker.C {
		type result struct {
			slot    int
			healthy bool
		}
		ch := make(chan result, m.maxSlots)
		var wg sync.WaitGroup
		limit := make(chan struct{}, 4)
		for _, v := range m.Tunnels() {
			if v.Status == "failed" {
				m.mu.RLock()
				t := m.tunnels[v.Slot]
				m.mu.RUnlock()
				if t != nil {
					_ = m.beginReconnect(t, nil)
				}
				continue
			}
			if v.Status != "up" {
				continue
			}
			wg.Add(1)
			go func(v *Tunnel) {
				defer wg.Done()
				limit <- struct{}{}
				ok := m.tunnelHealthy(v)
				<-limit
				ch <- result{v.Slot, ok}
			}(v)
		}
		wg.Wait()
		close(ch)
		for r := range ch {
			if r.healthy {
				fails[r.slot] = 0
				continue
			}
			fails[r.slot]++
			if fails[r.slot] >= healthFailures {
				fails[r.slot] = 0
				m.mu.RLock()
				t := m.tunnels[r.slot]
				m.mu.RUnlock()
				if t != nil {
					_ = m.beginReconnect(t, nil)
				}
			}
		}
	}
}
func (m *Manager) tunnelHealthy(t *Tunnel) bool {
	ip, err := t.probeExitIP()
	return err == nil && ip == t.ExitIP
}
func (m *Manager) beginReconnect(t *Tunnel, replacement *Node) error {
	t.mu.Lock()
	if t.Status == "starting" || t.Status == "stopped" {
		t.mu.Unlock()
		return fmt.Errorf("出口正在连接或已停止")
	}
	oldHost := t.Node.HostName
	t.Status = "starting"
	t.ExitIP = ""
	t.Err = "正在重新连接"
	t.mu.Unlock()
	go func() {
		t.opMu.Lock()
		if !m.tunnelActive(t) {
			t.opMu.Unlock()
			return
		}
		t.cleanup()
		if replacement != nil {
			t.mu.Lock()
			t.Node = *replacement
			t.mu.Unlock()
		}
		t.opMu.Unlock()
		m.bringUpPersist(t, false, true)
		v := t.snapshot()
		if v.Status == "up" {
			if oldHost != v.Node.HostName {
				_ = m.rebind(oldHost, t)
			} else {
				_ = m.resync(t)
			}
		}
	}()
	return nil
}
func (m *Manager) reconnect(t *Tunnel, oldHost string) { _ = m.beginReconnect(t, nil) }
