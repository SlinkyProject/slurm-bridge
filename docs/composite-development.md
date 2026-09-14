# CompositePodGroup development

This branch combines !394 and !438. The existing `hack/kind.yaml` enables the
native v1alpha3 CompositePodGroup API and its required feature gates. The bridge
has permission to read CompositePodGroups and update their status.

Composite development is the default. Create the normal Kind cluster, deploy,
and run the e2e suite with:

```sh
make kind-start test-e2e
```

This is an intentionally failing acceptance test for the implementation handoff.
It creates one native Workload, one CompositePodGroup, and two leaf PodGroups
with one Pod each (requesting one and two CPUs). It expects both Pods to run and
checks `scontrol` for one Slurm hetjob, two distinct components, and component
allocations matching the Pods' Kubernetes nodes. No opt-in flag or CRD is used.

Composite membership discovery and heterogeneous submission remain to be
implemented. !438 currently rejects multi-component submissions.
