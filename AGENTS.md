# Agent Instructions

- Keep tests few and load-bearing:
  - Test behavior that no single piece of code shows, such as invariants across components, concurrency, or I/O.
  - Don't test a lone guard, branch, ordering, or one-line rule; a comment explaining why guards it better.
  - A test must fail when what it guards breaks. Check by breaking the code or running against the base branch, and make it fail fast rather than hang.
  - Prefer one test that pins an invariant over several that restate its steps.
