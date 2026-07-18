<!-- GSD:project-start source:PROJECT.md -->

## Project

**Home Tutor Agent**

A web-based real-time dictation tutor agent that provides natural, interactive learning experiences for elementary school students. The system uses conversational AI to deliver personalized vocabulary practice through voice dictation sessions, with parent feedback integration that drives FSRS (Free Spaced Repetition Scheduler) algorithm updates.

**Core Value:** Students receive personalized, real-time dictation practice with immediate feedback while parents get detailed progress tracking and the ability to influence the learning algorithm through their reviews.

### Constraints

- **Infrastructure**: 1C 1G VPS with strict memory limits (150MB for Go service, 50MB audio buffer pool)
- **Target Platform**: iPad web browser (Safari compatibility required)
- **Audio Technology**: Full streaming TTS approach (consistency over caching), Doubao/XiaoMimiMo APIs
- **Language Stack**: Go for WebSocket service, Python for existing business logic integration
- **Storage**: SQLite database (sufficient for current needs, avoids PostgreSQL overhead)
- **Latency Target**: <1.5 seconds acceptable for dictation responses
- **Architecture**: Must support future digital avatar integration (Phase 3)
- **Concurrency**: Support 50-100 concurrent sessions within resource constraints

<!-- GSD:project-end -->

<!-- GSD:stack-start source:STACK.md -->

## Technology Stack

Technology stack not yet documented. Will populate after codebase mapping or first phase.
<!-- GSD:stack-end -->

<!-- GSD:conventions-start source:CONVENTIONS.md -->

## Conventions

Conventions not yet established. Will populate as patterns emerge during development.
<!-- GSD:conventions-end -->

<!-- GSD:architecture-start source:ARCHITECTURE.md -->

## Architecture

Architecture not yet mapped. Follow existing patterns found in the codebase.
<!-- GSD:architecture-end -->

<!-- GSD:skills-start source:skills/ -->

## Project Skills

| Skill | Description | Path |
|-------|-------------|------|
| ask-matt | Ask which skill or flow fits your situation. A router over the skills in this repo. | `.agents/skills/ask-matt/SKILL.md` |
| code-review | Review the changes since a fixed point (commit, branch, tag, or merge-base) along two axes — Standards (does the code follow this repo's documented coding standards?) and Spec (does the code match what the originating issue/PRD asked for?). Runs both reviews in parallel sub-agents and reports them side by side. Use when the user wants to review a branch, a PR, work-in-progress changes, or asks to "review since X". | `.agents/skills/code-review/SKILL.md` |
| codebase-design | Shared vocabulary for designing deep modules. Use when the user wants to design or improve a module's interface, find deepening opportunities, decide where a seam goes, make code more testable or AI-navigable, or when another skill needs the deep-module vocabulary. | `.agents/skills/codebase-design/SKILL.md` |
| design-an-interface | Generate multiple radically different interface designs for a module using parallel sub-agents. Use when user wants to design an API, explore interface options, compare module shapes, or mentions "design it twice". | `.agents/skills/design-an-interface/SKILL.md` |
| diagnosing-bugs | Diagnosis loop for hard bugs and performance regressions. Use when the user says "diagnose"/"debug this", or reports something broken/throwing/failing/slow. | `.agents/skills/diagnosing-bugs/SKILL.md` |
| domain-modeling | Build and sharpen a project's domain model. Use when the user wants to pin down domain terminology or a ubiquitous language, record an architectural decision, or when another skill needs to maintain the domain model. | `.agents/skills/domain-modeling/SKILL.md` |
| grill-me | A relentless interview to sharpen a plan or design. | `.agents/skills/grill-me/SKILL.md` |
| grill-with-docs | A relentless interview to sharpen a plan or design, which also creates docs (ADR's and glossary) as we go. | `.agents/skills/grill-with-docs/SKILL.md` |
| grilling | Grill the user relentlessly about a plan, decision, or idea. Use when the user wants to stress-test their thinking, or uses any 'grill' trigger phrases. | `.agents/skills/grilling/SKILL.md` |
| handoff | Compact the current conversation into a handoff document for another agent to pick up. | `.agents/skills/handoff/SKILL.md` |
| implement | "Implement a piece of work based on a spec or set of tickets." | `.agents/skills/implement/SKILL.md` |
| improve-codebase-architecture | Scan a codebase for deepening opportunities, present them as a visual HTML report, then grill through whichever one you pick. | `.agents/skills/improve-codebase-architecture/SKILL.md` |
| prototype | Build a throwaway prototype to answer a design question. Use when the user wants to sanity-check whether a state model or logic feels right, or explore what a UI should look like. | `.agents/skills/prototype/SKILL.md` |
| qa | Interactive QA session where user reports bugs or issues conversationally, and the agent files GitHub issues. Explores the codebase in the background for context and domain language. Use when user wants to report bugs, do QA, file issues conversationally, or mentions "QA session". | `.agents/skills/qa/SKILL.md` |
| research | Investigate a question against high-trust primary sources and capture the findings as a Markdown file in the repo. Use when the user wants a topic researched, docs or API facts gathered, or reading legwork delegated to a background agent. | `.agents/skills/research/SKILL.md` |
| resolving-merge-conflicts | "Use when you need to resolve an in-progress git merge/rebase conflict." | `.agents/skills/resolving-merge-conflicts/SKILL.md` |
| setup-matt-pocock-skills | Configure this repo for the engineering skills — set up its issue tracker, triage label vocabulary, and domain doc layout. Run once before first use of the other engineering skills. | `.agents/skills/setup-matt-pocock-skills/SKILL.md` |
| tdd | Test-driven development. Use when the user wants to build features or fix bugs test-first, mentions "red-green-refactor", or wants integration tests. | `.agents/skills/tdd/SKILL.md` |
| teach | Teach the user a new skill or concept, within this workspace. | `.agents/skills/teach/SKILL.md` |
| to-spec | Turn the current conversation into a spec and publish it to the project issue tracker — no interview, just synthesis of what you've already discussed. | `.agents/skills/to-spec/SKILL.md` |
| to-tickets | Break a plan, spec, or the current conversation into a set of tracer-bullet tickets, each declaring its blocking edges, published to the configured tracker — edges as text in one file per ticket locally, or native blocking links on a real tracker. | `.agents/skills/to-tickets/SKILL.md` |
| triage | Move issues and external PRs through a state machine of triage roles — categorise, verify, grill if needed, and write agent-ready briefs. | `.agents/skills/triage/SKILL.md` |
| wayfinder | Plan a huge chunk of work — more than one agent session can hold — as a shared map of decision tickets on your issue tracker, and resolve them one at a time until the way to the destination is clear. | `.agents/skills/wayfinder/SKILL.md` |
| writing-great-skills | Reference for writing and editing skills well — the vocabulary and principles that make a skill predictable. | `.agents/skills/writing-great-skills/SKILL.md` |
<!-- GSD:skills-end -->

<!-- GSD:workflow-start source:GSD defaults -->

## GSD Workflow Enforcement

Before using Edit, Write, or other file-changing tools, start work through a GSD command so planning artifacts and execution context stay in sync.

Use these entry points:

- `/gsd-quick` for small fixes, doc updates, and ad-hoc tasks
- `/gsd-debug` for investigation and bug fixing
- `/gsd-execute-phase` for planned phase work

Do not make direct repo edits outside a GSD workflow unless the user explicitly asks to bypass it.
<!-- GSD:workflow-end -->

<!-- GSD:profile-start -->

## Developer Profile

> Profile not yet configured. Run `/gsd-profile-user` to generate your developer profile.
> This section is managed by `generate-claude-profile` -- do not edit manually.
<!-- GSD:profile-end -->
