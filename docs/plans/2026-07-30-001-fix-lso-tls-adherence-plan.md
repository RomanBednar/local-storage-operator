---
title: "Fix LSO TLS Adherence Implementation - Plan"
artifact_contract: ce-unified-plan/v1
artifact_readiness: implementation-ready
execution: code
product_contract_source: ce-plan-bootstrap
created: 2026-07-30
---

# Fix LSO TLS Adherence Implementation - Plan

## Goal Capsule

**Objective:** Fix the TLS adherence implementation in LSO PR #643 by adding the missing `ShouldHonorClusterTLSProfile` gate, replacing `os.Exit(0)` with graceful `cancel()` shutdown, removing dead code, and eliminating trivial wrapper functions.

**Product authority:** OpenShift enhancement [centralized-tls-config](https://github.com/openshift/enhancements/blob/master/enhancements/security/centralized-tls-config.md) and the reference implementation in [cluster-machine-approver](https://github.com/openshift/cluster-machine-approver/blob/1ae3f157b88c167a7dbe06c36d6e55a82f7fd4f0/pkg/tls/tls.go).

**Open blockers:** None — PR #643 is open and we have local fork access.

---

## Product Contract

### Problem Frame

LSO PR #643 (STOR-3054) adds TLS adherence to the Local Storage Operator but has four issues identified during cross-reference review against the enhancement document, CVO source code, and the cluster-machine-approver reference implementation.

### Requirements

- R1. The operator must check `ShouldHonorClusterTLSProfile` before applying the cluster TLS profile to its metrics endpoint — new-to-TLS components should only honor the cluster profile when `StrictAllComponents` is set
- R2. The operator must use graceful shutdown (`cancel()`) instead of `os.Exit(0)` when TLS configuration changes
- R3. Dead code (`ValidateMetricsAccess`) must be removed
- R4. Trivial pass-through wrapper functions must be replaced with direct calls to `controller-runtime-common`

### Key Decisions

**KD1. Use `ShouldHonorClusterTLSProfile` adherence gate (Governs R1)**

The enhancement explicitly states: *"Component implementors should use the ShouldHonorClusterTLSProfile helper function from library-go rather than checking the tlsAdherence field values directly."*

Currently LSO PR #643 applies TLS based on `tlsProfile.Ciphers != nil || tlsProfile.MinTLSVersion != ""` — a non-empty profile check. This means in `LegacyAdheringComponentsOnly` mode with Intermediate profile configured (the default), LSO applies it anyway. Per the enhancement, new-to-TLS components should only honor the cluster profile under `StrictAllComponents`.

Evidence: `controller-runtime-common/pkg/tls/` does NOT contain `ShouldHonorClusterTLSProfile` — confirmed by searching the entire module. The adherence gating is the operator's responsibility. `cluster-machine-approver` does it at [pkg/tls/tls.go:126-140](https://github.com/openshift/cluster-machine-approver/blob/1ae3f157b88c167a7dbe06c36d6e55a82f7fd4f0/pkg/tls/tls.go#L126-L140).

**KD2. Use `cancel()` for graceful shutdown, not `os.Exit(0)` (Governs R2)**

The `SecurityProfileWatcher`'s own source code documents the `cancel()` pattern as the intended usage — from [`controller-runtime-common/pkg/tls/controller.go:63-77`](https://github.com/openshift/controller-runtime-common/blob/fea68df23430b5c1e86599ac37d1de7e7fe55eb6/pkg/tls/controller.go#L63-L77):

```
ctx, cancel := context.WithCancel(ctrl.SetupSignalHandler())
defer cancel()
watcher := &SecurityProfileWatcher{
    OnProfileChange: func(ctx context.Context, old, new configv1.TLSProfileSpec) {
        cancel()
    },
}
```

And cluster-machine-approver follows this exactly at [main.go:210-229](https://github.com/openshift/cluster-machine-approver/blob/1ae3f157b88c167a7dbe06c36d6e55a82f7fd4f0/main.go#L210-L229).

`os.Exit(0)` terminates immediately — leader election lease is not released (other replicas wait for the full lease duration), in-flight reconciles are killed, and `defer` cleanup functions don't run.

**KD3. Remove dead `ValidateMetricsAccess` (Governs R3)**

`ValidateMetricsAccess` in `pkg/tls/tlsprofile.go` correctly uses `ShouldHonorClusterTLSProfile` but is never called from `main.go`. Rather than wiring it in (its validation-only approach doesn't fit the startup flow), we implement the adherence check inline in `main.go` matching the cluster-machine-approver pattern.

**KD4. Remove trivial wrappers (Governs R4)**

Three functions in `pkg/tls/tlsprofile.go` are one-line pass-throughs to `controller-runtime-common`:
- `GetTLSConfigFromProfile` → `crcommon.NewTLSConfigFromProfile`
- `GetAdherencePolicyForLogging` → `crcommon.FetchAPIServerTLSAdherencePolicy`
- `FetchAPIServerTLSProfile` → `crcommon.FetchAPIServerTLSProfile`

Encapsulation wrappers are justified when adding local invariants (transforming results, adding defaults, combining calls). These add none — `controller-runtime-common` IS the abstraction layer. `cluster-machine-approver` calls `crcommon` directly.

### Scope Boundaries

**In scope:** Fixing the four issues in `cmd/local-storage-operator/main.go` and `pkg/tls/` files added by PR #643.

**Out of scope:**
- Changing the existing `GetTLSProfileValues` function (used for kube-rbac-proxy operand TLS — different concern)
- Adding feature gate checks (enhancement explicitly says not needed)
- Modifying the `controller-runtime-common` dependency

---

## Planning Contract

### Implementation Units

### U1. Add ShouldHonorClusterTLSProfile adherence gate

**Goal:** Gate TLS profile application on the adherence policy, matching the cluster-machine-approver pattern.

**Requirements:** R1

**Dependencies:** None

**Files:**
- `cmd/local-storage-operator/main.go`

**Approach:** After fetching adherence and tlsProfile, check `libcrypto.ShouldHonorClusterTLSProfile(adherence)`. When true, apply the cluster profile. When false, apply the default Intermediate profile. This replaces the current `tlsProfile.Ciphers != nil || tlsProfile.MinTLSVersion != ""` check.

**Patterns to follow:** [cluster-machine-approver pkg/tls/tls.go:126-140](https://github.com/openshift/cluster-machine-approver/blob/1ae3f157b88c167a7dbe06c36d6e55a82f7fd4f0/pkg/tls/tls.go#L126-L140)

---

### U2. Replace os.Exit(0) with cancel() for graceful shutdown

**Goal:** Use controller-runtime's graceful shutdown mechanism instead of abrupt process termination.

**Requirements:** R2

**Dependencies:** None (independent of U1)

**Files:**
- `cmd/local-storage-operator/main.go`
- `pkg/tls/watcher.go`

**Approach:**
1. In `main.go`, create a cancellable context: `ctx, cancel := context.WithCancel(ctrl.SetupSignalHandler())`
2. Pass `cancel` to the watcher factory function
3. In `watcher.go`, change `NewSecurityProfileWatcher` to accept `cancel func()` and use it in both callbacks instead of `os.Exit(0)`
4. Pass `ctx` to `mgr.Start(ctx)`

**Patterns to follow:** [cluster-machine-approver main.go:155,210-229](https://github.com/openshift/cluster-machine-approver/blob/1ae3f157b88c167a7dbe06c36d6e55a82f7fd4f0/main.go#L155) and the `SecurityProfileWatcher` Example in [controller.go:63-77](https://github.com/openshift/controller-runtime-common/blob/fea68df23430b5c1e86599ac37d1de7e7fe55eb6/pkg/tls/controller.go#L63-L77)

---

### U3. Remove dead code and trivial wrappers

**Goal:** Remove `ValidateMetricsAccess` (dead code) and the three trivial pass-through wrappers. Update `main.go` to call `controller-runtime-common` directly.

**Requirements:** R3, R4

**Dependencies:** U1 (the wrappers are being called from main.go; U1 changes those call sites)

**Files:**
- `pkg/tls/tlsprofile.go`
- `cmd/local-storage-operator/main.go`

**Approach:** Remove `ValidateMetricsAccess`, `GetTLSConfigFromProfile`, `GetAdherencePolicyForLogging`, and `FetchAPIServerTLSProfile` from `pkg/tls/tlsprofile.go`. Replace their call sites in `main.go` with direct calls to `crcommon.FetchAPIServerTLSAdherencePolicy`, `crcommon.FetchAPIServerTLSProfile`, and `crcommon.NewTLSConfigFromProfile`. Add the `crcommon` import to `main.go`.

---

## Verification Contract

| Gate | Applies to | Criterion |
|------|-----------|-----------|
| Build | All units | `go build ./...` passes |
| Tests | All units | `go test ./...` passes |
| Vet | All units | `go vet ./...` passes |
| Dead code | U3 | Removed functions not referenced anywhere |

## Definition of Done

- All three units implemented and verified
- `go build ./...` passes
- `go test ./...` passes
- No `os.Exit(0)` in watcher callbacks
- `ShouldHonorClusterTLSProfile` is called before TLS profile application
- No trivial wrapper functions remain in `pkg/tls/tlsprofile.go`
- `ValidateMetricsAccess` removed
- Changes committed on a branch ready for PR

---

## Sources & Research

- Enhancement: [centralized-tls-config.md](https://github.com/openshift/enhancements/blob/master/enhancements/security/centralized-tls-config.md)
- Reference implementation: [cluster-machine-approver](https://github.com/openshift/cluster-machine-approver)
- Shared library: [controller-runtime-common/pkg/tls/](https://github.com/openshift/controller-runtime-common/tree/main/pkg/tls)
- CVO TLS injection: [cluster-version-operator lib/resourcebuilder/core.go](https://github.com/openshift/cluster-version-operator)
- CVO own endpoint TLS: [cluster-version-operator pkg/tls/tls.go](https://github.com/openshift/cluster-version-operator)
- LSO PR under review: [openshift/local-storage-operator#643](https://github.com/openshift/local-storage-operator/pull/643)
- GCP Filestore PR (parallel work): [openshift/gcp-filestore-csi-driver-operator#152](https://github.com/openshift/gcp-filestore-csi-driver-operator/pull/152)
- STOR-3054 Jira: [https://redhat.atlassian.net/browse/STOR-3054](https://redhat.atlassian.net/browse/STOR-3054)
