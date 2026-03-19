# ECIES on Pallas

Elliptic Curve Integrated Encryption Scheme using the Pallas curve, ChaCha20-Poly1305, and SHA-256.

## Diffie-Hellman on Elliptic Curves

Two parties want to establish a shared secret over a public channel. Neither sends their private key. The trick is **commutativity of scalar multiplication** on elliptic curves.

Alice has private key \(a\), public key \(A = a \cdot G\). Bob has private key \(b\), public key \(B = b \cdot G\). Both \(A\) and \(B\) are public.

- Alice computes \(a \cdot B = a \cdot (b \cdot G) = (ab) \cdot G\).
- Bob computes \(b \cdot A = b \cdot (a \cdot G) = (ba) \cdot G\).

Scalar multiplication is commutative, so they arrive at the same point \(S = (ab) \cdot G\). An eavesdropper sees \(A\) and \(B\) but can't compute \(S\) without knowing \(a\) or \(b\). This is the **Elliptic Curve Diffie-Hellman (ECDH)** problem — given \(a \cdot G\) and \(b \cdot G\), computing \(ab \cdot G\) is believed to be hard.

That shared point \(S\) isn't directly usable as a symmetric key (it's a curve point, not a byte string), so you hash it to derive a symmetric key: \(k = \text{SHA256}(S_x)\). Now both parties have the same \(k\) and can use it with any symmetric cipher.

## The Problem: Static Keys Are Dangerous

If Alice and Bob reuse the same keypairs across many exchanges, they get the same shared secret \(S\) every time. This means every message encrypted under that key is **linkable**, and if \(k\) is ever compromised, all past and future messages are exposed.

## Ephemeral Keys Fix This

Instead of using her long-lived key, Alice generates a **fresh keypair** \((e, E = e \cdot G)\) for each message. She computes the shared secret as \(e \cdot B\); Bob computes \(b \cdot E\). Same result: \((eb) \cdot G\).

Alice sends \(E\) alongside the ciphertext. After encryption, she **deletes** \(e\). Now every message has a unique shared secret, even if Bob's key never changes. This is the **ephemeral-static ECDH** pattern.

If you compromise Bob's long-lived key \(b\) later, you can decrypt past messages (no forward secrecy in this direction). But compromising any single ephemeral key \(e\) only exposes that one message.

## ECIES: Putting It Together

ECIES is the name for this pattern packaged as a complete encryption scheme:

1. **Key agreement** — Generate ephemeral \((e, E)\), compute \(S = e \cdot \text{pk}_{\text{recipient}}\).
2. **Key derivation** — \(k = \text{SHA256}(E \mathbin\| S_x)\). Including \(E\) in the hash input is domain separation: it binds the derived key to this specific ephemeral key, preventing subtle attacks where an adversary replays \(E\) with a different ciphertext.
3. **Symmetric encryption** — \(\text{ct} = \text{AEAD}(k, \text{plaintext})\), using ChaCha20-Poly1305. The AEAD provides both confidentiality (encryption) and integrity (authentication). Without authentication, a man-in-the-middle could flip bits in the ciphertext undetected.
4. **Output** — \((E, \text{ct})\): the ephemeral public key plus the ciphertext. This is everything the recipient needs.

**Decryption** is the reverse: the recipient computes \(S = \text{sk}_{\text{recipient}} \cdot E\), derives \(k\) the same way, and decrypts while verifying the AEAD tag.

## Why This Works for the Ceremony

In the setup ceremony, the orchestrator needs to send `ea_sk` to each validator \(i\) privately. The orchestrator knows each validator's public key \(\text{pk}_i\) (from genesis registration). For each validator:

- The orchestrator is "Alice" with a fresh ephemeral key \(e_i\).
- The validator is "Bob" with static key \((\text{sk}_i, \text{pk}_i)\).
- The shared secret is \(S_i = e_i \cdot \text{pk}_i = \text{sk}_i \cdot E_i\).
- The symmetric key \(k_i = \text{SHA256}(E_i \mathbin\| S_{i,x})\) is unique per validator.
- The ciphertext \(\text{ct}_i\) encrypts `ea_sk` under that unique key.

Each \((E_i, \text{ct}_i)\) is posted publicly (on-chain), but only validator \(i\) can decrypt their ciphertext because only they know \(\text{sk}_i\). Other validators see the ciphertexts but can't derive each other's symmetric keys.

## Why Pallas Specifically

Nothing about the theory requires Pallas — any prime-order elliptic curve group works. The reason to use Pallas here is that the validators **already have Pallas keypairs** (they need them for ElGamal decryption), so we avoid introducing a second curve. The ECDH math is identical whether you're on Pallas, secp256k1, Curve25519, or any other curve — we're just reusing the algebraic structure we already have.

## Security Assumption

Everything above rests on the **Computational Diffie-Hellman (CDH) assumption**: given \(a \cdot G\) and \(b \cdot G\), no efficient algorithm can compute \(ab \cdot G\). This is believed to hold for Pallas (and any well-chosen elliptic curve group). If CDH breaks, all of elliptic curve cryptography breaks — ElGamal encryption, ZKPs on Pallas/Vesta, everything. So this isn't introducing a new assumption; it's the same one the whole system already depends on.
