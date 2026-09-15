# Development workflow

This session runs the [superpowers](https://github.com/obra/superpowers)
skill set. Enter at whichever stage the work has already reached rather
than starting from the top every time:

| What already exists | Start with |
|---|---|
| A spec, no plan | `writing-plans`, then `executing-plans` |
| A spec and a plan | `executing-plans` |
| A beads issue only | `brainstorming` → spec → `writing-plans` → `executing-plans` |
| Nothing written down | ask what the session is for, record it, then as above |

Before handing back, once execution is done:

1. Run `refactor:scan` against what changed and fix what it finds.
2. Run `commit-tools:review-commits` to fold any TDD-churn commits into
   logical units.

If a step's tooling is not installed on this instance, say so and carry
on rather than stopping — getting it here is a separate concern, and a
missing cleanup step is not a reason to leave the work unfinished.
