# Shamir Secret Sharing on Pallas

Threshold secret sharing over the Pallas scalar field $\mathbb{F}_q$, providing the cryptographic building blocks for threshold ElGamal decryption in the EA key ceremony.

## Why Shamir Sharing

In the current ceremony every validator receives the full election authority secret key $\text{ea\_sk}$. Any single validator can decrypt individual votes. Shamir sharing replaces this: the dealer splits $\text{ea\_sk}$ into $n$ shares such that any $t$ shares can cooperate to decrypt the tally, but fewer than $t$ shares reveal nothing about the key.

## The Math

### Polynomial splitting

Choose a random degree-$(t{-}1)$ polynomial over $\mathbb{F}_q$:

$$f(x) = s + a_1 x + a_2 x^2 + \cdots + a_{t-1} x^{t-1}$$

where $s = \text{ea\_sk}$ is the secret (the constant term) and each $a_i$ is a random scalar. Evaluate $f$ at points $1, 2, \ldots, n$ to produce shares $(i, f(i))$.

Any $t$ evaluations of a degree-$(t{-}1)$ polynomial uniquely determine it — this is the fundamental theorem of algebra over a field. Fewer than $t$ evaluations leave the constant term (the secret) uniformly distributed. This is **information-theoretic** security: it holds regardless of the adversary's computational power.

### Lagrange interpolation

Given $t$ shares $\{(x_j, y_j)\}_{j=1}^{t}$, reconstruct $f(0) = s$ via:

$$s = \sum_{j=1}^{t} \lambda_j \cdot y_j \qquad \text{where} \quad \lambda_j = \prod_{\substack{m=1 \\ m \neq j}}^{t} \frac{0 - x_m}{x_j - x_m}$$

Each $\lambda_j$ is a Lagrange basis coefficient computed entirely in $\mathbb{F}_q$. Division is multiplication by the modular inverse.

The same Lagrange coefficients work in the exponent for threshold decryption — instead of combining scalar shares to recover $s$, we combine curve points $D_i = f(i) \cdot C_1$ to recover $s \cdot C_1$ without ever reconstructing $s$ in the clear.

### Partial decryption

Given an ElGamal ciphertext $(C_1, C_2) = (r \cdot G,\; v \cdot G + r \cdot \text{ea\_pk})$, validator $i$ computes a partial decryption:

$$D_i = f(i) \cdot C_1$$

After collecting $t$ valid partials, the combiner recovers:

$$\text{ea\_sk} \cdot C_1 = \sum_{j=1}^{t} \lambda_j \cdot D_j$$

and then $v \cdot G = C_2 - \text{ea\_sk} \cdot C_1$, which is solved for $v$ via baby-step giant-step.

### Feldman commitments

The dealer publishes $t$ **Feldman polynomial commitments** alongside the encrypted shares:

$$C_j = a_j \cdot G \quad \text{for } j = 0, \ldots, t{-}1$$

Each validator verifies their share against the public commitments:

$$\text{share}_i \cdot G \stackrel{?}{=} \sum_{j=0}^{t-1} i^j \cdot C_j$$

This proves that all shares lie on the **same** degree-$(t{-}1)$ polynomial. Without Feldman commitments, per-validator verification keys $VK_i = f(i) \cdot G$ only prove each share individually — a malicious dealer can send shares from different polynomials to different validators without detection.

The first commitment $C_0 = a_0 \cdot G = s \cdot G = \text{ea\_pk}$ is the election authority public key, so it no longer needs to be transmitted separately.

During the tally phase, $VK_i$ (needed for DLEQ proof verification) is derived on the fly from the stored commitments via `EvalCommitmentPolynomial`.

## Package API

| Function | Purpose |
|---|---|
| `Split(secret, t, n)` | Generate $n$ shares from a degree-$(t{-}1)$ polynomial with $f(0) = \text{secret}$. Returns shares and coefficients. |
| `Reconstruct(shares, t)` | Recover the secret from $\geq t$ shares via Lagrange at $x = 0$. Test-only — production uses `CombinePartials`. |
| `LagrangeCoefficients(indices, target)` | Compute Lagrange basis scalars for given evaluation points at an arbitrary target. |
| `FeldmanCommit(G, coeffs)` | Compute Feldman commitments $C_j = a_j \cdot G$ from the polynomial coefficients returned by `Split`. |
| `VerifyFeldmanShare(G, commitments, index, share)` | Check a share against Feldman commitments: $\text{share} \cdot G = \sum C_j \cdot i^j$. |
| `EvalCommitmentPolynomial(commitments, index)` | Evaluate the commitment polynomial at a Shamir index to derive $VK_i$ (Horner's method in the group). |
| `PartialDecrypt(share, C1)` | Compute $D_i = f(i) \cdot C_1$. |
| `CombinePartials(partials, t)` | Lagrange interpolation in the exponent to recover $\text{ea\_sk} \cdot C_1$. |

## Conventions

- **1-based indices**: Share evaluation points are $1, 2, \ldots, n$. Index 0 is the secret (reconstruction target) and never appears as a share index.
- **Scalar field**: All arithmetic is in $\mathbb{F}_q$ (Pallas scalar field) via `curvey.ScalarPallas`.
- **Generator parameter**: Feldman functions accept the generator $G$ as an argument rather than importing it from `crypto/elgamal`, keeping the package dependency-free on group-level choices.
