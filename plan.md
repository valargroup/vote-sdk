Overall layout

App shell

Left sidebar (fixed, 280px): navigation + “Create new” CTA + list of recent rounds.

Top bar (in main area): round title + status pill + primary actions.

Main content (responsive): split into a builder area + details panel (optional) depending on page.

Right details drawer (optional, 360px): contextual editor for the selected proposal/field.

Style direction

Minimal, high-contrast, “security product” vibe.

Soft surface layers, subtle borders, rounded corners, monospaced JSON areas.

Primary accent: emerald/cyan (suggests “shielded/private”) + neutral grays.

Sidebar (left)
Header

App name: Shielded Vote Creator

Small subtitle: “Private voting rounds & proposals”

Primary button: + New voting round

Secondary: Import JSON (optional but useful)

Navigation items

Voting rounds

“All rounds”

“In progress”

“Drafts”

“Archived”

Results & exports

“Raw JSON”

“Downloads”

Settings (optional)

“Defaults” (Support/Oppose, vote weighting, etc.)

“Recent rounds” list (beneath nav)

Each row:

Round name (1 line, truncates)

Status pill: Draft / In Progress / Published

Timestamp: “Edited 2h ago”

Tiny progress indicator: “3 proposals”

Empty state:

“No rounds yet. Create your first shielded voting round.”

Page 1: Voting rounds list

Main header

Title: “Voting rounds”

Search box: “Search rounds…”

Filter chips: Draft / In progress / Published

Round cards

Round name

Status pill

Proposal count

Last edited

Buttons: Open, overflow menu (Duplicate / Export JSON / Archive)

Page 2: Round builder (core experience)
Top bar (sticky)

Left:

Round name (editable inline)

Status pill: Draft / In progress / Published

Small “autosaved” indicator

Right actions:

Primary: Publish round (disabled until validations pass)

Secondary: Preview

Secondary: Export JSON

Icon: “⋯” (Duplicate / Archive / Delete)

Main content structure

Two-column (responsive collapses to stacked on mobile):

Left: Proposal list + structure

Right: Proposal editor

Left column: Proposal list

Header row:

“Proposals”

Button: + Add proposal

Button: + Add from template (Support/Oppose, Multi-choice, Ranked, etc. optional)

Proposal list item design:

Number badge (01, 02…)

Title (editable on click)

Tiny label for type: “Binary” / “Multi-choice”

Validation indicator:

✅ ready

⚠ missing fields

Drag handle for reorder

Interactions:

Click selects proposal (loads editor on the right).

Drag reorders.

Overflow menu per proposal: Duplicate / Delete

Empty state (before adding any):

Illustration + “Add your first proposal”

CTA: Add Support/Oppose proposal

Right column: Proposal editor (selected proposal)

Section A: Proposal basics

Field: Title (required)

Field: Text with tabs:

Write (markdown editor)

Preview (rendered markdown)

Helper text: “Markdown supported. Links, lists, headings.”

Section B: Options

Toggle: Proposal type

Binary (Support/Oppose)

Multi-choice

If Binary:

Options locked by default: Support / Oppose

Button: “Customize labels” (optional)

If Multi-choice:

List of choices:

Text input + delete icon

“+ Add choice”

Validation: at least 2 choices

Section C: Advanced (collapsible)

Proposal ID (auto-generated, copy button)

Optional metadata (key/value):

category, tag, URL, forum link, etc.

“Allow abstain” toggle (optional)

Footer actions (right panel):

Save (if not autosave)

Delete proposal

Round-level settings panel (either a tab or a right-side drawer)

Accessible via a “Round settings” button near the top.

Fields:

Round description (markdown)

Voting window:

Start time / End time (or “Open until manually closed”)

Defaults:

Default proposal type (Binary / Multi-choice)

Default labels (Support/Oppose)

Privacy / shielding metadata (display-only for now):

“Shielded balances: enabled”

“Keystone compatible: yes”

“Homomorphic totals: enabled”

“Warnings” section if misconfigured

Page 3: Raw JSON view (copy/paste friendly)
Layout

Header: “Raw JSON”

Subtitle: “This is the canonical export format for this round.”

Buttons:

Copy JSON

Download .json

Validate (runs schema validation, shows results)

JSON viewer panel

Full height code block with:

Syntax highlighting

Line numbers

Collapse/expand nodes

Monospace font

Small sticky bar at bottom:

“Schema: v1”

“Last generated: timestamp”

“Valid ✅” or “Errors ⚠”

Errors UI (if invalid)

Red banner: “3 issues found”

List of issues with:

JSON path (e.g., round.proposals[2].options)

Description

Click jumps to relevant builder field

Download flow

Click Download .json

Default filename: shielded-vote-round_<round-slug>_<YYYY-MM-DD>.json

Optional: “Include debug metadata” checkbox (off by default)

Also include a small “Downloads” page that lists recent exports (optional).

Key UX details that make it feel “premium”

Autosave with “Saved ✓” and “Saving…” states.

Keyboard-first: Cmd+K opens command palette:

Add proposal, Export JSON, Copy JSON, Preview.

Validation as you go:

Sidebar shows small “n issues” badge on the active round.

Preview mode:

Read-only view of proposals as a voter would see them.

Nice empty states everywhere.

Suggested IA (routes)

/rounds — list

/rounds/:id/builder — main builder

/rounds/:id/json — raw JSON

/rounds/:id/preview — preview

/downloads — optional export history

Microcopy (useful text)

“Your vote round is a container for one or more proposals.”

“Markdown supported for proposal text.”

“Exported JSON is deterministic and easy to copy/paste.”


