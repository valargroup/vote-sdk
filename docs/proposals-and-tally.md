# Proposals, Options, and Tally

This document describes the data model for proposals and vote options within a voting round, including option groups for avoiding vote splits, and how tally results are computed and presented.

## Proposal Structure

Each `VoteRound` contains 1-15 `Proposal` messages. Each proposal has 2-8 `VoteOption` entries (0-indexed) and an optional list of `OptionGroup` entries.

```protobuf
message VoteOption {
  uint32 index = 1; // 0-indexed
  string label = 2; // e.g. "Support", "Oppose"
}

message OptionGroup {
  uint32 id = 1;                       // 0-indexed group ID
  string label = 2;                    // e.g. "At a fixed date"
  repeated uint32 option_indices = 3;  // Which VoteOption.index values belong to this group
}

message Proposal {
  uint32 id = 1;                           // 1-indexed
  string title = 2;
  string description = 3;
  repeated VoteOption options = 4;         // 2-8 named choices
  repeated OptionGroup option_groups = 5;  // If non-empty, enables grouped tally mode
}
```

## Option Groups

Option groups solve the **vote-split problem**: when multiple options represent the same broad position, their votes should be aggregated before comparing against other positions.

### Example: Sprout Pool Sunset

Consider a poll with four options:

| Index | Label                                    |
| ----- | ---------------------------------------- |
| 0     | Immediately upon NU7 activation date     |
| 1     | One year following poll conclusion date  |
| 2     | Two years following poll conclusion date |
| 3     | When quantum threat is imminent          |

Options 1 and 2 are both "fixed date" positions. Without grouping, they split the fixed-date vote and may each lose to a less popular position individually.

The solution is one `OptionGroup`:

| Group ID | Label                                     | Option Indices |
| -------- | ----------------------------------------- | -------------- |
| 0        | At a fixed date following poll conclusion | [1, 2]         |

Options 0 and 3 are standalone -- they don't need groups because they each represent a single position. A UI would present:

1. **Immediately upon NU7 activation date** (standalone option 0)
2. **At a fixed date following poll conclusion** (group 0 -- voter then chooses between "One year" or "Two years")
3. **When quantum threat is imminent** (standalone option 3)

### Validation Rules

- Each group must contain at least 2 options (single-option groups are rejected -- use a standalone option instead)
- Group IDs must be 0-indexed and sequential
- Group labels must be non-empty ASCII
- Option indices must reference valid options (`< len(options)`)
- An option can appear in at most one group (no overlaps)
- Options not in any group are standalone -- partial coverage is allowed
- If `option_groups` is empty, the proposal uses flat (ungrouped) tallying

## Tally Pipeline

The tally pipeline is the same regardless of whether groups are present:

1. **Accumulation**: Each `MsgRevealShare` adds an ElGamal ciphertext to the per-`(proposal_id, vote_decision)` accumulator via `HomomorphicAdd`.
2. **Decryption**: When the round reaches TALLYING, partial decryptions (threshold mode) or direct decryption (legacy mode) recover the plaintext vote total per `(proposal_id, vote_decision)`.
3. **Finalization**: `MsgSubmitTally` stores a `TallyResult` per `(proposal_id, vote_decision)` and transitions the round to FINALIZED.

Option groups do **not** change any of these steps. Grouping is purely a post-decryption aggregation applied when building the `VoteSummary` query response.

## VoteSummary Query

The `VoteSummary` query (`GET /shielded-vote/v1/vote-summary/{round_id}`) returns a `ProposalSummary` per proposal with:

- **`options`**: Per-option `OptionSummary` with `ballot_count` (number of revealed shares) and `total_value` (decrypted aggregate, populated when finalized).
- **`groups`**: Per-group `GroupSummary` with aggregated `ballot_count` and `total_value` summed across all options in the group. Only present when the proposal has `option_groups`.

```protobuf
message ProposalSummary {
  uint32 id = 1;
  string title = 2;
  string description = 3;
  repeated OptionSummary options = 4;
  repeated GroupSummary groups = 5;  // Populated when proposal has option_groups
}

message GroupSummary {
  uint32 id = 1;
  string label = 2;
  repeated uint32 option_indices = 3;
  uint64 ballot_count = 4;  // Sum of ballot_count across all options in group
  uint64 total_value = 5;   // Sum of total_value across all options in group (finalized only)
}
```

### Interpreting Grouped Results

Using the Sprout example with finalized results:

| Option          | total_value |
| --------------- | ----------- |
| 0: Immediately  | 1500        |
| 1: One year     | 800         |
| 2: Two years    | 1200        |
| 3: When quantum | 500         |

The `VoteSummary` response includes:

**Per-option** (always present):
- Option 0: 1500
- Option 1: 800
- Option 2: 1200
- Option 3: 500

**Per-group** (one group defined):
- Group 0 "Fixed date": 2000 (800 + 1200)

To determine the winner, a client compares the group total against standalone option totals:
- Immediately: 1500
- Fixed date (group): 2000
- When quantum: 500

The "Fixed date" group wins. Within the group, "Two years" (1200) beats "One year" (800).
