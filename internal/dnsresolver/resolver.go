package dnsresolver

import (
	"cmp"
	"fmt"
	"net"
	"net/netip"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/eclipse-iofog/edgelet/internal/config"
	"github.com/eclipse-iofog/edgelet/internal/constants"
	"github.com/eclipse-iofog/edgelet/internal/network"
	"github.com/eclipse-iofog/edgelet/internal/utils/logging"
	"github.com/miekg/dns"
)

const (
	moduleName            = "EmbeddedDNS"
	defaultZoneName       = "svc.bridge.local"
	reservedRouterName    = "router.default.svc.bridge.local"
	reservedNatsName      = "nats.default.svc.bridge.local"
	reservedAgentName     = "edgelet.default.svc.bridge.local"
	compatDockerHostName  = "host.docker.internal"
	compatContainerHost   = "host.container.internal"
	bindRetryInterval     = 5 * time.Second
	reconcileDefaultEvery = 60 * time.Second
	snapshotDefaultEvery  = 60 * time.Second
	defaultForwardTimeout = 2 * time.Second
)

type Scope string

const (
	ScopeManaged Scope = "edgelet"
	ScopeLocal   Scope = "iofog-local"
)

type WorkloadRecord struct {
	UUID         string
	Application  string
	Name         string
	Scope        Scope
	IP           string
	HostNetwork  bool
	IsRouter     bool
	IsNats       bool
	IsController bool
	Active       bool
	StartedAt    int64
}

type serverSet struct {
	addr string
	udp  *dns.Server
	tcp  *dns.Server
}

type ScopeListenerState struct {
	Listening bool
	Address   string
}

type StatsSnapshot struct {
	Started                 bool
	CompatAliasesEnabled    bool
	ReconcileIntervalSec    int64
	RateLimitEnabled        bool
	RateLimitRPS            int
	RateLimitBurst          int
	MaxRequestBytes         int
	MaxQNameBytes           int
	ForwardingDegraded      bool
	ForwardTotalUpstream    uint64
	ForwardHealthyUpstream  uint64
	ForwardLastSuccessUnix  int64
	ForwardLastFailureUnix  int64
	ScopeManaged            ScopeListenerState
	ScopeLocal              ScopeListenerState
	QueriesTotal            uint64
	SuccessTotal            uint64
	NXDomainTotal           uint64
	ServFailTotal           uint64
	PolicyDeniedTotal       uint64
	InactiveTotal           uint64
	ForwardedTotal          uint64
	ForwardErrTotal         uint64
	ReconcileRunsTotal      uint64
	ReconcileAddTotal       uint64
	ReconcileUpdateTotal    uint64
	ReconcileRemoveTotal    uint64
	ReconcileErrorTotal     uint64
	ForwardBackoffSkipTotal uint64
	RateLimitedTotal        uint64
	RejectedTotal           uint64
}

type Resolver struct {
	mu sync.RWMutex

	workloads    map[string]*WorkloadRecord
	index        map[Scope]map[string]map[string]struct{}
	scopeEnabled map[Scope]bool

	servers map[Scope]*serverSet

	started                bool
	stopCh                 chan struct{}
	wg                     sync.WaitGroup
	compatOn               bool
	reconcileProvider      RuntimeSnapshotProvider
	reconcileEvery         time.Duration
	forwardState           map[string]*upstreamForwardState
	forwardResolveFn       func() ([]string, error)
	forwardNowFn           func() time.Time
	forwardDegraded        bool
	forwardLastSuccessUnix int64
	forwardLastFailureUnix int64
	rateLimiter            *sourceRateLimiter
	logSampler             *sampledLogger
	rateLimitEnabled       bool
	rateLimitRPS           int
	rateLimitBurst         int
	maxRequestBytes        int
	maxQNameBytes          int
	snapshotPath           string
	snapshotEvery          time.Duration
	snapshotTriggerCh      chan struct{}
	startScopeServerFn     func(scope Scope, addr string) error

	queriesTotal        atomic.Uint64
	successTotal        atomic.Uint64
	nxdomainTotal       atomic.Uint64
	servfailTotal       atomic.Uint64
	policyDeniedTotal   atomic.Uint64
	inactiveTotal       atomic.Uint64
	forwardedTotal      atomic.Uint64
	forwardErrTotal     atomic.Uint64
	reconcileRuns       atomic.Uint64
	reconcileAdds       atomic.Uint64
	reconcileUpdates    atomic.Uint64
	reconcileRemoves    atomic.Uint64
	reconcileErrors     atomic.Uint64
	forwardBackoffSkips atomic.Uint64
	rateLimitedTotal    atomic.Uint64
	rejectedTotal       atomic.Uint64
	snapshotRevision    atomic.Uint64
	snapshotPersisted   atomic.Uint64
}

var (
	instance *Resolver
	once     sync.Once
)

func GetInstance() *Resolver {
	once.Do(func() {
		instance = &Resolver{
			workloads: make(map[string]*WorkloadRecord),
			index: map[Scope]map[string]map[string]struct{}{
				ScopeManaged: make(map[string]map[string]struct{}),
				ScopeLocal:   make(map[string]map[string]struct{}),
			},
			scopeEnabled: map[Scope]bool{
				ScopeManaged: false,
				ScopeLocal:   false,
			},
			servers:          make(map[Scope]*serverSet),
			forwardState:     make(map[string]*upstreamForwardState),
			forwardResolveFn: defaultForwardResolvers,
			forwardNowFn:     time.Now,
			rateLimitEnabled: true,
			rateLimitRPS:     defaultRateLimitRPS,
			rateLimitBurst:   defaultRateLimitBurst,
			maxRequestBytes:  defaultMaxRequestBytes,
			maxQNameBytes:    defaultMaxQNameBytes,
			rateLimiter:      newSourceRateLimiter(defaultRateLimitRPS, defaultRateLimitBurst, defaultBucketIdleTTL),
			logSampler:       newSampledLogger(defaultLogSampleInterval),
		}
	})
	return instance
}

func (r *Resolver) Start() error {
	r.mu.Lock()
	if r.started {
		r.mu.Unlock()
		return nil
	}

	r.compatOn = strings.EqualFold(strings.TrimSpace(os.Getenv("EDGELET_DNS_COMPAT_ALIASES")), "true")
	r.reconcileEvery = reconcileIntervalFromEnv()
	r.applyGuardrailConfigFromEnvLocked()
	if strings.TrimSpace(r.snapshotPath) == "" {
		r.snapshotPath = defaultSnapshotPath
	}
	if r.snapshotEvery <= 0 {
		r.snapshotEvery = snapshotDefaultEvery
	}
	r.stopCh = make(chan struct{})
	r.snapshotTriggerCh = make(chan struct{}, 1)
	r.started = true
	r.mu.Unlock()

	if err := r.restoreSnapshot(); err != nil {
		logging.LogWarn(moduleName, fmt.Sprintf("dns snapshot restore skipped: %v", err))
	}

	r.wg.Add(1)
	go r.bindLoop()
	r.wg.Add(1)
	go r.reconcileLoop()
	r.wg.Add(1)
	go r.snapshotLoop()

	logging.LogInfo(moduleName, "Embedded DNS started")
	return nil
}

func (r *Resolver) Stop() error {
	r.mu.Lock()
	if !r.started {
		r.mu.Unlock()
		return nil
	}
	close(r.stopCh)
	for _, ss := range r.servers {
		if ss == nil {
			continue
		}
		if ss.udp != nil {
			_ = ss.udp.Shutdown()
		}
		if ss.tcp != nil {
			_ = ss.tcp.Shutdown()
		}
	}
	r.started = false
	r.servers = make(map[Scope]*serverSet)
	r.mu.Unlock()

	r.wg.Wait()
	logging.LogInfo(moduleName, "Embedded DNS stopped")
	return nil
}

func (r *Resolver) Snapshot() StatsSnapshot {
	r.mu.RLock()
	defer r.mu.RUnlock()

	snapshot := StatsSnapshot{
		Started:                 r.started,
		CompatAliasesEnabled:    r.compatOn,
		ReconcileIntervalSec:    int64(r.reconcileEvery / time.Second),
		RateLimitEnabled:        r.rateLimitEnabled,
		RateLimitRPS:            r.rateLimitRPS,
		RateLimitBurst:          r.rateLimitBurst,
		MaxRequestBytes:         r.maxRequestBytes,
		MaxQNameBytes:           r.maxQNameBytes,
		ForwardingDegraded:      r.forwardDegraded,
		ForwardLastSuccessUnix:  r.forwardLastSuccessUnix,
		ForwardLastFailureUnix:  r.forwardLastFailureUnix,
		QueriesTotal:            r.queriesTotal.Load(),
		SuccessTotal:            r.successTotal.Load(),
		NXDomainTotal:           r.nxdomainTotal.Load(),
		ServFailTotal:           r.servfailTotal.Load(),
		PolicyDeniedTotal:       r.policyDeniedTotal.Load(),
		InactiveTotal:           r.inactiveTotal.Load(),
		ForwardedTotal:          r.forwardedTotal.Load(),
		ForwardErrTotal:         r.forwardErrTotal.Load(),
		ReconcileRunsTotal:      r.reconcileRuns.Load(),
		ReconcileAddTotal:       r.reconcileAdds.Load(),
		ReconcileUpdateTotal:    r.reconcileUpdates.Load(),
		ReconcileRemoveTotal:    r.reconcileRemoves.Load(),
		ReconcileErrorTotal:     r.reconcileErrors.Load(),
		ForwardBackoffSkipTotal: r.forwardBackoffSkips.Load(),
		RateLimitedTotal:        r.rateLimitedTotal.Load(),
		RejectedTotal:           r.rejectedTotal.Load(),
	}
	snapshot.ForwardTotalUpstream, snapshot.ForwardHealthyUpstream = r.forwardHealthCountsLocked()
	if ss, ok := r.servers[ScopeManaged]; ok && ss != nil {
		snapshot.ScopeManaged = ScopeListenerState{
			Listening: true,
			Address:   ss.addr,
		}
	}
	if ss, ok := r.servers[ScopeLocal]; ok && ss != nil {
		snapshot.ScopeLocal = ScopeListenerState{
			Listening: true,
			Address:   ss.addr,
		}
	}
	return snapshot
}

func (r *Resolver) SetForwardResolverProvider(provider func() ([]string, error)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if provider == nil {
		r.forwardResolveFn = defaultForwardResolvers
		return
	}
	r.forwardResolveFn = provider
}

func (r *Resolver) SetRuntimeSnapshotProvider(provider RuntimeSnapshotProvider) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reconcileProvider = provider
}

func (r *Resolver) bindLoop() {
	defer r.wg.Done()

	ticker := time.NewTicker(bindRetryInterval)
	defer ticker.Stop()

	r.tryBindMissingServers()

	for {
		select {
		case <-r.stopCh:
			return
		case <-ticker.C:
			r.tryBindMissingServers()
		}
	}
}

func (r *Resolver) tryBindMissingServers() {
	for _, scope := range []Scope{ScopeManaged, ScopeLocal} {
		r.mu.RLock()
		_, exists := r.servers[scope]
		enabled := r.scopeEnabled[scope]
		r.mu.RUnlock()
		if !enabled {
			if exists {
				r.stopScopeServer(scope)
			}
			continue
		}
		if exists {
			continue
		}
		addr, err := scopeBindAddr(scope)
		if err != nil {
			logging.LogWarn(moduleName, fmt.Sprintf("DNS bind skipped for scope %s: failed to resolve bind address: %v", scope, err))
			continue
		}
		startFn := r.startScopeServerFn
		if startFn == nil {
			startFn = r.startScopeServer
		}
		if err := startFn(scope, addr); err != nil {
			logging.LogWarn(moduleName, fmt.Sprintf("DNS bind retry failed for scope %s addr=%s enabled=%t: %v", scope, addr, enabled, err))
		}
	}
}

func (r *Resolver) stopScopeServer(scope Scope) {
	r.mu.Lock()
	ss, ok := r.servers[scope]
	if ok {
		delete(r.servers, scope)
	}
	r.mu.Unlock()
	if !ok || ss == nil {
		return
	}
	if ss.udp != nil {
		_ = ss.udp.Shutdown()
	}
	if ss.tcp != nil {
		_ = ss.tcp.Shutdown()
	}
	logging.LogInfo(moduleName, fmt.Sprintf("DNS scope %s listener disabled", scope))
}

func (r *Resolver) updateScopePolicy(records []WorkloadRecord) {
	managedCount := 0
	localCount := 0
	for _, rec := range records {
		if rec.HostNetwork {
			continue
		}
		switch normalizeScope(rec.Scope) {
		case ScopeLocal:
			localCount++
		default:
			managedCount++
		}
	}
	// Single-bridge mode: local/managed remain metadata scopes, but listener binds
	// only once on the canonical managed bridge gateway.
	_ = config.GetInstance().WatchdogEnabled
	localEnabled := false
	managedEnabled := (managedCount + localCount) > 0

	r.mu.Lock()
	if r.scopeEnabled == nil {
		r.scopeEnabled = map[Scope]bool{
			ScopeManaged: false,
			ScopeLocal:   false,
		}
	}
	r.scopeEnabled[ScopeManaged] = managedEnabled
	r.scopeEnabled[ScopeLocal] = localEnabled
	r.mu.Unlock()
}

func (r *Resolver) isScopeEnabled(scope Scope) bool {
	scope = normalizeScope(scope)
	r.mu.RLock()
	enabled := r.scopeEnabled[scope]
	r.mu.RUnlock()
	return enabled
}

func (r *Resolver) filterRecordsByEnabledScopes(records []WorkloadRecord) []WorkloadRecord {
	filtered := make([]WorkloadRecord, 0, len(records))
	for _, rec := range records {
		if r.isScopeEnabled(rec.Scope) {
			filtered = append(filtered, rec)
		}
	}
	return filtered
}

func (r *Resolver) startScopeServer(scope Scope, addr string) error {
	handler := dns.HandlerFunc(func(w dns.ResponseWriter, req *dns.Msg) {
		r.handleDNSQuery(scope, w, req)
	})

	udpConn, err := net.ListenPacket("udp", addr)
	if err != nil {
		return err
	}
	tcpLn, err := net.Listen("tcp", addr)
	if err != nil {
		_ = udpConn.Close()
		return err
	}

	udp := &dns.Server{
		PacketConn: udpConn,
		Handler:    handler,
	}

	tcp := &dns.Server{
		Listener: tcpLn,
		Handler:  handler,
	}

	r.wg.Add(2)
	go func() {
		defer r.wg.Done()
		if err := udp.ActivateAndServe(); err != nil && !strings.Contains(strings.ToLower(err.Error()), "closed network connection") {
			logging.LogWarn(moduleName, fmt.Sprintf("DNS UDP server for scope %s stopped: %v", scope, err))
		}
	}()
	go func() {
		defer r.wg.Done()
		if err := tcp.ActivateAndServe(); err != nil && !strings.Contains(strings.ToLower(err.Error()), "closed network connection") {
			logging.LogWarn(moduleName, fmt.Sprintf("DNS TCP server for scope %s stopped: %v", scope, err))
		}
	}()

	r.mu.Lock()
	r.servers[scope] = &serverSet{
		addr: addr,
		udp:  udp,
		tcp:  tcp,
	}
	r.mu.Unlock()

	logging.LogInfo(moduleName, fmt.Sprintf("DNS scope %s listening on %s", scope, addr))
	return nil
}

func (r *Resolver) handleDNSQuery(scope Scope, w dns.ResponseWriter, req *dns.Msg) {
	r.ensureGuardrailsInitialized()
	r.queriesTotal.Add(1)

	resp := new(dns.Msg)
	resp.SetReply(req)
	resp.Authoritative = true

	if r.maxRequestBytes > 0 && req.Len() > r.maxRequestBytes {
		resp.Rcode = dns.RcodeFormatError
		r.rejectedTotal.Add(1)
		r.warnSampled("reject.max_request_bytes", fmt.Sprintf("DNS request rejected reason=max_request_bytes size=%d max=%d", req.Len(), r.maxRequestBytes))
		_ = w.WriteMsg(resp)
		return
	}

	if len(req.Question) != 1 {
		resp.Rcode = dns.RcodeFormatError
		r.rejectedTotal.Add(1)
		r.warnSampled("reject.question_count", fmt.Sprintf("DNS request rejected reason=question_count count=%d", len(req.Question)))
		_ = w.WriteMsg(resp)
		return
	}

	source := sourceKeyFromAddr(w.RemoteAddr())
	if r.rateLimitEnabled && !r.rateLimiter.Allow(source, time.Now()) {
		resp.Rcode = dns.RcodeRefused
		r.rateLimitedTotal.Add(1)
		r.warnSampled("rate_limit."+source, fmt.Sprintf("DNS request rate-limited source=%s", source))
		_ = w.WriteMsg(resp)
		return
	}

	q := req.Question[0]
	if q.Qclass != dns.ClassINET {
		resp.Rcode = dns.RcodeRefused
		r.rejectedTotal.Add(1)
		r.warnSampled("reject.qclass", fmt.Sprintf("DNS request rejected reason=unsupported_qclass class=%d", q.Qclass))
		_ = w.WriteMsg(resp)
		return
	}

	name := normalizeName(q.Name)
	if name == "" || (r.maxQNameBytes > 0 && len(name) > r.maxQNameBytes) {
		resp.Rcode = dns.RcodeFormatError
		r.rejectedTotal.Add(1)
		r.warnSampled("reject.qname", fmt.Sprintf("DNS request rejected reason=invalid_qname len=%d", len(name)))
		_ = w.WriteMsg(resp)
		return
	}

	switch q.Qtype {
	case dns.TypeA, dns.TypeAAAA, dns.TypeANY:
	default:
		resp.Rcode = dns.RcodeNotImplemented
		r.rejectedTotal.Add(1)
		r.warnSampled("reject.qtype", fmt.Sprintf("DNS request rejected reason=unsupported_qtype qtype=%d", q.Qtype))
		_ = w.WriteMsg(resp)
		return
	}

	knownInScope, answers, knownOtherScope := r.resolveInternal(scope, name, q.Qtype)
	if knownInScope {
		if len(answers) > 0 {
			resp.Answer = append(resp.Answer, answers...)
			r.successTotal.Add(1)
		} else {
			r.inactiveTotal.Add(1)
		}
		_ = w.WriteMsg(resp)
		return
	}

	if knownOtherScope || isInternalZoneName(name) {
		resp.Rcode = dns.RcodeNameError
		if knownOtherScope {
			r.policyDeniedTotal.Add(1)
		}
		r.nxdomainTotal.Add(1)
		_ = w.WriteMsg(resp)
		return
	}

	forwarded, err := r.forwardExternal(req)
	if err != nil {
		resp.Rcode = dns.RcodeServerFailure
		r.servfailTotal.Add(1)
		r.forwardErrTotal.Add(1)
		r.warnSampled("forward.fail", fmt.Sprintf("DNS forward failed for %s: %v", name, err))
		_ = w.WriteMsg(resp)
		return
	}
	r.forwardedTotal.Add(1)
	_ = w.WriteMsg(forwarded)
}

func (r *Resolver) resolveInternal(scope Scope, name string, qtype uint16) (bool, []dns.RR, bool) {
	hostIP := r.hostAdvertiseIP()
	if isHostReservedName(name, r.compatOn) {
		return true, rrFromIP(name, hostIP, qtype), false
	}

	r.mu.RLock()
	defer r.mu.RUnlock()

	ids := r.index[scope][name]
	// Single-listener mode: when only managed listener is active, allow managed
	// queries to resolve local-scoped records (metadata-only local/managed split).
	if len(ids) == 0 && scope == ScopeManaged && !r.scopeEnabled[ScopeLocal] {
		ids = r.index[ScopeLocal][name]
	}
	if len(ids) == 0 {
		otherScope := ScopeManaged
		if scope == ScopeManaged {
			otherScope = ScopeLocal
		}
		_, knownOther := r.index[otherScope][name]
		return false, nil, knownOther
	}

	known := true
	candidates := make([]*WorkloadRecord, 0, len(ids))
	for id := range ids {
		if wl, ok := r.workloads[id]; ok {
			candidates = append(candidates, wl)
		}
	}

	active := make([]*WorkloadRecord, 0, len(candidates))
	for _, wl := range candidates {
		if !wl.Active {
			continue
		}
		if strings.TrimSpace(wl.IP) == "" {
			continue
		}
		active = append(active, wl)
	}

	if len(active) == 0 {
		return known, nil, false
	}

	rrs := make([]dns.RR, 0, len(active))
	if isReservedRoleName(name) {
		best := pickReservedTarget(active)
		if best == nil {
			return known, nil, false
		}
		rrs = append(rrs, rrFromIP(name, best.IP, qtype)...)
		return known, rrs, false
	}

	seen := make(map[string]struct{})
	for _, wl := range active {
		if _, ok := seen[wl.IP]; ok {
			continue
		}
		seen[wl.IP] = struct{}{}
		rrs = append(rrs, rrFromIP(name, wl.IP, qtype)...)
	}
	return known, rrs, false
}

func pickReservedTarget(active []*WorkloadRecord) *WorkloadRecord {
	if len(active) == 0 {
		return nil
	}
	slices.SortStableFunc(active, func(a, b *WorkloadRecord) int {
		if a.StartedAt != b.StartedAt {
			return cmp.Compare(b.StartedAt, a.StartedAt)
		}
		return cmp.Compare(a.UUID, b.UUID)
	})
	return active[0]
}

func (r *Resolver) UpsertWorkload(rec WorkloadRecord) {
	rec.UUID = strings.TrimSpace(rec.UUID)
	if rec.UUID == "" {
		return
	}
	rec.Application = strings.TrimSpace(rec.Application)
	rec.Name = strings.TrimSpace(rec.Name)
	rec.IP = strings.TrimSpace(rec.IP)
	rec.Scope = normalizeScope(rec.Scope)
	if rec.HostNetwork && rec.IP == "" {
		rec.IP = r.hostAdvertiseIP()
	}

	newRec := rec

	r.mu.Lock()
	changed := false
	if old, ok := r.workloads[newRec.UUID]; ok {
		if workloadEqual(*old, newRec) {
			r.mu.Unlock()
			return
		}
		r.deindexLocked(old)
		changed = true
	} else {
		changed = true
	}
	r.workloads[newRec.UUID] = &newRec
	r.indexLocked(&newRec)
	if changed {
		r.markSnapshotDirtyLocked()
	}
	r.mu.Unlock()
	r.triggerSnapshotPersist()
}

func (r *Resolver) SetWorkloadActive(uuid string, active bool, startedAt int64) {
	uuid = strings.TrimSpace(uuid)
	if uuid == "" {
		return
	}
	r.mu.Lock()
	rec, ok := r.workloads[uuid]
	if !ok {
		r.mu.Unlock()
		return
	}
	changed := rec.Active != active
	rec.Active = active
	if startedAt > 0 && rec.StartedAt != startedAt {
		rec.StartedAt = startedAt
		changed = true
	}
	if changed {
		r.markSnapshotDirtyLocked()
	}
	r.mu.Unlock()
	if changed {
		r.triggerSnapshotPersist()
	}
}

func (r *Resolver) RemoveWorkload(uuid string) {
	uuid = strings.TrimSpace(uuid)
	if uuid == "" {
		return
	}
	r.mu.Lock()
	changed := false
	if old, ok := r.workloads[uuid]; ok {
		r.deindexLocked(old)
		delete(r.workloads, uuid)
		changed = true
	}
	if changed {
		r.markSnapshotDirtyLocked()
	}
	r.mu.Unlock()
	if changed {
		r.triggerSnapshotPersist()
	}
}

func (r *Resolver) indexLocked(rec *WorkloadRecord) {
	names := aliasesForWorkload(*rec, r.compatOn)
	scopeMap := r.index[rec.Scope]
	if scopeMap == nil {
		scopeMap = make(map[string]map[string]struct{})
		r.index[rec.Scope] = scopeMap
	}
	for _, n := range names {
		if _, ok := scopeMap[n]; !ok {
			scopeMap[n] = make(map[string]struct{})
		}
		scopeMap[n][rec.UUID] = struct{}{}
	}
}

func (r *Resolver) deindexLocked(rec *WorkloadRecord) {
	names := aliasesForWorkload(*rec, r.compatOn)
	scopeMap := r.index[rec.Scope]
	if scopeMap == nil {
		return
	}
	for _, n := range names {
		ids, ok := scopeMap[n]
		if !ok {
			continue
		}
		delete(ids, rec.UUID)
		if len(ids) == 0 {
			delete(scopeMap, n)
		}
	}
}

func aliasesForWorkload(rec WorkloadRecord, compat bool) []string {
	out := make([]string, 0, 8)
	add := func(name string) {
		name = normalizeName(name)
		if name != "" {
			out = append(out, name)
		}
	}

	if rec.Application != "" && rec.Name != "" {
		short := rec.Application + "." + rec.Name
		add(short)
		add(short + "." + defaultZoneName)
	}
	if rec.UUID != "" {
		short := "iofog_" + rec.UUID
		add(short)
		add(short + "." + defaultZoneName)
	}
	if rec.IsController {
		add(controlPlaneAliasEdgeletController)
		add(controlPlaneAliasEdgeletController + "." + defaultZoneName)
		if nsAlias := ControlPlaneBridgeAliasNamespaceController(rec.Application); nsAlias != "" {
			add(nsAlias)
			add(nsAlias + "." + defaultZoneName)
		}
	}
	if rec.Scope == ScopeManaged && rec.IsRouter {
		add(reservedRouterName)
	}
	if rec.Scope == ScopeManaged && rec.IsNats {
		add(reservedNatsName)
	}
	if rec.Scope == ScopeManaged {
		add(reservedAgentName)
		if compat {
			add(compatDockerHostName)
			add(compatContainerHost)
		}
	}
	return out
}

func rrFromIP(name, ip string, qtype uint16) []dns.RR {
	parsed, err := netip.ParseAddr(strings.TrimSpace(ip))
	if err != nil {
		return nil
	}
	fqdn := dns.Fqdn(name)
	switch qtype {
	case dns.TypeAAAA:
		if !parsed.Is6() {
			return nil
		}
		return []dns.RR{
			&dns.AAAA{
				Hdr:  dns.RR_Header{Name: fqdn, Rrtype: dns.TypeAAAA, Class: dns.ClassINET, Ttl: 5},
				AAAA: net.IP(parsed.AsSlice()),
			},
		}
	case dns.TypeA:
		if !parsed.Is4() {
			return nil
		}
		return []dns.RR{
			&dns.A{
				Hdr: dns.RR_Header{Name: fqdn, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 5},
				A:   net.IP(parsed.AsSlice()),
			},
		}
	default:
		rrs := make([]dns.RR, 0, 2)
		if parsed.Is4() {
			rrs = append(rrs, &dns.A{
				Hdr: dns.RR_Header{Name: fqdn, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 5},
				A:   net.IP(parsed.AsSlice()),
			})
		} else if parsed.Is6() {
			rrs = append(rrs, &dns.AAAA{
				Hdr:  dns.RR_Header{Name: fqdn, Rrtype: dns.TypeAAAA, Class: dns.ClassINET, Ttl: 5},
				AAAA: net.IP(parsed.AsSlice()),
			})
		}
		return rrs
	}
}

func normalizeName(name string) string {
	name = strings.TrimSpace(strings.ToLower(name))
	name = strings.TrimSuffix(name, ".")
	return name
}

func isInternalZoneName(name string) bool {
	return name == defaultZoneName || strings.HasSuffix(name, "."+defaultZoneName)
}

func isReservedRoleName(name string) bool {
	return name == reservedRouterName || name == reservedNatsName
}

func isHostReservedName(name string, compat bool) bool {
	if name == reservedAgentName {
		return true
	}
	if compat && (name == compatDockerHostName || name == compatContainerHost) {
		return true
	}
	return false
}

func (r *Resolver) hostAdvertiseIP() string {
	if ip, err := GatewayIPForScope(ScopeManaged); err == nil && strings.TrimSpace(ip) != "" {
		return ip
	}
	if ip := strings.TrimSpace(network.GetInstance().GetCurrentIPAddress()); ip != "" {
		return ip
	}
	return ""
}

func GatewayIPForScope(scope Scope) (string, error) {
	_ = scope
	cidr := constants.EdgeletBridgeCIDR
	pfx, err := netip.ParsePrefix(cidr)
	if err != nil {
		return "", err
	}
	addr := pfx.Addr()
	if !addr.Is4() {
		return "", fmt.Errorf("only IPv4 bridge CIDR supported: %s", cidr)
	}
	b := addr.As4()
	b[3]++
	return netip.AddrFrom4(b).String(), nil
}

func scopeBindAddr(scope Scope) (string, error) {
	ip, err := GatewayIPForScope(scope)
	if err != nil {
		return "", err
	}
	return net.JoinHostPort(ip, "53"), nil
}

func normalizeScope(scope Scope) Scope {
	if scope == ScopeLocal {
		return ScopeLocal
	}
	return ScopeManaged
}

func (r *Resolver) markSnapshotDirtyLocked() {
	r.snapshotRevision.Add(1)
}
