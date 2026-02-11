import { defineConfig } from "vitest/config";

export default defineConfig({
  test: {
    testTimeout: 30_000, // 30s per test (network + block times)
    hookTimeout: 30_000, // 30s for beforeAll/afterAll hooks
    fileParallelism: false, // run test files sequentially to avoid round ID collisions
  },
});
