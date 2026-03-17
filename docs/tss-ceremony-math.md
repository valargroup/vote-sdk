# TSS Ceremony — Full Pipeline Walkthrough

A single end-to-end worked example through every step of the TSS ceremony, from key generation to vote recovery. All arithmetic is verified by hand.

## Notation

All arithmetic is in $\mathbb{F}_{23}$ (scalars mod 23). We write $[s]$ to denote the public curve point $s \cdot G$. In the real system this is a Pallas curve point; here it's a shorthand that lets us verify everything in the scalar field directly.

Key properties of this notation:

$$[a] + [b] = [a + b] \qquad\qquad k \cdot [a] = [k \cdot a]$$

All scalar operations are mod 23.

### Parameters

| Parameter | Value |
|-----------|-------|
| Scalar field | $\mathbb{F}_{23}$ |
| Validators | $n = 5$ |
| Threshold | $t = 3$ |
| Secret key | $sk = 7$ |
| Public key | $pk = [sk] = [7]$ |

---

## Step 1 — Polynomial & Shares

The dealer picks a random degree-$(t{-}1) = 2$ polynomial with the secret as the constant term:

$$f(x) = \underbrace{7}_{a_0 = sk} + \underbrace{3}_{a_1} \cdot x + \underbrace{11}_{a_2} \cdot x^2 \pmod{23}$$

The coefficients $a_1 = 3$ and $a_2 = 11$ are random. The secret $sk = 7$ is $a_0 = f(0)$.

Evaluate at $x = 1, \ldots, 5$ to get each validator's share:

| Validator $i$ | $f(i)$ | $s_i \bmod 23$ |
|:---:|:---|:---:|
| 1 | $7 + 3(1) + 11(1) = 21$ | **21** |
| 2 | $7 + 3(2) + 11(4) = 57$ | **11** |
| 3 | $7 + 3(3) + 11(9) = 115$ | **0** |
| 4 | $7 + 3(4) + 11(16) = 195$ | **11** |
| 5 | $7 + 3(5) + 11(25) = 297$ | **21** |

Evaluation uses Horner's method: $f(x) = a_0 + x(a_1 + x \cdot a_2)$ — only $t{-}1$ multiplications.

**Code:** `crypto/shamir/shamir.go` — `Split()`, `evalPolynomial()`

```go
coeffs := make([]curvey.Scalar, t)
coeffs[0] = secret
for i := 1; i < t; i++ {
    coeffs[i] = new(curvey.ScalarPallas).Random(rand.Reader)
}

// Horner evaluation
result := coeffs[len(coeffs)-1]
for i := len(coeffs) - 2; i >= 0; i-- {
    result = result.Mul(xScalar).Add(coeffs[i])
}
```

---

## Step 2 — Feldman Commitments

The dealer publishes commitments to each polynomial coefficient:

$$C_j = [a_j] = a_j \cdot G \qquad \text{for } j = 0, \ldots, t{-}1$$

| $j$ | $a_j$ (secret) | $C_j$ (public) |
|:---:|:---:|:---:|
| 0 | 7 (= $sk$) | $[7]$ (= $pk$) |
| 1 | 3 | $[3]$ |
| 2 | 11 | $[11]$ |

Note: $C_0 = [a_0] = [sk] = pk$, so the public key is derivable from the commitments.

### Each Validator Verifies Their Share

Validator $i$ checks:

$$[s_i] \;\stackrel{?}{=}\; C_0 + i \cdot C_1 + i^2 \cdot C_2$$

This works because:

$$C_0 + i \cdot C_1 + i^2 \cdot C_2 = [a_0] + i \cdot [a_1] + i^2 \cdot [a_2] = [a_0 + a_1 i + a_2 i^2] = [f(i)] = [s_i]$$

The scalar arithmetic "lifts" into the group without revealing any $a_j$.

**Validator 1** ($s_1 = 21$):

$$\text{RHS} = [7] + 1 \cdot [3] + 1 \cdot [11] = [7 + 3 + 11] = [21] \qquad \text{LHS} = [21] \quad \checkmark$$

**Validator 2** ($s_2 = 11$):

$$\text{RHS} = [7] + 2 \cdot [3] + 4 \cdot [11] = [7 + 6 + 44] = [57 \bmod 23] = [11] \qquad \text{LHS} = [11] \quad \checkmark$$

**Validator 4** ($s_4 = 11$):

$$\text{RHS} = [7] + 4 \cdot [3] + 16 \cdot [11] = [7 + 12 + 176] = [195 \bmod 23] = [11] \qquad \text{LHS} = [11] \quad \checkmark$$

**Why this matters:** A malicious dealer who gives validator 2 a share from a *different* polynomial would be caught — the commitments lock in a single polynomial that all validators verify against.

**Code:** `crypto/shamir/feldman.go` — `FeldmanCommit()`, `VerifyFeldmanShare()`

```go
// Commit
commitments := make([]curvey.Point, len(coeffs))
for j, a := range coeffs {
    commitments[j] = G.Mul(a)
}

// Verify (Horner in the group)
result := commitments[len(commitments)-1]
for i := len(commitments) - 2; i >= 0; i-- {
    result = result.Mul(xScalar).Add(commitments[i])
}
actual := G.Mul(share)
return actual.Equal(result)
```

---

## Step 3 — Encrypt a Vote

A voter encrypts vote $v = 3$ under $pk = [7]$ with randomness $r = 4$:

$$C_1 = r \cdot G = [4]$$

$$C_2 = v \cdot G + r \cdot pk = [3] + 4 \cdot [7] = [3 + 28] = [31 \bmod 23] = [8]$$

Ciphertext: $(C_1, C_2) = ([4], \; [8])$.

**Code:** `crypto/elgamal/elgamal.go` — `encryptCore()`

```go
C1 := G.Mul(r)                            // r * G
C2 := G.Mul(vScalar).Add(pk.Point.Mul(r)) // v*G + r*pk
```

Multiple votes are tallied homomorphically: $(C_1^{(a)} + C_1^{(b)}, \; C_2^{(a)} + C_2^{(b)}) = \mathrm{Enc}(a + b)$.

---

## Step 4 — Partial Decryptions

Each validator computes a partial decryption by multiplying their share by $C_1$:

$$D_i = s_i \cdot C_1$$

Validators 1, 2, and 4 participate ($t = 3$ required):

$$D_1 = 21 \cdot [4] = [84 \bmod 23] = [84 - 3 \times 23] = [15]$$

$$D_2 = 11 \cdot [4] = [44 \bmod 23] = [44 - 1 \times 23] = [21]$$

$$D_4 = 11 \cdot [4] = [44 \bmod 23] = [21]$$

Nobody reconstructs $sk$ — each validator only uses their own share.

**Code:** `crypto/shamir/partial_decrypt.go` — `PartialDecrypt()`

```go
return C1.Mul(share), nil
```

---

## Step 5 — Chaum-Pedersen DLEQ Proof

Each validator must prove they used their real share $s_i$ (the same scalar behind their public verification key $VK_i$) to compute $D_i$. This prevents a malicious validator from submitting a fake partial decryption.

### The Statement

$$\text{Prove:} \quad \log_G(VK_i) = \log_{C_1}(D_i)$$

In words: "The same secret $s_i$ satisfies both $VK_i = s_i \cdot G$ and $D_i = s_i \cdot C_1$."

### Worked Example for Validator 2

**Public values:**

$$VK_2 = [s_2] = [11], \qquad C_1 = [4], \qquad D_2 = [21]$$

**Prover (validator 2, knows $s_2 = 11$):**

**1. Commit** — pick random $k = 9$, compute:

$$R_1 = k \cdot G = [9]$$

$$R_2 = k \cdot C_1 = 9 \cdot [4] = [36 \bmod 23] = [13]$$

The prover locks in $k$ before seeing the challenge. This prevents cheating — the prover cannot work backwards from the challenge to forge a response.

**2. Challenge** — Fiat-Shamir hash of all public values plus commitments:

$$e = H\!\left(\texttt{"svote-pd-dleq-v1"} \| G \| [11] \| [4] \| [21] \| [9] \| [13]\right) = 5 \quad \text{(toy hash)}$$

The challenge is unpredictable at commit time, which is what makes the proof sound.

**3. Response:**

$$z = k + e \cdot s_i = 9 + 5 \times 11 = 64 \bmod 23 = 18$$

This "mixes" the secret with the blinding scalar, weighted by the unpredictable challenge.

**4. Proof:** $(e, z) = (5, 18)$ — just two scalars, 64 bytes total.

### Verification

The verifier knows only the public values and the proof $(e, z)$. They recompute $R_1'$ and $R_2'$:

$$R_1' = z \cdot G - e \cdot VK_2 = [18] - 5 \cdot [11] = [18] - [55 \bmod 23] = [18] - [9] = [18 - 9] = [9]$$

$$R_2' = z \cdot C_1 - e \cdot D_2 = 18 \cdot [4] - 5 \cdot [21] = [72 \bmod 23] - [105 \bmod 23] = [3] - [13] = [3 - 13 \bmod 23] = [13]$$

Recompute the challenge:

$$e' = H\!\left(\texttt{"svote-pd-dleq-v1"} \| G \| [11] \| [4] \| [21] \| [9] \| [13]\right) = 5$$

$$e' = 5 = e \quad \checkmark$$

**Why it works:** The key cancellation:

$$z \cdot G - e \cdot VK_i = (k + e \cdot s_i) \cdot G - e \cdot s_i \cdot G = k \cdot G = R_1$$

The $e \cdot s_i \cdot G$ terms cancel exactly, recovering the original commitment. If the prover used a *different* scalar for $D_i$ than for $VK_i$, the cancellation would fail.

**Why commit-then-challenge:** Without the commit step, the prover sees $e$ first and can compute $R_1 = z \cdot G - e \cdot VK_i$ backwards for any $z$, faking the proof without knowing $s_i$. The commit forces $R_1$ to be fixed before $e$ is known.

**Code:** `crypto/elgamal/dleq.go` — `GeneratePartialDecryptDLEQ()`, `VerifyPartialDecryptDLEQ()`

```go
// Prover
VKi := G.Mul(share)
Di  := C1.Mul(share)
R1  := G.Mul(k)
R2  := C1.Mul(k)
e   := pdDleqChallenge(G, VKi, C1, Di, R1, R2)
z   := e.Mul(share).Add(k)

// Verifier
R1 := G.Mul(z).Sub(VKi.Mul(e))
R2 := C1.Mul(z).Sub(Di.Mul(e))
ePrime := pdDleqChallenge(G, VKi, C1, Di, R1, R2)
// Accept iff e == ePrime
```

---

## Step 6 — Lagrange Interpolation in the Exponent

Combine the $t = 3$ partial decryptions to recover $sk \cdot C_1$ — without anyone learning $sk$.

### Intuition

We have a degree-2 polynomial $f(x)$ but don't know its coefficients. We only know 3 points on the curve: $f(1) = 21$, $f(2) = 11$, $f(4) = 11$. A degree-2 polynomial is fully determined by 3 points — there's exactly one parabola through them. Lagrange interpolation finds that parabola and evaluates it at $x = 0$ to recover the secret.

The trick is to construct **basis polynomials** $L_j(x)$, each designed so that $L_j = 1$ at point $x_j$ and $L_j = 0$ at every other point:

$$L_1(x) = \frac{(x - 2)(x - 4)}{(1 - 2)(1 - 4)} \qquad \begin{cases} L_1(1) = 1 \\ L_1(2) = 0 \\ L_1(4) = 0 \end{cases}$$

$$L_2(x) = \frac{(x - 1)(x - 4)}{(2 - 1)(2 - 4)} \qquad \begin{cases} L_2(1) = 0 \\ L_2(2) = 1 \\ L_2(4) = 0 \end{cases}$$

$$L_4(x) = \frac{(x - 1)(x - 2)}{(4 - 1)(4 - 2)} \qquad \begin{cases} L_4(1) = 0 \\ L_4(2) = 0 \\ L_4(4) = 1 \end{cases}$$

Each one is a "selector" — it picks out exactly one point and ignores the rest. Blend them weighted by the known values:

$$f(x) = s_1 \cdot L_1(x) + s_2 \cdot L_2(x) + s_4 \cdot L_4(x)$$

To get the secret, evaluate at $x = 0$. The **Lagrange coefficients** $\lambda_j$ are just $L_j(0)$ — "how much weight does share $j$ get when reconstructing the value at $x = 0$?"

$$sk = f(0) = s_1 \cdot \underbrace{L_1(0)}_{\lambda_1} + s_2 \cdot \underbrace{L_2(0)}_{\lambda_2} + s_4 \cdot \underbrace{L_4(0)}_{\lambda_4}$$

### Why "in the exponent"?

In the tally we **don't want to reconstruct** $sk$ as a number — that would expose it. We want $sk \cdot C_1$ as a curve point.

The key insight: Lagrange interpolation is just a weighted sum, and scalar multiplication distributes over point addition:

$$\begin{aligned}
\lambda_1 \cdot D_1 + \lambda_2 \cdot D_2 + \lambda_4 \cdot D_4
&= \lambda_1 (s_1 \cdot C_1) + \lambda_2 (s_2 \cdot C_1) + \lambda_4 (s_4 \cdot C_1) \\[4pt]
&= \underbrace{(\lambda_1 s_1 + \lambda_2 s_2 + \lambda_4 s_4)}_{= \; sk \text{ by Lagrange}} \cdot C_1 \\[4pt]
&= sk \cdot C_1
\end{aligned}$$

The reconstruction of $sk$ happens **implicitly inside the scalar multiplier** of $C_1$. No one ever sees $sk$ as a number — it only exists as a "virtual" coefficient that was never materialized. The output is a curve point $sk \cdot C_1$, from which $sk$ cannot be extracted (discrete log is hard).

### Compute Lagrange Coefficients

For indices $\{1, 2, 4\}$ at target $x = 0$:

$$\lambda_j = \prod_{\substack{m \in S \\ m \neq j}} \frac{0 - x_m}{x_j - x_m}$$

---

**$\lambda_1$** &ensp; ($j = 1$, others $\{2, 4\}$):

$$\lambda_1 = \frac{(0 - 2)(0 - 4)}{(1 - 2)(1 - 4)} = \frac{(-2)(-4)}{(-1)(-3)} = \frac{8}{3}$$

> $3^{-1} \bmod 23 = 8$ &ensp; (since $3 \times 8 = 24 \equiv 1$)
>
> $\lambda_1 = 8 \times 8 = 64 \bmod 23 = \mathbf{18}$

---

**$\lambda_2$** &ensp; ($j = 2$, others $\{1, 4\}$):

$$\lambda_2 = \frac{(0 - 1)(0 - 4)}{(2 - 1)(2 - 4)} = \frac{4}{-2} = -2 \equiv \mathbf{21} \pmod{23}$$

---

**$\lambda_4$** &ensp; ($j = 4$, others $\{1, 2\}$):

$$\lambda_4 = \frac{(0 - 1)(0 - 2)}{(4 - 1)(4 - 2)} = \frac{2}{6}$$

> $6^{-1} \bmod 23 = 4$ &ensp; (since $6 \times 4 = 24 \equiv 1$)
>
> $\lambda_4 = 2 \times 4 = \mathbf{8}$

---

### Combine

$$\begin{aligned}
sk \cdot C_1 &= \lambda_1 \cdot D_1 + \lambda_2 \cdot D_2 + \lambda_4 \cdot D_4 \\[6pt]
             &= 18 \cdot [15] + 21 \cdot [21] + 8 \cdot [21] \\[4pt]
             &= [270 \bmod 23] + [441 \bmod 23] + [168 \bmod 23] \\[4pt]
             &= [17] + [4] + [7] \\[4pt]
             &= [17 + 4 + 7] \\[4pt]
             &= [28 \bmod 23] \\[4pt]
             &= [5]
\end{aligned}$$

**Verify independently:** $sk \cdot C_1 = 7 \cdot [4] = [28 \bmod 23] = [5]$ $\checkmark$

Nobody learned $sk = 7$. Each validator only contributed $s_i \cdot C_1$; the Lagrange coefficients combined them in the exponent.

**Code:** `crypto/shamir/partial_decrypt.go` — `CombinePartials()`

```go
lambdas, _ := LagrangeCoefficients(indices, 0)
result := new(curvey.PointPallas).Identity()
for i, p := range partials {
    result = result.Add(p.Di.Mul(lambdas[i]))
}
return result, nil
```

---

## Step 7 — Recover the Vote

Subtract the combined partial decryption from $C_2$:

$$v \cdot G = C_2 - sk \cdot C_1 = [8] - [5] = [3]$$

So $v \cdot G = [3]$, meaning $v = \mathbf{3}$.

**Proof of correctness:**

$$\begin{aligned}
C_2 - sk \cdot C_1 &= (v \cdot G + r \cdot pk) - sk \cdot (r \cdot G) \\
                    &= v \cdot G + r \cdot sk \cdot G - sk \cdot r \cdot G \\
                    &= v \cdot G \quad \checkmark
\end{aligned}$$

We encrypted $v = 3$ and recovered $v = 3$. $\checkmark$

In the real system, $v \cdot G$ is a curve point and the scalar $v$ is recovered using Baby-Step Giant-Step (BSGS) in $O(\sqrt{N})$ time. For the default bound $N = 2^{32}$, this requires a precomputed table of $2^{16} = 65\,536$ entries (~2.5 MB).

**Code:** `crypto/elgamal/bsgs.go` — `NewBSGSTable()`, `Solve()`

```go
// Precompute: store j·G → j for j = 0..√N
m := ceilSqrt(N)
current := new(curvey.PointPallas).Identity()
for j := uint64(0); j < m; j++ {
    table[pointToKey(current)] = j
    current = current.Add(G)
}

// Search: for i = 0,1,2,… check if h − i·(m·G) is in the table
candidate := h
for i := uint64(0); i <= maxI; i++ {
    if j, ok := t.table[pointToKey(candidate)]; ok {
        return i*t.m + j, nil  // v = i·m + j
    }
    candidate = candidate.Sub(t.mG)
}
```

---

## Summary

| Step | Input | Output | What's public |
|------|-------|--------|---------------|
| **1. Polynomial** | $sk = 7$, random $a_1{=}3$, $a_2{=}11$ | $f(x) = 7 + 3x + 11x^2$ | Nothing (dealer memory only) |
| **2. Feldman** | Coefficients $a_0, a_1, a_2$ | $C_0{=}[7]$, $C_1{=}[3]$, $C_2{=}[11]$ | Commitments (on-chain) |
| **2b. Verify** | Share $s_i$, commitments | $[s_i] \stackrel{?}{=} C_0 + iC_1 + i^2C_2$ | Pass/fail (each validator) |
| **3. Encrypt** | $v{=}3$, $r{=}4$ | $([4], [8])$ | Ciphertext (on-chain) |
| **4. Partial decrypt** | $D_i = s_i \cdot C_1$ | $[15], [21], [21]$ | $D_i$ values (on-chain) |
| **5. DLEQ proof** | $(e, z) = (5, 18)$ | Proof for validator 2 | Proof (on-chain, 64 bytes) |
| **6. Lagrange combine** | $\sum \lambda_i D_i$ | $[5] = sk \cdot C_1$ | Combined point |
| **7. Recover** | $C_2 - sk \cdot C_1$ | $[3] \Rightarrow v = 3$ | Tally result |

---

## Code Reference Map

| Step | Math | Code File |
|------|------|-----------|
| Key generation | $sk \xleftarrow{\$} \mathbb{F}_q$, $\; pk = sk \cdot G$ | `crypto/elgamal/elgamal.go` — `KeyGen()` |
| Polynomial split | $f(x) = sk + a_1 x + \cdots$, $\; s_i = f(i)$ | `crypto/shamir/shamir.go` — `Split()` |
| Feldman commit | $C_j = a_j \cdot G$ | `crypto/shamir/feldman.go` — `FeldmanCommit()` |
| Feldman verify | $[s_i] \stackrel{?}{=} \sum i^j C_j$ | `crypto/shamir/feldman.go` — `VerifyFeldmanShare()` |
| ECIES encrypt shares | $E = e \cdot G$, $S = e \cdot pk_i$, $k = \text{SHA256}(E \| S_x)$ | `crypto/ecies/ecies.go` — `Encrypt()` |
| Vote encryption | $C_1 = r \cdot G$, $\; C_2 = v \cdot G + r \cdot pk$ | `crypto/elgamal/elgamal.go` — `encryptCore()` |
| Homomorphic tally | $(C_1^{(a)} + C_1^{(b)}, \; C_2^{(a)} + C_2^{(b)})$ | `crypto/elgamal/elgamal.go` — `HomomorphicAdd()` |
| Partial decryption | $D_i = s_i \cdot C_1$ | `crypto/shamir/partial_decrypt.go` — `PartialDecrypt()` |
| PD DLEQ proof | $\log_G(VK_i) = \log_{C_1}(D_i)$ | `crypto/elgamal/dleq.go` — `GeneratePartialDecryptDLEQ()` |
| Lagrange coefficients | $\lambda_j = \prod \frac{-x_m}{x_j - x_m}$ | `crypto/shamir/shamir.go` — `LagrangeCoefficients()` |
| Combine partials | $\sum \lambda_j D_j = sk \cdot C_1$ | `crypto/shamir/partial_decrypt.go` — `CombinePartials()` |
| Recover plaintext | $v \cdot G = C_2 - sk \cdot C_1$, BSGS $\to v$ | `crypto/elgamal/bsgs.go` — `Solve()` |
| Tally DLEQ proof | $\log_G(pk) = \log_{C_1}(D)$ | `crypto/elgamal/dleq.go` — `GenerateDLEQProof()` |

### Orchestration Code

| Phase | Code File |
|-------|-----------|
| Deal (split + Feldman + ECIES encrypt) | `app/prepare_proposal_ceremony.go` — `CeremonyDealPrepareProposalHandler()` |
| Ack (decrypt + Feldman verify + write share) | `app/prepare_proposal_ceremony.go` — `CeremonyAckPrepareProposalHandler()` |
| Partial decrypt ($D_i$ + DLEQ) | `app/prepare_proposal_partial_decrypt.go` — `PartialDecryptPrepareProposalInjector()` |
| Combine + BSGS tally | `app/prepare_proposal.go` — tally combiner |
| On-chain verification | `x/vote/keeper/msg_server_tally_decrypt.go` |
