# Session Development Workflow

A cloudlab session runs a coding agent using the
[superpowers](https://github.com/obra/superpowers) skill set:
`brainstorming`, `writing-plans`, `executing-plans`, plus
`refactor:scan` and `commit-tools:review-commits` for cleanup. Getting
that tooling onto the instance is [aide](https://github.com/jskswamy/aide)'s
problem, tracked separately -- this spec only names the workflow itself,
assuming the tooling is already there.

## Starting a session

A session starts at one of three points, depending on how much of the
work is already decided:

1. **Spec exists, no plan.** First step is `writing-plans`, then
   `executing-plans`.
2. **Spec and plan both exist.** Straight to `executing-plans`.
3. **A beads story or epic, nothing written yet.** The full pipeline:
   `brainstorming` -> spec -> `writing-plans` -> `executing-plans`.

## Before handing back

Once execution is done, before the session is ready to pull or merge:

4. Run `refactor:scan` against what changed, and fix what it finds.
5. Run `commit-tools:review-commits` to fold any TDD-churn commits into
   logical units.

Then `cloudlab session pull` / `cloudlab session merge` proceed as they
already do.

## Out of scope

- How the tooling (superpowers, refactor:scan, commit-tools) gets onto
  the instance, and how the agent authenticates there -- aide's problem.
- Multi-repo sessions (one session, several related repos, shared beads
  context): a related but separate line of design, not yet written up.
