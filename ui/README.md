# Shielded Vote Generator UI

A browser-based tool for creating, editing, and publishing shielded voting rounds. Build proposals, configure snapshot settings, and broadcast voting sessions on-chain — all from a single interface.

## Features

- **Round builder** — create voting rounds with binary or multi-choice proposals
- **Snapshot settings** — configure snapshot height and voter eligibility
- **On-chain publishing** — sign and broadcast `MsgCreateVotingSession` / `MsgSetVoteManager` transactions via Keplr
- **JSON export** — preview and copy the generated round payload
- **Local persistence** — rounds auto-save to localStorage as drafts

## Tech Stack

React, TypeScript, Vite, Tailwind CSS, CosmJS

## Getting Started

```bash
npm install
npm run dev
```

## Scripts

| Command           | Description                         |
| ----------------- | ----------------------------------- |
| `npm run dev`     | Start dev server                    |
| `npm run build`   | Type-check and build for production |
| `npm run preview` | Preview production build            |
| `npm run lint`    | Run ESLint                          |
