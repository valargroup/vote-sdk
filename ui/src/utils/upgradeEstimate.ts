export interface UpgradeHeightEstimateInput {
  latestHeight: number;
  latestTimeMs: number;
  sampleHeight: number;
  sampleTimeMs: number;
  targetTimeMs: number;
}

export interface UpgradeHeightEstimate {
  averageSecondsPerBlock: number;
  sampledBlocks: number;
  secondsUntilTarget: number;
  blocksUntilTarget: number;
  targetHeight: number;
  estimatedTimeMs: number;
}

function assertFinitePositive(name: string, value: number) {
  if (!Number.isFinite(value) || value <= 0) {
    throw new Error(`${name} must be greater than 0`);
  }
}

export function estimateUpgradeHeight(input: UpgradeHeightEstimateInput): UpgradeHeightEstimate {
  assertFinitePositive("latest height", input.latestHeight);
  assertFinitePositive("sample height", input.sampleHeight);
  assertFinitePositive("latest block time", input.latestTimeMs);
  assertFinitePositive("sample block time", input.sampleTimeMs);
  assertFinitePositive("target time", input.targetTimeMs);

  if (input.sampleHeight >= input.latestHeight) {
    throw new Error("sample height must be below latest height");
  }
  if (input.sampleTimeMs >= input.latestTimeMs) {
    throw new Error("sample block time must be before latest block time");
  }
  if (input.targetTimeMs <= input.latestTimeMs) {
    throw new Error("target time must be after the latest block time");
  }

  const sampledBlocks = input.latestHeight - input.sampleHeight;
  const elapsedSeconds = (input.latestTimeMs - input.sampleTimeMs) / 1000;
  const averageSecondsPerBlock = elapsedSeconds / sampledBlocks;
  if (!Number.isFinite(averageSecondsPerBlock) || averageSecondsPerBlock <= 0) {
    throw new Error("average block speed must be greater than 0");
  }

  const secondsUntilTarget = (input.targetTimeMs - input.latestTimeMs) / 1000;
  const blocksUntilTarget = Math.max(1, Math.ceil(secondsUntilTarget / averageSecondsPerBlock));
  const targetHeight = input.latestHeight + blocksUntilTarget;
  const estimatedTimeMs = input.latestTimeMs + blocksUntilTarget * averageSecondsPerBlock * 1000;

  return {
    averageSecondsPerBlock,
    sampledBlocks,
    secondsUntilTarget,
    blocksUntilTarget,
    targetHeight,
    estimatedTimeMs,
  };
}

export function sampleHeightForWindow(latestHeight: number, requestedWindow: number): number {
  assertFinitePositive("latest height", latestHeight);
  assertFinitePositive("averaging window", requestedWindow);
  const wholeWindow = Math.max(1, Math.floor(requestedWindow));
  return Math.max(1, latestHeight - wholeWindow);
}
