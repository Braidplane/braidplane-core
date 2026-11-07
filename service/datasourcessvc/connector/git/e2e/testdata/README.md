# Git E2E SSH test key material

The Ed25519 keypair used by Soft Serve E2E tests is generated at runtime by
scripts/gen-worktree-env.sh.

Default generated files:

- .worktree/git-keys/id_ed25519
- .worktree/git-keys/id_ed25519.pub

The key path can be overridden via GIT_TEST_SSH_KEY_PATH.
Generated key files are intentionally not committed.
