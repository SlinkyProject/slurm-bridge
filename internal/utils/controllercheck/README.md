# Registration controller properties

The registration campaign uses the real `Reconciler` in a sequential world with
independent authoritative Slurm records and informer snapshots, Kubernetes fake
storage, a typed deterministic queue, and a virtual clock. Only the reusable
`controllercheck` package was imported from the Pod-cleanup prototype.

| File                                                                                                   | Responsibility                                                          |
| ------------------------------------------------------------------------------------------------------ | ----------------------------------------------------------------------- |
| [property_test_contract_test.go](../../controller/registration/property_test_contract_test.go)         | Safety rules, convergence, and recovery assumptions                     |
| [property_test_world_test.go](../../controller/registration/property_test_world_test.go)               | Actions, service fakes, fault boundaries, snapshots, and reconciliation |
| [property_test_workloads_test.go](../../controller/registration/property_test_workloads_test.go)       | Workload generators and independent allocation intent                   |
| [property_test_registration_test.go](../../controller/registration/property_test_registration_test.go) | Pure survivor, submission guard, and identity properties                |
| [property_test_allocation_test.go](../slurmjob/property_test_allocation_test.go)                       | Component matching under permutation and incomplete allocations         |
| [campaign.go](campaign.go)                                                                             | Independent properties, recovery, restart stability, and traces         |
| [queue.go](queue.go)                                                                                   | Generic queue with deduplication, deadlines, and deterministic ordering |

## Running

Run these commands from `slurm-bridge` with the workspace's direnv environment.
The existing unit target runs the pure properties, harness checks, and campaign:

```sh
GOWORK=off make test
```

No environment variable or separate Make target is needed. `make test` disables
test-result caching so each campaign executes afresh. The campaign defaults to
Rapid's 100 histories per rule and average 100 steps. For a longer nightly
campaign or a single-rule replay, invoke the Go test directly:

```sh
GOWORK=off go test -race \
  ./internal/controller/registration -run '^TestRegistrationContract$' \
  -count=1 -rapid.checks=1000 -rapid.steps=300

GOWORK=off go test -race \
  ./internal/controller/registration \
  -run '^TestRegistrationContract/always/duplicate-cancellation$' \
  -count=1 -rapid.seed=1 -rapid.checks=100 -rapid.steps=300
```

Rapid prints replay seeds and shrinks failures. Keep the step count when
replaying seeds. Saved counterexamples go under the test package's
`testdata/rapid` directory; `-rapid.nofailfile` disables saving. Every attempt,
including shrinking, starts with a fresh world. Each contract rule is a separate
subtest, so one failing rule does not prevent the others from running.

## Contract and scope

The campaign creates standalone Pods, Job-owned Pods, native gang PodGroups,
CompositePodGroups with two or three heterogeneous components, and LWS parents
with two or three independent replicas. Gang components and LWS replicas each
have two or three members. Pod names can be reused after deletion. The model
does not run the scheduler, Pod cleanup controller, or workload controllers. It
does not model parallel controller workers or arbitrary stale Kubernetes reads.
Histories start empty, registered, or running; the latter two phases are reached
through the real reconciler before disturbances begin. This gives later actions
such as binding useful exposure during histories, instead of registering most
workloads only during final recovery. The test log reports completed action
counts.

Actions create workloads, delete Pods, advance the Slurm cache, process a ready
key, advance time, cancel or start a job externally, create a competing
allocation, inject a pre-upgrade allocation, bind one Pod, arm a fault, heal,
restart, and mark the informer unsynchronized. Slurm start and cancel actions
apply to the entire allocation, including every heterogeneous component.
Competing allocations model another writer or a replayed submission and exercise
duplicate recovery without depending on a rare random sequence of faults and
cooldown expiry. Faults occur before a mutation or after it commits but before
its reply reaches the controller. Slurm submission writes held records using the
real `ExternalJobInfoFor`; a lost submission reply returns ID zero. Kubernetes
patches and deletion, Slurm submission, and controller cancellation invoke the
campaign's observer after each committed effect.

Binding requires an unbound, non-deleting Pod with no scheduling gates and a job
label naming a running live allocation on the chosen node. It binds one member
at a time, so restarts can encounter partially bound gangs. Some generated Pods
carry an unrelated gate and cannot bind while it remains. The world never
performs automatic Pod cleanup when Slurm jobs finish.

Pre-upgrade injection targets complete, unbound native PodGroups. It writes a
released pending job with the original Pod UID list and no group identity,
matching the scheduler's pre-MR write format. Cache advancement discovers it
normally. The oracle associates legacy records with independently generated
membership, so adoption and conflicts with modern allocations use the same
contract. Composite groups had no released legacy format; older name-only
comments remain outside this campaign.

Snapshots contain stored Pods, authoritative Slurm records, recorded effects,
and immutable workload intent captured at creation (identity and component
membership). Expected membership is independent of the production IR translator.
They never query the current cache. Submission effects preserve the cache
records observed at that boundary so the contract can detect submission against
an already indexed unfinished allocation. They also preserve the authoritative
bound Pod keys at submission, so deleting or binding a Pod later cannot change
that evidence. Cancellation effects preserve the preceding live records so the
contract can verify that another unfinished allocation exists.

A label must identify a Slurm record listing that Pod UID. Cancellation retains
a terminal Slurm record: record ownership and unfinished-allocation liveness are
separate requirements. This model assumes Slurm does not purge records while
Pods still reference them. The gate must disappear in the patch publishing the
label; unrelated gates must survive. Controller cancellation requires another
unfinished allocation for the same identity; a sibling heterogeneous component
does not count as a survivor. The controller must never patch a bound Pod or
submit an allocation whose identity has a bound member.

Recovery stops new workloads, external state transitions, and faults. It fully
advances the informer, marks it synchronized, advances virtual time beyond the
submission cooldown, and fairly processes ready keys. Every allocation with its
required quorum must converge to one unfinished allocation, correct component
labels (including heterogeneous leader and offset), and no registration gate
within 32 recovery ticks. LWS replicas must converge independently. Generated
gangs require every original member; deleting one can remove quorum, and
recovery does not replace it. Safety still applies to incomplete gangs, but
convergence does not require submitting them. A running, partially bound gang
must still converge for its unbound members. If a bound member's job is canceled
externally, convergence excludes that allocation: submission is forbidden while
a member is bound, and cleanup belongs to the Pod controller. Recovery also
checks further progress and a fresh reconciler. Restart loses the queue and
cooldown cache but reconstructs events from Pod and synchronized Slurm startup
lists.

Only the injected transport error is retryable. Unexpected errors fail the
campaign, including errors joined with an injected error. A failure to converge
must be investigated as a controller defect or an explicit missing recovery
assumption; it must not be hidden with a scenario-specific exception.
