# Worktree-owned Make Module Implementation Plan

**Goal:** Keep the existing wtree Make integration in the Worktree repository without adding a module to the canonical Makefiles repository.

**Architecture:** Preserve the existing module's target behavior and tests. A checkout-based bootstrap installs the module into a consumer's Makefiles cache and obtains only the canonical Makefiles core; remote publication and private release authentication remain unresolved.

## Constraints

- Do not alter the canonical Makefiles repository or overwrite unrelated cached modules.
- Preserve existing user files in the prior integration worktree.
- Keep `make wtree up`, `down`, `status`, `help`, and `clean` semantics; do not require Go for an already provisioned binary.
- Do not claim that private GitHub release downloads or macOS route activation work.

## Tasks

1. Port the module and fixture tests from the isolated Makefiles branch into this repository; preserve their security checks. A standalone example is not needed.
2. Provide checkout-based bootstrap that uses the canonical core without fetching an unrelated module or overwriting existing core, and test fresh and existing consumer caches offline.
3. Update Worktree onboarding and CI to run the module tests; clearly identify the missing remote distribution mechanism and platform smoke tests.
4. Run module tests, Go tests, vet, and a fresh review; preserve the original integration branch and untracked files.