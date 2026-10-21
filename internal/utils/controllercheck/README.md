# Controller lifecycle checks

`controllercheck` runs generated lifecycle histories against a controller
contract. It uses Rapid for action generation, shrinking, and seed replay.

A `Contract` defines read-only `Always` and `Eventually` rules. A `Campaign`
provides the world: authoritative snapshots, lifecycle actions, effect
observers, recovery, progress, and restart. `Explore` runs each rule as an
independent subtest over fresh worlds.

Call the observer at every committed effect, including when the effect's reply
is lost. The runner retains the first safety violation even if later effects
repair the state. Recovery must explicitly establish the assumptions needed for
convergence; it has a finite step bound and checks stability after restart.

`Queue[K]` models deduplication, deadlines, and retries with virtual time.
Supply a comparison function for deterministic key ordering. It does not model
parallel workers or client-go's exact rate limiter.

`Faults` injects one-shot failures before a commit or after it commits but
before its reply is delivered. Unexpected errors must fail the campaign,
including errors joined with an injected failure. `Trace` records actions and
effects for counterexample diagnosis.

The harness tests run in the existing `make test` target. Controller campaigns
can use an environment-variable gate while keeping cheap properties in the
normal unit suite. No separate test runner is required.
