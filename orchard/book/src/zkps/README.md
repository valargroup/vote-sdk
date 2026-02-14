# ZKP docs (symlinked from Obsidian)

The three `zkp*.md` files in this directory are symlinked from the written spec and are gitignored.

To build the book with full ZKP content, create symlinks (adjust source path if needed):

```bash
ln -sf /Users/roman/Documents/zcaloooors/shielded_vote_book/zkps/zkp1-delegation-proof.md zkp1-delegation-proof.md
ln -sf /Users/roman/Documents/zcaloooors/shielded_vote_book/zkps/zkp2-vote-proof.md zkp2-vote-proof.md
ln -sf /Users/roman/Documents/zcaloooors/shielded_vote_book/zkps/zkp3-vote-reveal-proof.md zkp3-vote-reveal-proof.md
```

Run from this directory (`orchard/book/src/zkps/`).
