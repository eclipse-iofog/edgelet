package dnsresolver

import (
	"net"
	"strings"
	"testing"
	"time"

	"github.com/eclipse-iofog/edgelet/internal/config"
	"github.com/miekg/dns"
)

func newTestResolver() *Resolver {
	return &Resolver{
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
	}
}

func TestHostAdvertiseIPUsesBridgeGateway(t *testing.T) {
	got := newTestResolver().hostAdvertiseIP()
	if got != "172.18.0.1" {
		t.Fatalf("hostAdvertiseIP() = %q, want 172.18.0.1", got)
	}
}

func TestNormalizeName(t *testing.T) {
	got := normalizeName("APP.Service.SVC.BRIDGE.LOCAL.")
	if got != "app.service.svc.bridge.local" {
		t.Fatalf("unexpected normalized name: %s", got)
	}
}

func TestReservedTieBreakNewestThenUUID(t *testing.T) {
	r := newTestResolver()
	r.UpsertWorkload(WorkloadRecord{
		UUID:        "b",
		Application: "core",
		Name:        "router-b",
		Scope:       ScopeManaged,
		IP:          "10.1.1.2",
		IsRouter:    true,
		Active:      true,
		StartedAt:   100,
	})
	r.UpsertWorkload(WorkloadRecord{
		UUID:        "a",
		Application: "core",
		Name:        "router-a",
		Scope:       ScopeManaged,
		IP:          "10.1.1.1",
		IsRouter:    true,
		Active:      true,
		StartedAt:   100,
	})
	r.UpsertWorkload(WorkloadRecord{
		UUID:        "z",
		Application: "core",
		Name:        "router-z",
		Scope:       ScopeManaged,
		IP:          "10.1.1.9",
		IsRouter:    true,
		Active:      true,
		StartedAt:   200,
	})

	known, answers, denied := r.resolveInternal(ScopeManaged, reservedRouterName, 1)
	if !known || denied {
		t.Fatal("expected known reserved name without policy deny")
	}
	if len(answers) != 1 {
		t.Fatalf("expected 1 answer, got %d", len(answers))
	}
	if answers[0].String() == "" || answers[0].Header().Name == "" {
		t.Fatal("expected valid RR for reserved router name")
	}
	// Newest startedAt should win (10.1.1.9).
	if got := answers[0].String(); !strings.Contains(got, "10.1.1.9") {
		t.Fatalf("expected newest router IP, got RR: %s", got)
	}
}

func TestKnownInactiveReturnsNoData(t *testing.T) {
	r := newTestResolver()
	r.UpsertWorkload(WorkloadRecord{
		UUID:        "ms-1",
		Application: "app",
		Name:        "svc",
		Scope:       ScopeManaged,
		IP:          "10.2.2.2",
		Active:      false,
	})

	known, answers, denied := r.resolveInternal(ScopeManaged, "app.svc", 1)
	if !known || denied {
		t.Fatal("expected known name in scope")
	}
	if len(answers) != 0 {
		t.Fatal("expected NODATA style empty answer for inactive target")
	}
}

func TestScopeIsolationDeniesOtherScopeNames(t *testing.T) {
	r := newTestResolver()
	r.UpsertWorkload(WorkloadRecord{
		UUID:        "local-ms",
		Application: "edgelet",
		Name:        "svc",
		Scope:       ScopeLocal,
		IP:          "10.3.3.3",
		Active:      true,
	})
	r.scopeEnabled[ScopeLocal] = true

	known, _, denied := r.resolveInternal(ScopeManaged, "edgelet.svc", 1)
	if known {
		t.Fatal("managed scope should not treat local scope name as directly known")
	}
	if !denied {
		t.Fatal("expected policy denied indication for other-scope name")
	}
}

func TestSingleListenerManagedQueryResolvesLocalScopeRecord(t *testing.T) {
	r := newTestResolver()
	r.scopeEnabled[ScopeManaged] = true
	r.scopeEnabled[ScopeLocal] = false
	r.UpsertWorkload(WorkloadRecord{
		UUID:        "local-ms",
		Application: "edgelet",
		Name:        "svc",
		Scope:       ScopeLocal,
		IP:          "10.3.3.3",
		Active:      true,
	})

	known, answers, denied := r.resolveInternal(ScopeManaged, "edgelet.svc", dns.TypeA)
	if !known {
		t.Fatal("expected managed query to resolve local-scope record in single-listener mode")
	}
	if denied {
		t.Fatal("did not expect policy denied for local record in single-listener mode")
	}
	if len(answers) == 0 {
		t.Fatal("expected at least one answer")
	}
}

func TestCompatHostAliasesArePolicyGated(t *testing.T) {
	if !isHostReservedName(compatDockerHostName, true) {
		t.Fatal("compat host alias should be enabled when policy is on")
	}
	if isHostReservedName(compatDockerHostName, false) {
		t.Fatal("compat host alias should be disabled when policy is off")
	}
}

func TestPerSourceRateLimitDoesNotAffectOtherSources(t *testing.T) {
	r := newTestResolver()
	r.rateLimitEnabled = true
	r.rateLimitRPS = 1
	r.rateLimitBurst = 1
	r.rateLimiter = newSourceRateLimiter(1, 1, time.Minute)
	r.logSampler = newSampledLogger(time.Millisecond)
	r.maxRequestBytes = defaultMaxRequestBytes
	r.maxQNameBytes = defaultMaxQNameBytes

	r.UpsertWorkload(WorkloadRecord{
		UUID:        "ms1",
		Application: "app",
		Name:        "svc",
		Scope:       ScopeManaged,
		IP:          "10.9.9.9",
		Active:      true,
	})

	req := new(dns.Msg)
	req.SetQuestion("app.svc.", dns.TypeA)

	w1 := &testDNSWriter{remote: &net.UDPAddr{IP: net.ParseIP("10.0.0.1"), Port: 5300}}
	r.handleDNSQuery(ScopeManaged, w1, req)
	if w1.msg == nil || w1.msg.Rcode != dns.RcodeSuccess {
		t.Fatalf("first request should pass, got rcode=%v", w1.msg)
	}

	w2 := &testDNSWriter{remote: &net.UDPAddr{IP: net.ParseIP("10.0.0.1"), Port: 5301}}
	r.handleDNSQuery(ScopeManaged, w2, req)
	if w2.msg == nil || w2.msg.Rcode != dns.RcodeRefused {
		t.Fatal("second same-source request should be rate-limited")
	}

	w3 := &testDNSWriter{remote: &net.UDPAddr{IP: net.ParseIP("10.0.0.2"), Port: 5301}}
	r.handleDNSQuery(ScopeManaged, w3, req)
	if w3.msg == nil || w3.msg.Rcode != dns.RcodeSuccess {
		t.Fatal("different source should not be impacted by other source limit")
	}

	s := r.Snapshot()
	if s.RateLimitedTotal == 0 {
		t.Fatal("expected rate_limited counter increment")
	}
}

func TestSafetyRejectsOversizeAndUnsupportedType(t *testing.T) {
	r := newTestResolver()
	r.rateLimitEnabled = false
	r.rateLimiter = newSourceRateLimiter(1, 1, time.Minute)
	r.logSampler = newSampledLogger(time.Millisecond)
	r.maxRequestBytes = 12
	r.maxQNameBytes = defaultMaxQNameBytes

	req := new(dns.Msg)
	req.SetQuestion("oversized.example.org.", dns.TypeA)
	w := &testDNSWriter{}
	r.handleDNSQuery(ScopeManaged, w, req)
	if w.msg == nil || w.msg.Rcode != dns.RcodeFormatError {
		t.Fatal("oversize request should be format error")
	}

	r.maxRequestBytes = defaultMaxRequestBytes
	req2 := new(dns.Msg)
	req2.SetQuestion("example.org.", dns.TypeTXT)
	w2 := &testDNSWriter{}
	r.handleDNSQuery(ScopeManaged, w2, req2)
	if w2.msg == nil || w2.msg.Rcode != dns.RcodeNotImplemented {
		t.Fatal("unsupported qtype should be rejected with not implemented")
	}

	s := r.Snapshot()
	if s.RejectedTotal < 2 {
		t.Fatalf("expected rejected counter increment for safety checks, got=%d", s.RejectedTotal)
	}
}

func TestUpdateScopePolicy_ManagedEnabledOnRunningBridgeWorkload(t *testing.T) {
	r := newTestResolver()
	cfg := config.GetInstance()
	origWatchdog := cfg.WatchdogEnabled
	cfg.WatchdogEnabled = false
	t.Cleanup(func() { cfg.WatchdogEnabled = origWatchdog })

	r.updateScopePolicy([]WorkloadRecord{
		{
			UUID:        "managed-1",
			Scope:       ScopeManaged,
			Active:      true,
			HostNetwork: false,
		},
	})

	if !r.isScopeEnabled(ScopeManaged) {
		t.Fatal("expected managed scope enabled")
	}
	if r.isScopeEnabled(ScopeLocal) {
		t.Fatal("expected local scope disabled with no local bridge workloads")
	}
}

func TestUpdateScopePolicy_LocalEligibleWorkloadEnablesManagedListenerInSingleBridgeMode(t *testing.T) {
	r := newTestResolver()
	cfg := config.GetInstance()
	origWatchdog := cfg.WatchdogEnabled
	cfg.WatchdogEnabled = false
	t.Cleanup(func() { cfg.WatchdogEnabled = origWatchdog })

	r.updateScopePolicy([]WorkloadRecord{
		{
			UUID:        "local-eligible",
			Scope:       ScopeLocal,
			HostNetwork: false,
			Active:      false, // eligible by scope/network presence, not runtime active state
		},
	})

	if r.isScopeEnabled(ScopeLocal) {
		t.Fatal("expected local listener scope disabled in single-bridge mode")
	}
	if !r.isScopeEnabled(ScopeManaged) {
		t.Fatal("expected managed listener scope enabled when eligible local workload exists")
	}
}

func TestUpdateScopePolicy_LocalDisabledWhenWatchdogEnabled(t *testing.T) {
	r := newTestResolver()
	cfg := config.GetInstance()
	origWatchdog := cfg.WatchdogEnabled
	cfg.WatchdogEnabled = true
	t.Cleanup(func() { cfg.WatchdogEnabled = origWatchdog })

	r.updateScopePolicy([]WorkloadRecord{
		{
			UUID:        "local-1",
			Scope:       ScopeLocal,
			Active:      true,
			HostNetwork: false,
		},
	})

	if r.isScopeEnabled(ScopeLocal) {
		t.Fatal("expected local scope disabled when watchdog is enabled")
	}
	if !r.isScopeEnabled(ScopeManaged) {
		t.Fatal("expected managed scope enabled for local workload in single-bridge mode")
	}
}

func TestUpdateScopePolicy_ManagedUnaffectedByWatchdog(t *testing.T) {
	r := newTestResolver()
	cfg := config.GetInstance()
	origWatchdog := cfg.WatchdogEnabled
	cfg.WatchdogEnabled = true
	t.Cleanup(func() { cfg.WatchdogEnabled = origWatchdog })

	r.updateScopePolicy([]WorkloadRecord{
		{
			UUID:        "managed-eligible",
			Scope:       ScopeManaged,
			HostNetwork: false,
			Active:      false,
		},
	})

	if !r.isScopeEnabled(ScopeManaged) {
		t.Fatal("expected managed scope enabled regardless of watchdog setting")
	}
}

func TestTryBindMissingServers_DoesNotBindDisabledScope(t *testing.T) {
	r := newTestResolver()
	attempts := map[Scope]int{
		ScopeManaged: 0,
		ScopeLocal:   0,
	}
	r.startScopeServerFn = func(scope Scope, _ string) error {
		attempts[scope]++
		return nil
	}
	r.scopeEnabled[ScopeManaged] = false
	r.scopeEnabled[ScopeLocal] = false

	r.tryBindMissingServers()

	if attempts[ScopeManaged] != 0 || attempts[ScopeLocal] != 0 {
		t.Fatalf("expected no bind attempts for disabled scopes, got %+v", attempts)
	}
}

func TestTryBindMissingServers_StopsEnabledScopeWhenPolicyTurnsOff(t *testing.T) {
	r := newTestResolver()
	r.scopeEnabled[ScopeManaged] = false
	r.scopeEnabled[ScopeLocal] = false
	r.servers[ScopeManaged] = &serverSet{addr: "127.0.0.1:53"}

	r.tryBindMissingServers()

	if _, ok := r.servers[ScopeManaged]; ok {
		t.Fatal("expected disabled managed scope listener removed")
	}
}
