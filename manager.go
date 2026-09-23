package main

import (
	"fmt"
	"log"
	"os/exec"
	"sort"
	"sync"
	"time"
)

// Manager 维护所有隧道，负责分配槽位与端口。
type Manager struct {
	mu           sync.RWMutex
	tunnels      map[int]*Tunnel
	nodes        []Node
	fetched      time.Time
	workDir      string
	maxSlots     int
	jobs         JobStore
	saveMu       sync.Mutex
	refreshMu    sync.Mutex
	qualityMu    sync.Mutex
	connectLimit chan struct{}
	pool         *PoolStore
	shutting     bool
}

func NewManager(maxSlots int, workDir string) *Manager {
	return &Manager{
		tunnels:      map[int]*Tunnel{},
		workDir:      workDir,
		maxSlots:     maxSlots,
		connectLimit: make(chan struct{}, 4),
	}
}

// RefreshNodes 重新拉取节点列表。
func (m *Manager) RefreshNodes() (int, error) {
	m.refreshMu.Lock()
	defer m.refreshMu.Unlock()
	nodes, err := fetchNodes(30 * time.Second)
	if m.pool != nil {
		imported, e := m.pool.Nodes()
		if e != nil {
			return 0, e
		}
		nodes = append(imported, nodes...)
		if len(nodes) > 0 {
			err = nil
		}
	}
	if err != nil {
		return 0, err
	}
	seen := map[string]bool{}
	unique := make([]Node, 0, len(nodes))
	for _, n := range nodes {
		if !seen[n.HostName] {
			unique = append(unique, n)
			seen[n.HostName] = true
		}
	}
	nodes = unique
	m.mu.Lock()
	m.nodes = nodes
	m.fetched = time.Now()
	m.mu.Unlock()
	return len(nodes), nil
}

func (m *Manager) Nodes() ([]Node, time.Time) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]Node, len(m.nodes))
	copy(out, m.nodes)
	return out, m.fetched
}

func (m *Manager) Tunnels() []*Tunnel {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]*Tunnel, 0, len(m.tunnels))
	for _, t := range m.tunnels {
		out = append(out, t.snapshot())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Slot < out[j].Slot })
	return out
}

// freeSlot 找一个未占用的槽位。槽位同时决定端口与网段。
func (m *Manager) freeSlot() (int, error) {
	for i := 1; i <= m.maxSlots; i++ {
		if _, used := m.tunnels[i]; !used {
			return i, nil
		}
	}
	return 0, fmt.Errorf("槽位已满（上限 %d）", m.maxSlots)
}

// Start 为指定节点开一条隧道，返回分配到的本地端口。
func (m *Manager) Start(node Node) (*Tunnel, error) {
	m.mu.Lock()
	if m.shutting {
		m.mu.Unlock()
		return nil, fmt.Errorf("正在关闭")
	}
	if m.pool != nil && node.Source != "" && node.Source != "vpngate" && !m.pool.HasSource(node.Source) {
		m.mu.Unlock()
		return nil, fmt.Errorf("来源已移除，请刷新候选列表")
	}
	for _, existing := range m.tunnels {
		if existing.snapshot().Node.HostName == node.HostName {
			m.mu.Unlock()
			return nil, fmt.Errorf("节点已被占用")
		}
	}
	slot, err := m.freeSlot()
	if err != nil {
		m.mu.Unlock()
		return nil, err
	}
	// 端口随机取，避免固定规律撞上机器上的其他服务
	taken := map[int]bool{}
	for _, other := range m.tunnels {
		taken[other.Port] = true
	}
	port, err := freeRandomPort(taken)
	if err != nil {
		m.mu.Unlock()
		return nil, err
	}
	cred, err := newSocksCred()
	if err != nil {
		m.mu.Unlock()
		return nil, err
	}
	t := &Tunnel{
		Slot:   slot,
		Port:   port,
		Node:   node,
		Status: "starting",
		Since:  time.Now(),
		Cred:   cred,
	}
	m.tunnels[slot] = t
	m.mu.Unlock()

	if err := m.saveState(); err != nil {
		m.mu.Lock()
		delete(m.tunnels, slot)
		m.mu.Unlock()
		return nil, err
	}
	go m.bringUp(t, true)
	return t, nil
}

// bringUp 把一条隧道拉起来。
//
// notify 决定成功后是否立刻重建后端配置。换节点重连时要传 false：
// 那条路径随后会调 rebind/resync 把入站改绑到新节点，在那之前重建配置
// 会因为入站还指着旧节点名而把路由规则丢掉。
func (m *Manager) bringUp(t *Tunnel, notify bool) {
	m.bringUpPersist(t, notify, false)
}

// 自动重连的退避区间：一轮候选全挂后等一会儿再刷新节点列表重来，
// 别把死节点列表打爆，也别让恢复拖太久。
const (
	reconnectBackoffMin = 5 * time.Second
	reconnectBackoffMax = 60 * time.Second
)

// bringUpPersist 把一条隧道拉起来。
//
// persist=false（手动新建）：走一轮候选，全失败就标 failed，让用户能立刻看到并重试。
// persist=true（自动重连 / 重启恢复）：一轮全失败不放弃，退避后刷新节点列表再来一轮，
// 一直循环到连上或这条隧道被用户停掉。VPN Gate 死节点多，"当前都不可用"往往只是
// 这一批候选恰好都挂了，过一会儿就有新节点，不该让出口永久躺死。
func (m *Manager) bringUpPersist(t *Tunnel, notify bool, persist bool) {
	backoff := reconnectBackoffMin
	for {
		if m.tryCandidates(t, notify) {
			return
		}
		// 隧道已被用户停掉或从管理器移除，别再重试
		if !persist || !m.tunnelActive(t) {
			if persist {
				return
			}
			t.state("failed", t.snapshot().Err)
			if serr := m.saveState(); serr != nil {
				log.Printf("保存状态失败: %v", serr)
			}
			return
		}

		t.state("starting", fmt.Sprintf("暂无可用节点，%.0f 秒后重试", backoff.Seconds()))
		log.Printf("隧道 %d 一轮候选均失败，%.0f 秒后刷新节点重试", t.Slot, backoff.Seconds())
		time.Sleep(backoff)
		if !m.tunnelActive(t) {
			return
		}
		if _, err := m.RefreshNodes(); err != nil {
			log.Printf("重试前刷新节点列表失败: %v", err)
		}
		if backoff < reconnectBackoffMax {
			backoff *= 2
			if backoff > reconnectBackoffMax {
				backoff = reconnectBackoffMax
			}
		}
	}
}

// tryCandidates 走一轮候选节点，成功返回 true。失败不改 Status（留给调用方决定）。
func (m *Manager) tryCandidates(t *Tunnel, notify bool) bool {
	candidates := m.candidatesFor(t.snapshot().Node)
	for i, node := range candidates {
		if !m.tunnelActive(t) {
			return false
		}
		m.mu.Lock()
		occupied := false
		for slot, other := range m.tunnels {
			if slot != t.Slot && other.snapshot().Node.HostName == node.HostName {
				occupied = true
				break
			}
		}
		if !occupied {
			t.mu.Lock()
			t.Node = node
			t.mu.Unlock()
		}
		m.mu.Unlock()
		if occupied {
			continue
		}
		t.state("starting", fmt.Sprintf("连接第 %d 个候选", i+1))
		m.connectLimit <- struct{}{}
		t.opMu.Lock()
		if !m.tunnelActive(t) {
			t.opMu.Unlock()
			<-m.connectLimit
			return false
		}
		err := m.tryNode(t)
		if err != nil {
			t.cleanup()
			t.state("starting", err.Error())
		}
		t.opMu.Unlock()
		<-m.connectLimit
		if err == nil && m.tunnelActive(t) {
			t.state("up", "")
			if m.pool != nil {
				m.pool.Record(node.HostName, true)
			}
			if e := m.saveState(); e != nil {
				log.Printf("保存状态: %v", e)
			}
			if notify {
				m.notifyPanel()
			}
			return true
		}
		if m.pool != nil {
			m.pool.Record(node.HostName, false)
		}
	}
	return false
}
func (m *Manager) tunnelActive(t *Tunnel) bool {
	m.mu.RLock()
	cur := m.tunnels[t.Slot]
	closing := m.shutting
	m.mu.RUnlock()
	return !closing && cur == t && t.snapshot().Status != "stopped"
}
func (m *Manager) tryNode(t *Tunnel) error {
	if coreEndpoint(t.snapshot().Node.Upstream) {
		if err := t.startSubscription(m.workDir); err != nil {
			return err
		}
	}
	if t.snapshot().Node.Upstream == "" {
		if err := t.setupNetns(); err != nil {
			return err
		}
		if err := t.startOpenVPN(m.workDir); err != nil {
			return err
		}
	}
	probeStarted := time.Now()
	ip, err := t.probeExitIP()
	probeMS := time.Since(probeStarted).Milliseconds()
	if err != nil {
		return err
	}
	if host := hostPublicIP(); host != "" && host == ip {
		return fmt.Errorf("出口与 VPS 公网 IP 相同，拒绝上架")
	}
	m.mu.Lock()
	duplicate := false
	for slot, other := range m.tunnels {
		v := other.snapshot()
		if slot != t.Slot && v.ExitIP == ip {
			duplicate = true
			break
		}
	}
	if !duplicate {
		t.mu.Lock()
		t.ExitIP = ip
		t.mu.Unlock()
	}
	m.mu.Unlock()
	if duplicate {
		return fmt.Errorf("出口 IP 重复，尝试其他节点")
	}
	quality := IPQuality{Type: "unknown", CheckedAt: time.Now()}
	if m.pool != nil {
		quality = m.pool.CheckIP(ip)
		if m.pool.Config().ResidentialOnly && quality.Type != "residential" {
			return fmt.Errorf("住宅筛选未通过：%s", quality.Type)
		}
	}
	t.mu.Lock()
	quality.LatencyMS = probeMS
	t.Quality = quality
	t.Since = time.Now()
	t.mu.Unlock()
	if t.listener == nil {
		if err = t.serve(); err != nil {
			return err
		}
	}
	if m.pool != nil {
		m.pool.RecordLatency(t.snapshot().Node.HostName, probeMS)
	}
	return nil
}

// candidatesFor 以指定节点打头，后面跟上同地区的其他节点作为备选。
func (m *Manager) candidatesFor(first Node) []Node {
	const maxTries = 6
	m.mu.RLock()
	defer m.mu.RUnlock()

	used := map[string]bool{first.HostName: true}
	for _, t := range m.tunnels {
		used[t.snapshot().Node.HostName] = true
	}

	// 地区决定了备选范围，缺失时先从当前列表补一次，
	// 否则会退化成"任意地区都算同区"。
	region := first.CountryCode
	if region == "" {
		for _, n := range m.nodes {
			if n.HostName == first.HostName {
				region = n.CountryCode
				break
			}
		}
	}

	out := []Node{first}
	for _, n := range m.nodes {
		if len(out) >= maxTries {
			break
		}
		if used[n.HostName] || (m.pool != nil && m.pool.Cooling(n.HostName)) {
			continue
		}
		// 地区实在拿不到时不做限制，总比连不上强
		if first.Upstream != "" && n.Upstream == "" {
			continue
		}
		if first.Upstream == "" && n.Upstream != "" {
			continue
		}
		if first.Source != "vpngate" && first.Source != "" && n.Source != first.Source {
			continue
		}
		if region != "" && n.CountryCode != region {
			continue
		}
		out = append(out, n)
		used[n.HostName] = true
	}
	return out
}

// Stop 停掉一条隧道并释放槽位。
func (m *Manager) Stop(slot int) error {
	m.mu.RLock()
	t := m.tunnels[slot]
	m.mu.RUnlock()
	if t == nil {
		return fmt.Errorf("出口不存在")
	}
	t.stop()
	m.mu.Lock()
	if m.tunnels[slot] == t {
		delete(m.tunnels, slot)
	}
	m.mu.Unlock()
	invalidateInbounds()
	if err := m.saveState(); err != nil {
		return err
	}
	m.notifyPanel()
	return nil
}
func (m *Manager) Swap(slot int) error {
	m.mu.RLock()
	t := m.tunnels[slot]
	m.mu.RUnlock()
	if t == nil {
		return fmt.Errorf("出口不存在")
	}
	v := t.snapshot()
	if v.Status == "starting" {
		return fmt.Errorf("正在连接，请稍候")
	}
	picks, err := m.pickNodes(v.Node.CountryCode, 1)
	if err != nil {
		return err
	}
	return m.beginReconnect(t, &picks[0])
}

// StopAll 停掉所有隧道并清空状态文件。
func (m *Manager) StopAll() {
	for _, t := range m.Tunnels() {
		_ = m.Stop(t.Slot)
	}
}

// SetCred 改一条出口的 SOCKS5 凭据。cred 两个字段都为空表示随机重置。
//
// 改完要通知后端：本机 Xray 的 socks 出站里带着这套凭据，
// 不同步的话面板侧的节点会立刻连不上自己的出口。
func (m *Manager) SetCred(slot int, cred SocksCred) (SocksCred, error) {
	m.mu.RLock()
	t, ok := m.tunnels[slot]
	m.mu.RUnlock()
	if !ok {
		return SocksCred{}, fmt.Errorf("槽位 %d 没有运行中的隧道", slot)
	}

	if cred.User == "" && cred.Pass == "" {
		gen, err := newSocksCred()
		if err != nil {
			return SocksCred{}, err
		}
		cred = gen
	}
	if err := validateCred(cred); err != nil {
		return SocksCred{}, err
	}

	t.setCredential(cred)
	if err := m.saveState(); err != nil {
		log.Printf("保存状态失败: %v", err)
	}
	m.syncCred(t)
	return cred, nil
}

// ReconcileOutbounds 在启动恢复隧道后跑一次，把后端出站对齐到当前隧道（含 SOCKS5 凭据）。
//
// 只为 3x-ui 模式而生：它的 OnTunnelsChanged 是空操作，重启不会重写面板出站，
// 而从旧版本升上来时面板里持久化的 socks 出站没有认证字段，端口一旦要认证就连不上。
// 自建模式恢复时每条隧道 up 都会重建配置，本就自洽，这里跳过免得多重启一次 Xray。
func (m *Manager) ReconcileOutbounds() {
	p, err := openPanel()
	if err != nil || p.Kind() != "3x-ui" {
		return
	}

	// 等隧道尽量都起完再重写一次，避免只覆盖到先 up 的那几条
	deadline := time.Now().Add(90 * time.Second)
	for {
		tunnels := m.Tunnels()
		if len(tunnels) == 0 {
			return
		}
		var up *Tunnel
		settled := true
		for _, t := range tunnels {
			if t.Status == "up" && up == nil {
				up = t
			}
			if t.Status == "starting" {
				settled = false
			}
		}
		if (settled || time.Now().After(deadline)) && up != nil {
			if err := m.resync(up); err != nil {
				log.Printf("启动对账面板出站失败: %v", err)
			}
			return
		}
		if settled || time.Now().After(deadline) {
			return // 全 failed，没有可写的出站
		}
		time.Sleep(2 * time.Second)
	}
}

// syncCred 把新凭据写进后端的 socks 出站。
//
// 两种后端的做法不同：自建模式整份重建配置，3x-ui 模式只改出站那一段。
// 都走 ResyncOutbound，接口语义正好是"重写这条隧道对应的出站"。
func (m *Manager) syncCred(t *Tunnel) {
	if err := m.resync(t); err != nil {
		log.Printf("同步 SOCKS5 凭据到节点链接后端失败: %v", err)
	}
}

// Shutdown 停掉运行态但保留状态文件，让下次启动能恢复同样的隧道。
func (m *Manager) Shutdown() {
	m.saveMu.Lock()
	m.mu.Lock()
	m.shutting = true
	m.mu.Unlock()
	if err := m.writeStateSnapshot(); err != nil {
		log.Printf("关闭前保存状态失败: %v", err)
	}
	m.saveMu.Unlock()
	m.mu.RLock()
	var live []*Tunnel
	for _, t := range m.tunnels {
		live = append(live, t)
	}
	m.mu.RUnlock()
	for _, t := range live {
		t.stop()
	}
}

// prepareHost 打开转发开关。netns 出网依赖它。
func prepareHost() error {
	if err := exec.Command("sysctl", "-qw", "net.ipv4.ip_forward=1").Run(); err != nil {
		return fmt.Errorf("开启 ip_forward 失败: %w", err)
	}
	return nil
}

// nodeInUse 判断某节点是否已被别的隧道占用。
func (m *Manager) nodeInUse(host string, exceptSlot int) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for slot, t := range m.tunnels {
		if slot != exceptSlot && t.snapshot().Node.HostName == host {
			return true
		}
	}
	return false
}

// rebind 在隧道换节点后，把原先指向旧节点的 3x-ui 入站改绑到新节点。
// 面板不可用时静默跳过，健康检查本身不应因此失败。
func (m *Manager) rebind(oldHost string, t *Tunnel) error {
	x, err := openPanel()
	if err != nil {
		return nil
	}
	return x.Rebind(oldHost, t.snapshot(), m.Tunnels())
}

// resync 在节点没换但重连过之后，把 3x-ui 的出站配置刷新一遍。
// 面板不可用时静默跳过，健康检查本身不应因此失败。
func (m *Manager) resync(t *Tunnel) error {
	x, err := openPanel()
	if err != nil {
		return nil
	}
	return x.ResyncOutbound(t.snapshot(), m.Tunnels())
}

// notifyPanel 告诉后端隧道集合变了。
//
// 自建模式下出站是由隧道列表现算出来的，不通知的话新开的出口在 Xray 里
// 没有对应的 socks 出站，绑定会指向一个不存在的 tag。接管 3x-ui 时是空操作。
// 后端不可用不该让开关出口失败，所以只记日志。
func (m *Manager) notifyPanel() {
	p, err := openPanel()
	if err != nil {
		return
	}
	if err := p.OnTunnelsChanged(m.Tunnels()); err != nil {
		log.Printf("同步节点链接后端失败: %v", err)
	}
}
