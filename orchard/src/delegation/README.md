# Delegation Circuit (ZKP 1)

## Inputs

- **nk** - nullifier key
- **ρ** ("rho"): The nullifier of the note that was spent to create this note
- **ψ** ("psi"): A pseudorandom field element derived from the note's random seed `rseed` and its nullifier domain separator rho
- **cm**: The note commitment, witnessed as an ECC point


## Nullifier Integrity

Purpose: Derive the standard Orchard nullifier deterministically from the note's secret components. Validate it against the one we created exclusion proof from.

```
derive nf_old = DeriveNullifier(nk, rho_old, psi_old, cm_old)
```

Where:  
- **nk**: The nullifier deriving key associated with the note.

- **ρ** ("rho"): The nullifier of the note that was spent to create this note. It is a Pallas base field element that serves as a unique, per-note domain separator. rho ensures that even if two notes have identical contents, they will produce different nullifiers because they were created by spending different input notes. rho provides deterministic, structural uniqueness — it chains each note to its creation context. A single tx can create multiple output notes from the same input; all those outputs share the same rho. If nullifier derivation only used rho (no psi), outputs from the same input could collide.

- **ψ** ("psi"): A pseudorandom field element derived from the note's random seed `rseed` and its nullifier domain separator rho. It adds randomness to the nullifier so that even if two notes share the same rho and nk, they produce different nullifiers. We provide it as a witness instead of deriving in-circuit since derivation would require an expensive Blake2b. psi provides randomized uniqueness — it is derived from `rseed` which is freshly random per note. Even if multiple outputs are derived from the same note, different `rseed` values produce different psi values. But if uniqueness relied only on psi (i.e. only randomness), a faulty RNG would cause nullifier collisions. Together with rho, they cover each other's weaknesses. Additionally, there is a structural reason: if we only used psi, there would be an implicit chain where each note's identity is linked to the note that was spent to create it. The randomized psi breaks the chain, unblocking a requirement used in Orchard's security proof.

- **cm**: The note commitment, witnessed as an ECC point (the form `DeriveNullifier` expects). Converted from `NoteCommitment` to a Pallas affine point in-circuit.

**Function:** `DeriveNullifier`

**Type:**  
```
DeriveNullifier: 𝔽_qP × 𝔽_qP × 𝔽_qP × ℙ → 𝔽_qP
```

**Defined as:**  
```
DeriveNullifier_nk(ρ, ψ, cm) = ExtractP(
    [ (PRF_nf_Orchard_nk(ρ) + ψ) mod q_P ] * 𝒦_Orchard + cm
)
```

- `ExtractP` denotes extracting the base field element from the resulting group element.  
- `𝒦_Orchard` is a fixed generator. Input to the `EccChip`.
- `PRF_nf_Orchard_nk(ρ)` is the nullifier pseudorandom function as defined in the Orchard protocol. Uses Poseidon hash for PRF.

**Constructions**:
- `Poseidon`: used as a PRF function.
- `Sinsemilla`: provides infrastructure for the lookup tables of the ECC chip.


- **Why do we take PRF of rho?**
   * The primary reason is unlinkability. Rho is the nullifier of the note that was spend to create this note. In standard Orchard, nullifiers are published onchain. The PRF destroys the link.
- **Why not expose nf_old publicly?**
   * In standard Orchard, the nullifier is published to prevent double-spending. In this delegation circuit, nf_old is not directly exposed as a public input. Instead, it is checked against the exclusion interval and a domain nullifier is published instead. The standard nullifier stays hidden.


## TODO

- Better understand underlying Poseidong and AddChip constructions. Specifically, how they select columns.
