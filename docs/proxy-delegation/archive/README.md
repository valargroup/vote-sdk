# Proxy delegation planning archive

This is the working material behind [../PLAN.md](../PLAN.md). Where they disagree, PLAN.md wins. Keep these files because they hold detail the plan only references, such as the Vizor screen inventory (PD-1 to PD-13, DG-1 to DG-11) in `component-specs/vizor.md` and the full message, state and API specs in the other component specs. The component specs predate rev 6, which removed much of what they describe (key rotation and recovery, delegator recovery, deep links, the signed config switch and more; see `review/rev6-simplification.md`).

| Path | What it is |
|---|---|
| `research-dossiers/` | First-pass research: circuits, chain, client library, Vizor, specs, identity linking (ICNS and others) and private delegation literature. Each topic has a full dossier and a `*.summary.md` |
| `component-specs/` | Six component designs written against the first architecture baseline: circuits, chain, client, Vizor, identity, and spec and rollout |
| `plan-history/integrated-design-v1.md` | The reconciled design that went into adversarial review |
| `plan-history/PLAN-rev1.md`, `PLAN-rev2.md` | Earlier plan revisions. Rev 2's published pool totals were reverted in rev 3 |
| `review/findings-and-verdicts.json` | Seven-lens adversarial review findings, each with a skeptic verdict. The IDs (SND, PRV, LIV, IDN, OPS, CF, CMP) are the ones cited in the PLAN.md hole register |
| `review/rev6-simplification.md` | The rev 6 simplification pass: the guarantees it had to keep, every adopted cut with its trade-off and saving, the rejected ideas, and the code evidence it relied on |
| `review/scripts/` | Small simulations and checks the reviewers ran: helper load, domain uniqueness, key and tx size, and others |

Source code snapshots, third-party repos (ICNS, Keplr, Keybase) and research papers (Kite, Clark et al., Kulyk et al., Treasury) were read during research but are not copied here. The dossiers cite them by URL.
