# zally

## Technical Assumptions


1. Start with a pre-defined val set. Changes either via major upgrades or add a poa module (future)
2. How to avoid cursed encoding stuff imported by clients?
   * Tx submission:
     * Client sends a plain JSON POST, no Cosmo SDK protobuf
     * Server handler does the dirty work: parse JSON and encoide as needed
   * Query:
     * gRPC gateway already supports JSON out-of-the-box
3. No native x/gov module
   *  Voters are public Cosmos accounts.
   * Vote weight = staked tokens
   * Uses standard Cosmos tx signing which we are bypassing
   * For us, it is more straightforward to rebuild custom stuff instead of trying to fit a module purposed for diverging context
