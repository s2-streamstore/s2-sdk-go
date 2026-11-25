# Agent Instructions

- Keep tests few and load-bearing:
  - Test behavior that no single piece of code shows, such as invariants across components, concurrency, or I/O.
  - Don't test a lone guard, branch, ordering, or one-line rule; a comment explaining why guards it better.
  - A test must fail when what it guards breaks. Check by breaking the code or running against the base branch, and make it fail fast rather than hang.
  - Prefer one test that pins an invariant over several that restate its steps.
- Before opening a PR, review your own diff as a reviewer would:
  - Check it against the rules it touches in `s2-specs/sdk/spec.md`, on every code path that handles the same input, not only the one you edited.
  - For concurrent code, consider every interleaving and every caller, including repeated and concurrent ones.
  - When changing shared code, check its other callers.
  - Make sure each test fails without your fix, and that nothing else could make it pass.
  - Keep doc comments true to the new behavior.
