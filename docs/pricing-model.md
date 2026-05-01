# COGS Analysis: Daytona Infrastructure

This document models our Daytona sandbox costs as we scale, to understand the infrastructure component of COGS.

---

## Daytona Rates

| Resource | Rate |
|----------|------|
| vCPU | $0.0504 / hour |
| Memory | $0.0162 / GiB / hour |
| Storage | $0.000108 / GiB / hour (first 5 GiB free per sandbox) |

---

## Cost Per PR Review

The unit of work is one sandbox session per iteration. We assume **3 iterations per PR on average** (range: 1–5).

### Session cost by sandbox size (10 min runtime)

| Size | vCPU | RAM | Cost/session | Cost/PR (3 iter) |
|------|------|-----|-------------|-----------------|
| Small | 1 | 1 GiB | $0.011 | $0.033 |
| Medium | 2 | 4 GiB | $0.028 | $0.084 |
| Large | 4 | 8 GiB | $0.050 | $0.150 |

**Working assumption: medium sandbox (~$0.084/PR).** Small sandboxes likely can't handle real repos; large is the worst case.

---

## COGS at Scale

Monthly cost = PRs/month × cost/PR. Using medium sandbox as baseline.

### By monthly PR volume

| PRs / month | Cost/PR | Monthly COGS |
|-------------|---------|-------------|
| 1,000 | $0.084 | $84 |
| 5,000 | $0.084 | $420 |
| 10,000 | $0.084 | $840 |
| 50,000 | $0.084 | $4,200 |
| 100,000 | $0.084 | $8,400 |
| 500,000 | $0.084 | $42,000 |
| 1,000,000 | $0.084 | $84,000 |

### Sensitivity to iteration count

At 100,000 PRs/month:

| Avg iterations | Cost/session | Monthly COGS |
|----------------|-------------|-------------|
| 1 | $0.028 | $2,800 |
| 2 | $0.028 | $5,600 |
| 3 | $0.028 | $8,400 |
| 4 | $0.028 | $11,200 |
| 5 | $0.028 | $14,000 |

The iteration count is the biggest lever on our unit economics — a 1-iteration improvement in average (e.g. from 3 to 2) cuts COGS by 33%.

### Sandbox size sensitivity at 100,000 PRs/month

| Sandbox size | Cost/PR | Monthly COGS |
|---|---|---|
| Small (1 vCPU / 1 GiB) | $0.033 | $3,300 |
| Medium (2 vCPU / 4 GiB) | $0.084 | $8,400 |
| Large (4 vCPU / 8 GiB) | $0.150 | $15,000 |

---

## Key Assumptions to Validate

1. **Sandbox size needed in practice.** Medium is the working assumption but needs benchmarking on real repos. This is the biggest cost uncertainty.
2. **Average iterations per PR.** 3 is a guess — instrumentation will tell us the real number.
3. **Actual session runtime.** 10 min per iteration is an estimate. Longer tail on large repos would increase cost linearly.
4. **Volume discounts.** Daytona offers discounts at scale; above ~$10K/month it's worth negotiating a contract rate.
