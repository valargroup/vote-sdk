# AI / MCP Setup

This document describes the MCP (Model Context Protocol) server integrations used in this workspace for Cursor IDE.

Configuration file: `~/.cursor/mcp.json`

## Figma

Figma MCP provides access to design files directly from the IDE. The agent can fetch screenshots, generate UI code from designs, inspect metadata, and read design variables.

**Config:**

```json
"Figma": {
  "url": "https://mcp.figma.com/mcp",
  "headers": {}
}
```

**Setup:** No local dependencies required -- connects to Figma's hosted MCP endpoint. Authentication is handled through the Figma desktop app (must be running and logged in).

**Capabilities:**
- Fetch screenshots of Figma nodes
- Generate UI code from design nodes
- Read node metadata and variable definitions
- Code Connect mapping between Figma components and codebase components

## Protocol Spec (shielded_vote_book symlink)

The protocol specification and design docs live in the `shielded_vote_book` Obsidian vault (a separate shared repo). A symlink brings them into the workspace so the agent can read them with its built-in file tools.

**Setup:**

1. Create the symlink from the workspace root to your local clone of the shielded_vote_book:
   ```bash
   ln -s /path/to/your/shielded_vote_book shielded_vote_book
   ```
   For example: `ln -s ~/Documents/shielded_vote_book shielded_vote_book`

2. Verify it works:
   ```bash
   ls shielded_vote_book/
   ```
   You should see `SUMMARY.md`, `data-types.md`, the `zkps/` directory, etc.

3. The symlink is already gitignored — it won't be committed.

**How the agent uses it:** Cursor's built-in Read/Shell tools follow symlinks without restriction, so the agent can read any file under `shielded_vote_book/` directly. No additional MCP server is needed.
