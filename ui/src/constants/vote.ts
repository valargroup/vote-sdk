export const MIN_VOTE_OPTIONS = 2;
export const MAX_VOTE_OPTIONS = 8;

export interface VoteOptionInput {
  index: number;
  label: string;
}

export function validateProposalOptions(
  proposalLabel: string,
  options: readonly VoteOptionInput[],
): void {
  if (options.length < MIN_VOTE_OPTIONS || options.length > MAX_VOTE_OPTIONS) {
    throw new Error(
      `${proposalLabel} must have ${MIN_VOTE_OPTIONS}-${MAX_VOTE_OPTIONS} options, got ${options.length}`,
    );
  }

  options.forEach((option, index) => {
    if (option.index !== index) {
      throw new Error(
        `${proposalLabel} option index mismatch at position ${index}: expected ${index}, got ${option.index}`,
      );
    }
    if (!option.label.trim()) {
      throw new Error(`${proposalLabel} option ${index} label cannot be empty`);
    }
  });
}

export function validateVoteChoice(
  proposalLabel: string,
  voteDecision: number,
  options: readonly VoteOptionInput[],
): void {
  validateProposalOptions(proposalLabel, options);
  if (!Number.isInteger(voteDecision) || voteDecision < 0 || voteDecision >= options.length) {
    throw new Error(
      `vote_decision ${voteDecision} out of range [0, ${options.length - 1}] for ${proposalLabel}`,
    );
  }
}
