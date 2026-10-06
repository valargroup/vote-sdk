# Social identity linking for proxy-delegation delegates: external research dossier

Research date: 2026-10-05. "Proxy delegation" means the new feature where one wallet hands some or all of its round voting power to another person. It is not the existing `MsgDelegateVote` / ZKP1 note-to-VAN step. Every claim below is labeled as one of three kinds:
- **[code]**: something I read in source, with a permalink or path:line.
- **[live]**: something I probed on 2026-10-05.
- **[doc]**: a third party's documentation or article.
- My own reasoning is marked **Inference** or **Recommendation**.

---

## 1. ICNS (Interchain Name Service)

### 1.1 Timeline and status
- **[doc]** Announced 6 Dec 2022 (archived Medium post: http://web.archive.org/web/20250815132004/https://medium.com/@icns/announcing-icns-the-interchain-name-service-e61e0c3e2abb). It described itself as a "hard spoon" of the Twitter namespace. A Twitter "bootstrapping phase" was "expected to last roughly 1 year", after which ICNS would "transition to a more open system" with fees and auctions. Governance was a council of Chainapsis (Keplr), Cosmostation, Commonwealth and Skiff. Its mandate included "Select the Twitter verifier oracles". Disputes were explicitly left to governance: "impossible to resolve both fairly and algorithmically".
- **[doc]** Claim tutorial, 20 Dec 2022 (http://web.archive.org/web/20230116044911/https://medium.com/@icns/tutorial-how-to-claim-icns-with-keplr-a5caeef1df2f):
  - The user clicks "Authorize App" (Twitter OAuth). "ICNS oracles will relay their respective verification to the ICNS Registrar Contract".
  - The fee is 0.5 OSMO.
  - Afterwards the user is nudged to "TWEET INVITE LINK" for referral points. That tweet is marketing, not the proof.
- **[live]** Osmosis mainnet registrar `osmo1llh07xn7pcst3jqm0xpsucf90lzugfskkkhk8a3u2yznqmse4l5smydwpw`, queried via https://osmosis-rest.publicnode.com:
  - `verifier_pubkeys` holds **4** secp256k1 keys.
  - `verification_threshold_percentage` = **"0.5"**, so 2 of 4 signatures pass.
  - `fee` = 500000 uosmo.
- **[live]** Name-NFT contract `osmo1mypljhat...`:
  - `num_tokens` = **54,343**.
  - `transferrable` = **false**.
  - Admin = `osmo1ldhpay5c66xft8w7sn80c62sg7puxmu9fddyf7`, a **2-of-4 LegacyAminoPubKey multisig**. Its account `pub_key` reports threshold 2 with 4 keys.
- **[doc]** Osmosis prop 382 (https://www.polkachu.com/gov_proposals/29): the admin multisig is "collectively owned by Chainapsis, Cosmostation, Commonwealth, and Skiff" and adds and removes "operators of Twitter OAuth verification".
- **[code]** **New registrations are closed.**
  - icns-frontend PR 4, "Add banner to notify users that name registration is disabled", was created 2026-04-16 and merged 2026-04-17 (`gh api repos/icns-xyz/icns-frontend/pulls/4`).
  - The live https://www.icns.xyz shows "New ICNS registrations are closed. Names you already own are unaffected."
  - No reason is given publicly.
  - The contracts repo was last touched 2023-06-09.

### 1.2 Verification flow: OAuth, not a tweet
ICNS did not verify ownership by checking a public tweet. It used Twitter OAuth 2.0 user tokens.
- **[code]** Frontend OAuth scopes are `users.read`, `tweet.read`, `offline.access` (https://github.com/icns-xyz/icns-frontend/blob/6b8ffbcf78d4b8596b2bb396c10ffe95cf4d6c1a/constants/twitter.ts#L3-L7).
- **[code]** The frontend server exchanges the code and stores the refresh token in the session. It calls `/2/users/me?user.fields=profile_image_url,public_metrics,description` and rewrites the pfp URL from `normal.jpg` to `400x400.jpg` (`pages/api/twitter-auth-info.ts` L33-L72, L78, L105-L107).
- **[code]** The frontend's `/api/icns-verification` POSTs the raw request body, which **contains the user's OAuth access token**, to every verifier origin in `ICNS_VERIFIER_ORIGIN_LIST` (`pages/api/icns-verification.ts` L17-L31).
- **[code]** Each verifier (https://github.com/icns-xyz/icns-verifier/blob/c86ba91e54ea1a967c6e8e0b7aedcd9b0ac6babc/src/routes/verifyTwitter.ts#L7-L86):
  - receives `{claimer, authToken}` and rejects an already-seen token from its local LevelDB (L37-L45);
  - calls `GET https://api.twitter.com/2/users/me` with `Bearer <token>` (`src/utils/twitter.ts` L16-L29);
  - builds `{unique_twitter_id, name: username, claimer, contract_address, chain_id}` (L41-L47);
  - signs sha256(JSON) with secp256k1 (verifyTwitter.ts L71-L84).

### 1.3 On-chain registrar (CosmWasm)
Contracts are at https://github.com/icns-xyz/icns/tree/a8ff699de102e79b3c37f41a932c722d37cab36b.
- **[code]** Three contracts, following ENS terminology:
  - **Registrar** gates claims on the verifier quorum.
  - **Name-NFT** is CW-721; the token id is the name.
  - **Resolver** maps (name, bech32 prefix) to an address, plus a reverse "primary name" (`README.md` L11-L17; `docs/README.md` L6-L28).
- **[code]** `VerifyingMsg {name, claimer, contract_address, chain_id, unique_twitter_id}` (`contracts/icns-registrar/src/msg.rs` L144-L150). It has **no nonce, expiry or issued-at field**.
- **[code]** `check_verfying_msg` checks that:
  - the name, claimer (= tx sender), contract address and chain id match;
  - the Twitter numeric id is not yet registered (`checks.rs` L77-L131).
- **[code]** `check_verification_pass_threshold`:
  - each pubkey must be in the config;
  - **duplicates are detected by signature bytes, not by signer pubkey** (L159-L170);
  - signatures are verified, then `verifications.len() / verifier_pubkeys.len()` is compared to the threshold (L136-L191; `state.rs` L23-L44).
  - **Lesson:** dedupe by signer key.
- **[code]** **Admin bypass:** if the sender is admin, all verification is skipped (`contract.rs` L121-L144).
- **[code]** `UNIQUE_TWITTER_ID` maps the numeric Twitter id to the claimed name, to stop one account claiming several names by renaming (`contract.rs` L168; registrar `README.md` L20-L22).
- **[code]** The query `NameByTwitterId` "does not indicate the 'current' name of the user in Twitter, but the name that the user has used when claiming".
- **[code]** Names are only checked for the absence of '.'; there is no case normalization (`icns-name-nft/src/checks.rs` L42-L50).
- **[code]** Per-chain addresses are proven by Keplr ADR-36 signatures over a text blob containing username, chain id, contract, owner and salt (https://github.com/chainapsis/keplr-wallet/blob/c5e56dbd5ef7ca8bc1c82c5e2ec86a644b2996de/packages/background/src/keyring-cosmos/service.ts#L1901-L2040).

### 1.4 How Keplr displayed ICNS
- **[code]** Keplr hardcodes `ICNSInfo = {chainId: "osmosis-1", resolverContractAddress: "osmo1xk0s8..."}` (`apps/extension/src/config.ui.ts` L133-L137).
- **[code]** It shows the ICNS **primary name** with an ICNS icon in the account switcher (`pages/main/components/account-switch-float-modal/account-item.tsx` L146, L218-L234).
- **[code]** It resolves `name.suffix` in the recipient input (`components/input/reciepient-input/input.tsx` L114-L133).
- **Inference** from the code: ICNS stored **no pfp**; Keplr showed names, not avatars.

### 1.5 ICNS weaknesses relevant to us
1. **Bearer-token fan-out.** Every verifier, and the frontend backend, receives a live user OAuth token. Scopes include `tweet.read` and `offline.access`.
2. **Single point of failure.** All verifiers depended on **one X developer app** (`TWITTER_CLIENT_ID`) and on X's API. "Decentralized" verifiers were not independent of X's control.
3. **No public, re-checkable artifact.** Nobody can later re-verify a claim. Trust rests on the 2-of-4 verifiers plus an admin bypass.
4. **Handle frozen at claim time.** Handle changes desynchronize name and person.
5. **Policy decisions deferred to governance:** disputes, squatting, impersonation.
6. **Bootstrapping phase never transitioned.** Registration is now closed.

---

## 2. Other precedents

| System | Binding direction | Proof artifact | Who verifies | Notes |
|---|---|---|---|---|
| Keybase | Two-way: signed sigchain statement plus public post containing sig id | Tweet "Verifying myself: I am X on Keybase.io. <sig_id_short> /" | **Client re-verifies** | See below |
| Cosmos validator `identity` | **One-way**: validator writes a Keybase key suffix | None on the Keybase side | Explorers fetch the Keybase avatar | Impersonation possible |
| Solana validator-info | Was two-way via Keybase file | — | — | Keybase dropped |
| ENS text records | One-way: owner sets `com.twitter` | — | — | Display-only, unverified |
| Nostr NIP-05 | Two-way: DNS domain to pubkey | `/.well-known/nostr.json` | Client | "Identification, not verification" |
| Nostr NIP-39 | Two-way | kind 10011 event with `i` tags plus external post containing npub | Client | Closest to our design |
| Bluesky domain handles | Two-way: DNS TXT or well-known, plus DID doc `alsoKnownAs` | — | — | Bidirectional, re-resolved periodically |
| Bluesky verification | Trusted-verifier records | — | — | Self-invalidates on handle change |
| Farcaster | Two-way | Verified address signs EIP-712 claim (fid, address, blockHash, network); Farcaster Ed25519 app key signs message | — | X badge is off-protocol |
| Gitcoin / Human Passport | X stamp via OAuth | — | — | **Retired** |
| Agora (OP) | **One-way** | Self-declared `twitter`/`discord`/`warpcast` inside a wallet-signed statement | — | `endorsed` flag; default sort `weighted_random` |
| Tally + Karma | n/a | — | — | Shows participation metrics, not identity verification |
| OP partial delegation | n/a | — | — | Directly relevant to "up to N delegates" |

Details and citations for each row:

- **Keybase.**
  - **[code]** The client checks Twitter proofs with PVL rules shipped through Keybase's Merkle-anchored kit. It fills `https://api.twitter.com/1/statuses/oembed.json?id=%{tweet_id}`, checks `author_url` username case-insensitively, and regex-extracts the username and short sig id from the tweet HTML (https://github.com/keybase/client/blob/6b0650540439a3b4807b2c2955ecdb96896a13f2/pvl-tools/tab/1.cson#L278-L340).
  - **[code]** The rules had to tolerate X rewriting "keybase.io" into t.co links. They were updated 2026-05-29 to accept x.com (commit "update PVL for twitter proofs to allow x.com").
  - **[code]** The post instructions say: "Please publicly tweet ... and don't delete it" (`go/externals/proof_service_twitter.go` L68-L70).
  - **[doc]** Clients verify sigchains and a site-wide Merkle root that was anchored to Bitcoin (https://book.keybase.io/docs/server).
  - **[live]** The lookup API still answers (`/_/api/1.0/user/lookup.json`).
  - **[doc]** Anza says "Keybase has sunset its service and thus is no longer supported" for validator icons (https://docs.anza.xyz/operations/guides/validator-info).
- **Cosmos validator `identity`.**
  - **[doc]** Explorers use the field's Keybase key suffix to fetch avatars (https://forum.cosmos.network/t/explorers-can-we-have-better-use-of-the-identity-field/2558).
  - **[doc]** Impersonation thread: "two-way authentication" was proposed, as Solana did by putting the validator key in the Keybase folder (https://forum.cosmos.network/t/how-do-you-deal-with-impersonators/2608).
  - **[doc]** Mintscan moniker images come from a PR into Cosmostation's chainlist `${chain}/moniker/` folder (https://docs.mintscan.io/mintscan/registry/moniker). This is a curated-git-registry precedent.
- **ENS.**
  - **[doc]** ENSIP-5 text records (`com.twitter`) are unverified.
  - **[doc]** The draft "Text Record Attestations" ENSIP has an attester ENS name sign (name, address, key, value). There is no expiry; validity is self-invalidating on record or key change. Known hole: a lapsed attester ENS name can be re-registered by someone else. PoC at atst.me (https://discuss.ens.domains/t/ensip-text-record-attestations/22376).
  - **[doc]** The ENS app shows "Verified by Dentity" badges.
- **Farcaster.**
  - **[doc]** The verified address signs an EIP-712 claim, then the account's Ed25519 app key signs the Verification message (https://docs.farcaster.xyz/developers/guides/writing/verify-address).
  - **[doc]** App keys are registered on-chain in the Key Registry (https://docs.farcaster.xyz/learn/architecture/contracts).
  - **[doc]** The X badge in Warpcast is app-level, not protocol (https://neynar.com/blog/fc-dev-call-101024).
- **Nostr.**
  - **[doc]** NIP-05: "Clients must always follow public keys, not NIP-05 addresses". If the mapping changes, stop displaying the name but never re-point follows. Redirects are forbidden (https://raw.githubusercontent.com/nostr-protocol/nips/master/05.md L59-L103).
  - **[doc]** NIP-39: kind 10011 `i` tags `platform:identity` plus a proof pointer. Proof text: "Verifying that I control the following Nostr public key: <npub>". Twitter, GitHub gist, Mastodon, Telegram, Bluesky and Discord are defined (https://raw.githubusercontent.com/nostr-protocol/nips/master/39.md).
- **Bluesky.**
  - **[doc]** "Handles should not be trusted or considered valid until the DID is also resolved and the current DID document is confirmed to link back". Cache and re-resolve periodically; an invalid handle becomes `handle.invalid` (https://atproto.com/specs/handle).
  - **[code]** `app.bsky.graph.verification` records hold `subject` DID, `handle`, `displayName`, `createdAt`. A verification "is only valid if the current handle matches ... [and] displayName matches the one at the time of verifying". Apps decide which verifiers they trust (https://raw.githubusercontent.com/bluesky-social/atproto/main/lexicons/app/bsky/graph/verification.json).
  - **[doc]** Blog, 2025-04-21: 270k+ domain handles; trusted verifiers get scalloped badges; tapping shows who verified (https://bsky.social/about/blog/04-21-2025-verification).
  - **[doc]** Repo commits are signed with the DID-document key (https://atproto.com/specs/repository).
  - **[live]** `public.api.bsky.app` serves profiles and posts without auth.
- **Lens.**
  - **[doc]** Onchain-identity flags (PoH, Worldcoin, ENS); one verified Lens profile per World ID human (https://docs.lens.xyz/docs/onchain-identity).
  - No first-party X verification found.
- **Gitcoin / Human Passport.**
  - **[doc]** The X Stamp "has been retired". Reason: "Changes to the Twitter/X platform, including API access restrictions ... made it impossible to maintain reliable and consistent verification".
  - Former requirements: Premium-verified profile, 100 or more followers, account at least 1 year old (https://support.passport.human.tech/stamps/how-to-verify/x-stamp).
- **DAO delegate platforms.**
  - **[code]** Agora's delegate statement message is a JSON blob with self-declared `twitter`, `discord`, `warpcast`, `topIssues` and the statement. It is signed by the delegate's wallet with no X-side proof (https://github.com/voteagora/agora-next/blob/main/src/lib/delegateStatement/messageFormat.ts).
  - **[live]** Agora profiles carry an `endorsed` boolean. The default delegate sort is `weighted_random` (`src/app/delegates/search-params.ts`).
  - **[live]** vote.optimism.io profiles show voting power, "Delegated addresses", "Delegated from" with tx links, "Past Votes", For/Against/Abstain, top issues and statement.
  - **[doc]** Tally integrated Karma on 2024-06-11. Karma Score weights forum 1x, off-chain votes 3x, on-chain votes 5x; it also shows Snapshot %, on-chain % and a forum score (https://forum.arbitrum.foundation/t/tally-integrates-karmas-delegate-score-and-contributor-metrics/24911).
  - **[doc]** Arbitrum: "The 10 largest delegates represent 40% of delegated voting power" (https://forum.arbitrum.foundation/t/dvp-quorum-for-arbitrumdao/29996).
- **OP partial delegation (directly relevant to "up to N delegates").**
  - **[code]** `MAX_DELEGATIONS = 20` ("based on gas estimates"), `DENOMINATOR = 10_000` for relative delegations.
  - Absolute and relative allowances.
  - The relative sum must be at most `DENOMINATOR`.
  - Relative is applied first, then absolute (https://github.com/ethereum-optimism/specs/blob/main/specs/experimental/gov-delegation.md L48-L62, L361-L367).

---

## 3. X/Twitter API realities (as of 2026-10-05)

### 3.1 Pricing and limits
- **[doc]** Official pricing (https://docs.x.com/x-api/getting-started/pricing):
  - Pay-per-use, "no subscriptions", **no free tier**. New accounts get $20 in credits.
  - Reads: **Posts $0.005, Users $0.010** per resource.
  - Writes: post $0.015, post with URL $0.200.
  - Resources are deduplicated within a 24-hour UTC day.
  - Cap of 3M post reads per month before Enterprise.
- **[doc]** History: free API ended Feb 2023; Basic was $100 (later $200) per month; Pro $5,000 per month; pay-per-use beta Oct 2025, general Feb 2026 (https://techcrunch.com/2025/10/21/x-is-testing-a-pay-per-use-pricing-model-for-its-api ; https://gigazine.net/gsc_news/en/20260209-x-api-pay-per-use).
- **[doc]** Rate limits per 15 minutes (https://docs.x.com/x-api/fundamentals/rate-limits):

| Endpoint | Per app | Per user |
|---|---|---|
| `GET /2/tweets/:id` | 450 | 900 |
| `GET /2/tweets` | 3,500 | — |
| `GET /2/users/:id` | 300 | — |
| `GET /2/users/by/username` | 300 | — |
| `GET /2/users/me` | — | 75 |

- **Cost estimate (Inference)** at the per-resource prices above:
  - Each registration is about 1 post read plus 1 user read, roughly $0.015.
  - Maintenance for N delegates with daily user refresh and weekly post recheck:
    - N=100: about $32 per month.
    - N=1,000: about $322 per month.
    - N=10,000: about $3.2k per month.

### 3.2 Unauthenticated options (live-probed 2026-10-05)
- **oEmbed.**
  - `https://publish.x.com/oembed?url=https://x.com/<user>/status/<id>` returns HTTP 200 JSON with `author_name`, `author_url` (handle only, **no numeric id**), `html` text and `cache_age`.
  - `publish.twitter.com` now 301-redirects to `publish.x.com`.
  - The legacy `https://api.twitter.com/1/statuses/oembed.json?id=` also returns 200. Keybase uses it.
  - Official docs say "Requires authentication? No", "Rate limited: No" (https://docs.x.com/x-for-websites/oembed-api).
  - **[live]** A non-existent id returns the X HTML error page.
- **Syndication.**
  - `https://cdn.syndication.twimg.com/tweet-result?id=<id>&token=<x>` returns full JSON, including `user.id_str`, `screen_name`, `profile_image_url_https` (a `_normal.jpg` URL), `is_blue_verified`, `edit_control` and `isEdited`.
  - **[live]** Without `token` it returns `{}`.
  - **[doc]** This endpoint is undocumented. A token requirement was added in August 2023 and broke react-tweet and similar tools until reverse-engineered (https://www.stefanjudis.com/blog/how-to-prerender-tweets-without-using-the-official-twitter-apis/).
- **Third-party mirrors.** `api.fxtwitter.com` works; `api.vxtwitter.com` is behind a Cloudflare challenge. These add a third-party trust and privacy dependency.

### 3.3 Terms and policy
- **[live]** X ToS (https://x.com/en/tos; new version effective October 9, 2026):
  - Forbids access "by any means ... other than through our currently available, published interfaces".
  - "crawling or scraping the Services in any form, for any purpose without our prior written consent is expressly prohibited".
  - Liquidated damages of $15,000 per 1,000,000 posts accessed in 24h.
  - **Inference:** oEmbed is a published interface; the syndication endpoint is not.
- **[doc]** Developer Policy (https://docs.x.com/developer-terms/policy):
  - Off-X matching is allowed with user-provided info or public data such as handles and posts.
  - "If you store X Content offline, you must keep it up to date with the current state of that content on X". Delete or modify "within 24 hours" of a request or of deletion on X.
  - Remove content that "ceases to be available through the X API".
- **[doc]** Developer Agreement III.D: do not circumvent rate limits (https://docs.x.com/developer-terms/agreement).

### 3.4 Identity facts about X
- **[doc]** Usernames: at most 15 characters, alphanumeric plus underscore (https://help.x.com/en/managing-your-account/x-username-rules).
- **[doc]** A deactivated account's username frees up 30 days after deactivation (https://help.x.com/managing-your-account/how-to-deactivate-x-account).
- **[doc]** X's Handle Marketplace (Oct 2025) reassigns inactive handles. "Priority" handles revert if Premium lapses (https://www.techspot.com/news/110359-x-launches-premium-handle-marketplace-complex-access-rules.html ; https://decrypt.co/345103/x-handle-marketplace-rare-usernames-seven-figures).
- **[doc]** The blue check "means that the account has an active subscription to X Premium ... It does not mean that the account has been ID verified" (https://help.x.com/en/managing-your-account/about-x-bluecheck).
- **[doc]** Posts are limited to 280 weighted characters for non-Premium accounts. URLs always count as 23 characters via t.co (https://docs.x.com/resources/fundamentals/counting-characters).
- **[doc]** IDs are 64-bit Snowflake values, roughly time-ordered; use string ids (https://docs.x.com/fundamentals/x-ids).
- **[live]** Posts are editable: `edit_control` is present in syndication output.

### 3.5 Recommended proof-post design (Recommendation)
**Keys.** The delegate's Vizor wallet generates a long-lived **delegate identity key**. Ed25519 is suggested, matching the `ed25519` alg that zcash_voting already verifies; see section 8.

**Proof post** (ASCII only, no URL, no dotted tokens so X does not auto-link it, under 280 characters):
```
I accept Zcash shielded-vote delegations.
zvote-delegate:v1:<bech32m(identity_pk)>
```

**Registration statement.** Signed by the identity key and canonical-JSON encoded:
- `domain` = `"zcash-shielded-vote/delegate-registration/v1"`
- `identity_pk`
- `x_user_id`: the numeric id, which closes the two-way binding
- `x_handle_at_signing`
- `created_at`
- optional profile text, signed like Agora's statement
- optional extra providers

**Per-round opt-in.** Also signed by the identity key:
- `domain` = `".../round-optin/v1"`
- `vote_round_id`
- `delegate_receive_address`: the hotkey raw Orchard address the delegator's VAN-split binds to
- optional `accepting=true` and rationale text

No new post is needed per round.

**Verifier algorithm.** Primary source is the official API (compliant at about $0.005 per post); fallback is oEmbed, which is free and published.
1. Fetch the post by id with author expansion. Require `author_id == x_user_id`. The post must be public.
2. Normalize whitespace and t.co links. Regex-extract `zvote-delegate:v1:(\S+)` and require it to equal `identity_pk`.
3. Verify the statement signature.
4. Order competing proofs by Snowflake post id; the newest wins. A different `identity_pk` for the same `x_user_id` triggers the key-change policy in section 5.
5. Emit a signed observation: `{x_user_id, current_handle, post_id, identity_pk, observed_at, status}`.

**oEmbed fallback.** oEmbed gives only `author_url`, which is a handle. To recover the numeric id, compare against the last API-confirmed handle-to-id mapping. If they disagree, mark the entry "needs recheck"; never silently re-bind.

**Fallback providers**, all verifiable without X:
- GitHub gist: the API returns the stable numeric `owner.id`; 60 requests per hour unauthenticated ([live] `x-ratelimit-limit: 60`).
- Domain: DNS TXT `_zvote-delegate.<domain>` or `/.well-known/zvote-delegate.json`, as in atproto and NIP-05.
- Bluesky post: open API; repo commits are signed.
- Nostr: a BIP-340-signed event containing `identity_pk`, verifiable offline.
- Farcaster cast: signed by an Ed25519 app key registered on-chain.

---

## 4. Profile pictures

- **Privacy (Inference).** If the wallet hot-links `pbs.twimg.com`, X/CDN learns the viewer's IP and which delegates they look at. If it queries a registry server per search, that server learns delegation intent. Both are inconsistent with the project's privacy posture; the project already uses PIR for nullifier lookups.
- **Recommended pipeline:**
  1. The verifier fetches the pfp server-side via the API `profile_image_url` or syndication.
  2. Decode it in a sandbox and **re-encode** to fixed sizes (for example 64 and 256 px WebP or PNG), stripping metadata.
  3. Name the file by sha256 and put the hash inside the signed registry snapshot.
  4. Serve it from our CDN or mirrors.
  5. The wallet fetches the whole registry, and either the thumbnail bundle or content-addressed files, with no per-user identifiers. Optionally fetch over Vizor's embedded Tor client ([code] Vizor `rust/Cargo.toml` L119 enables the `tor` feature; `rust/src/tor_update_relay.rs` L1-L7 uses the embedded Arti client).
- **Sizing (Inference).** A 64 px thumbnail is about 2-4 KB. 1,000 delegates come to about 3-4 MB including JSON, so full download is fine. Above about 10k delegates, use k-anonymous shards by hash prefix (precedent: HIBP range API, https://haveibeenpwned.com/API/v3).
- **Security.** Re-encoding server-side shrinks the decoder attack surface. Malicious images are a real vector: libwebp CVE-2023-4863 was exploited in the BLASTPASS chain (https://www.wiz.io/fr-fr/blog/cve-2023-4863-and-cve-2023-5217-exploited-in-the-wild).
- **Staleness and compliance.** Re-fetch user objects daily. X policy requires keeping stored content current and deleting within 24h. Keep pfps and display names in a **mutable store, not in immutable git history**.
- **Moderation.** The pfp and display name are attacker-controlled: an influencer can switch to NSFW content or someone else's face.
  - Keep a denylist and allow blanking an avatar.
  - Hold pfp changes on "featured" delegates for review.
  - Never treat the pfp as identity; show the handle plus key fingerprint.
- **ICNS precedent [code].** It just took the OAuth `profile_image_url` and requested the 400x400 variant client-side (`twitter-auth-info.ts` L105-L107). It had no proxy.

---

## 5. Trust models and registry hosting

| Option | What you trust | Robustness | Cost | Notes |
|---|---|---|---|---|
| A. Single verifier, signed static snapshot, raw evidence (post id and identity-key signature) included | Verifier for "X account X posted K"; key-to-statement binding is client-checkable | Good: static mirrors and public evidence anyone can re-check | Low | Mirrors zcash_voting's existing pattern (section 8) |
| B. t-of-n independent verifiers (each fetches the post itself; no token sharing) | t colluders | Better | Medium; each needs X access, though oEmbed is free | ICNS used 2-of-4. Dedupe by signer key and remove admin bypass |
| C. Client-side re-verification | X endpoint plus network path | Fragile; privacy leak | Free | Offer as optional "verify independently" via Tor |
| D. On-chain registry on the vote chain | Validators plus verifiers | Highest auditability | High: chain upgrade, spam control | ICNS registrar pattern. A cheaper variant anchors only the snapshot hash per round |
| E. Git repo with PR-based curation | Maintainers | High availability, transparent history | Low infra, high friction for influencers | Mintscan moniker, token-holder-voting-config. History conflicts with deletion duties |
| F. Dynamic web API | Operator | Medium | Low | Logs searches; avoid for lookups |

**Lifecycle policy (Recommendation):**
- **Key everything by `x_user_id` and `identity_pk`.** Display the current handle, with "formerly @old" for 30 days after a rename. If an old handle now belongs to a different id, that is a different, unrelated entry.
- **Wallet favorites and existing delegations pin `identity_pk`**, never the handle (NIP-05 rule).
- **Re-verification cadence:**
  - user object daily;
  - proof post weekly, plus before each round snapshot;
  - featured delegates daily.
- **Failure handling:**
  - 404 or deleted post: mark "proof removed" and drop from search after a 72h grace.
  - Suspended or protected account: flag immediately.
  - 5xx or 429: no state change, so X outages do not cause mass de-listing.
- **Revocation:** deleting the post, or a revocation statement signed by the identity key. Existing in-round delegations remain bound to the key; only new delegations are blocked.
- **Key rotation:** a new post with a new key for the same `x_user_id`.
  - If the new statement is co-signed by the old identity key (continuity), it takes effect immediately.
  - Otherwise, a cooling-off period (for example 7 days or the next round) with a visible "key changed" warning. This covers X account takeover.
- **Snapshot discipline:**
  - Freeze a per-round snapshot whose hash is pinned in the signed round config, so every wallet sees the same view.
  - Allow append-only additions mid-round, labeled "registered during this round".
  - Key changes apply only at the next snapshot or after cooling-off.

---

## 6. Impersonation, squatting, sybil and lookalikes

- **Squatting.** Not possible for X-bound names when proofs require control of the X account and are keyed by numeric id.
  - Residual risk 1: X itself reassigns handles (Marketplace, 30-day release).
  - Residual risk 2: account takeover.
  - Both are handled by id-keying and the key-change policy.
- **One-way-binding impersonation.** Seen with Cosmos `identity` and Agora self-declared twitter. Prevented by the two-way binding: the post contains the key, and the key signs the x_user_id.
- **Lookalikes:**
  - Handles are limited to `[A-Za-z0-9_]{1,15}`, so the confusables are O/0, l/I/1, rn/m and extra underscores.
  - Display names are arbitrary Unicode; use UTS #39 confusable skeletons.
  - In search, rank exact handle matches first. Warn when a result's skeleton matches a featured delegate ("This is not @zooko").
  - Always show the @handle, a short key fingerprint and identicon, registration age, and key-change history.
- **Sybil and spam:**
  - X accounts are cheap and followers can be bought.
  - Use follower count and account age (Gitcoin's old thresholds: 100 followers, 1 year) only to **rank or list in search**. Exact-handle lookup and deeplinks still resolve.
  - Add registration rate limits and a manual "featured/endorsed" tier (Agora `endorsed`).
  - Show the X blue check only as information; it is a Premium subscription.
- **Concentration:** the Arbitrum top-10 delegates hold 40%. Use weighted-random ordering (Agora default), surface many delegates, and possibly show the delegate's current share if that becomes knowable.
- **Deeplinks:** links that say "delegate to @X" must resolve through the registry by `identity_pk` or `x_user_id`. Never trust an address embedded in a link next to a social label. The Vizor deeplink gateway is a dumb router (/Users/czstudio/Documents/vizor-deeplink-server/README.md L3-L11).

---

## 7. Ranked recommendations (most robust per cost first)

1. **Two-way-bound public post proof plus Ed25519 identity key plus per-round signed opt-in carrying the delegate receive address.** Cost: near zero. Closes impersonation, handle recycling and dead-delegate accumulation.
2. **Single verifier service with signed, hash-pinned static snapshot.**
   - The service uses the official X API (about $30-$320 per month at 100-1,000 delegates) with oEmbed fallback and no syndication in production.
   - The snapshot is served from two mirrors and includes the raw evidence.
   - The schema should allow t-of-n signatures from day one.
3. **Privacy-preserving pfp pipeline:** re-encode, content-address, bundle, never hot-link X, optional Tor fetch.
4. **Lifecycle state machine:** grace periods, key-change cooling-off, rename display, compliance-driven refresh, mutable store for X content.
5. **At least one non-X provider at launch** (GitHub gist or domain), plus Bluesky and Nostr later. The Gitcoin X stamp and ICNS show X-only identity can be shut off.
6. **Curation and anti-lookalike UI:** featured tier, confusable warnings, fingerprints, weighted-random ordering.
7. **t-of-n verifiers**, run by Vizor plus vote-chain endorsers or validators. Dedupe by key, no admin bypass.
8. **Anchor the per-round registry hash on the vote chain**, via round creation or an endorsement payload, or build a full on-chain registry module.
9. **Later:** an optional client-side "re-verify" over Tor; zkTLS (Reclaim/TLSNotary) to drop verifier trust; an OAuth path as UX sugar. Never forward tokens to multiple parties.

**Avoid:**
- ICNS-style OAuth token fan-out.
- Keying on handles.
- The undocumented syndication endpoint as a primary source.
- Keybase as a dependency.
- Hot-linking pbs.twimg.com.
- Storing X content in immutable git history.
- Admin bypass without a visible label.

---

## 8. Fit with existing Vizor and zcash_voting architecture

- **[code]** Vizor already has a **hash-pinned static trust anchor with two mirrors**: `voting.valargroup.dev/pins/prod/<sha256>/v2-static-voting-config.json?checksum=sha256:...` and a raw.githubusercontent.com mirror (`/private/tmp/claude-501/-Users-czstudio-Documents-vote-sdk--claude-worktrees-vote-delegation-planning-29b451/0cff7cc1-5f3f-4de1-be83-322a1fdf7727/scratchpad/src/vizor-wallet/lib/src/services/voting/voting_config_loader.dart` L14-L46).
- **[code]** zcash_voting authenticates per-round dynamic config with Ed25519 `trusted_keys` from the static config (`.../src/zcash_voting/zcash_voting/src/config/mod.rs` L65, L863-L875). `RoundEntry.signatures` (L886-L898) is checked by `verify_round_entry`, which **returns true on the first valid trusted signature, so it is 1-of-N** (L1092-L1143). The signed payload is `RoundAuthPayloadV2(round_id, ea_pk, pir_layout)` (L1121).
- **Inference / Recommendation:** pin `delegate_registry_sha256` in a new round-auth payload version, or sign the registry snapshot with the same trusted keys. If t-of-n is wanted for the registry, that needs new verification logic, because today's semantics are 1-of-N.
- **[code]** token-holder-voting-config is served via GitHub Pages. The chain commits `VoteRound.proposals_hash` and wallets "hard-fail" on mismatch (/Users/czstudio/Documents/token-holder-voting-config/README.md L3, L57, L69-L71). The round id is a Poseidon hash including the proposals_hash (/Users/czstudio/Documents/vote-sdk/.claude/worktrees/vote-delegation-planning-29b451/x/vote/types/tx.pb.go L27). That gives a precedent for committing a registry hash on-chain.
- **[code]** `MsgEndorseRound` carries only `(creator, endorser_id, vote_round_id)` (/Users/czstudio/Documents/vote-sdk/.claude/worktrees/vote-delegation-planning-29b451/proto/svote/v1/tx.proto L294-L300; README.md L271-L272). Endorsing a registry hash would need a new field or message (Inference).

## 9. Cross-team notes (outside my area, but they constrain identity design)

- **[doc]** The book says the delegate's VAN carries "their hotkey" (`.../src/shielded-vote-book/userflow/delegating-your-vote.md` L9). The registry must therefore publish an Orchard-style receive address per delegate and round.
- **[doc]** Proving control of a shielded address by signature needs a ZK construction. ZIP 304 covers **Sapling only** and is still Draft (https://zips.z.cash/zip-0304).
- Options for proving address control: an interactive challenge at registration (encrypt a nonce to the address), or accept unproven addresses. A typo'd or foreign address only strands the delegators' power.
- **"Did they vote?"**
  - Ethereum delegate platforms rely on public voting records. Shielded voting has none by default.
  - Identity can only offer the per-round opt-in, plus optional delegate-signed vote rationales, which are unverifiable without protocol support.
  - Any real answer needs circuit or protocol design, for example a delegator-computable spend tag.
- **[doc]** Publicly identified delegates with large power make tally deltas informative. The submission-server ZIP already notes that "social context ... can close the remaining gap" (`.../src/zips/draft-valargroup-submission-server__adam_submission-server-client-delay.md` L92-L101).

## 10. Unverified items
- Who operated each of the 4 ICNS verifier keys.
- Why ICNS closed registrations.
- Whether oEmbed fails for protected accounts (expected yes; untested).
- Whether X anti-spam suppresses posts containing key strings.
- Whether X serves oEmbed to Tor exits.
- The exact X pfp size suffixes beyond `_normal` and `400x400`.

## Sources
- ICNS repos: https://github.com/icns-xyz/icns , https://github.com/icns-xyz/icns-verifier , https://github.com/icns-xyz/icns-frontend
- ICNS live site: https://www.icns.xyz
- ICNS articles: archived Medium links above
- Osmosis props: https://www.polkachu.com/gov_proposals/29 , https://osmosis.valopers.com/proposals/383
- Keplr: https://github.com/chainapsis/keplr-wallet
- Keybase: https://github.com/keybase/client , https://book.keybase.io/docs/server
- Validator identity and icons: https://forum.cosmos.network/t/how-do-you-deal-with-impersonators/2608 , https://docs.anza.xyz/operations/guides/validator-info , https://docs.mintscan.io/mintscan/registry/moniker
- ENS: https://discuss.ens.domains/t/ensip-text-record-attestations/22376
- Farcaster: https://docs.farcaster.xyz/developers/guides/writing/verify-address
- Nostr: https://github.com/nostr-protocol/nips (05.md, 39.md)
- Bluesky: https://atproto.com/specs/handle , https://bsky.social/about/blog/04-21-2025-verification
- Human Passport: https://support.passport.human.tech/stamps/how-to-verify/x-stamp
- Agora: https://github.com/voteagora/agora-next , https://vote.optimism.io/delegates
- Tally and Karma: https://forum.arbitrum.foundation/t/tally-integrates-karmas-delegate-score-and-contributor-metrics/24911
- OP partial delegation spec: https://github.com/ethereum-optimism/specs/blob/main/specs/experimental/gov-delegation.md
- X docs: https://docs.x.com/x-api/getting-started/pricing , https://docs.x.com/x-api/fundamentals/rate-limits , https://docs.x.com/x-for-websites/oembed-api , https://docs.x.com/developer-terms/policy , https://x.com/en/tos
- X help: https://help.x.com/en/managing-your-account/x-username-rules , https://help.x.com/en/managing-your-account/about-x-bluecheck
- ZIP 304: https://zips.z.cash/zip-0304
- HIBP range API: https://haveibeenpwned.com/API/v3
