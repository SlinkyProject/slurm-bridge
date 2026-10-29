## v1.3.0-rc1

### Added

- Added pods/finalizers resource to RBAC rules.
- Use cosign to sign image artifacts.
- Generate an SBOM that is included in OCI artifacts.
- Added the ability to install slurm by ref in kind.sh.
- Added priority as an annotation for workloads scheduled by Slurm-bridge.
- Added Helm unittests.
- Add dynamic topology reconciliation for external Slurm nodes.
- Added Kind topology example configuration for external and hybrid bridge
  nodes.
- Merge Slurm job annotations from Workload, Job, and PodGroup for PodGroup
  scheduling.
- Add documentation to support hybrid or external node configuration.
- Added PodDisruptionBudgets for deployments.
- Validate supported DRA resources at admission time.
- Support using an existing Kubernetes cluster for development.
- Map Kubernetes Job activeDeadlineSeconds to the Slurm job time limit.
- Added admission validation that rejects Slurm annotation overrides conflicting
  with explicit DRA resource requests.
- Added configurable ImagePullPolicy.
- Support leader election in controllers deployment.
- Admission fails when unsupported pod `.spec.topologySpreadConstraints` is set.
- Slurm-minimum-version annotation in helm chart.
- Added support for SLURM_JWT rotation.
- Admission warnings for unsupported DRA DeviceClasses.
- Slurm node GRES inventory updates from ResourceSlices.
- Node DRA info is stored in the the slurm node Comment field.
- Allocate indexed GRES using new DRA backend.
- Added e2e tests for Slinky components.
- NVIDIA GPU allocations use the new DRA backend.
- CPU allocation uses new DRA backend.
- Install the NVIDIA GPU DRA driver in Kind fixtures.
- Enable opt-in mock NVIDIA GPUs on Kind workers.
- Add an optional KWOK scale-testing fixture.
- Expose controller, admission webhook, and scheduler metrics.
- Add optional kube-prometheus-stack metrics collection for development.
- Add a Grafana dashboard for Slurm Bridge component metrics.
- Add comprehensive scheduler and controller-runtime Grafana dashboards.
- Configure DRA device profiles from shared YAML.
- Report overlapping DRA device profiles on Kubernetes Nodes.
- Install DRANET as an optional Kind development fixture.
- Create selectable DRANET interfaces on managed Kind workers.
- Select Kind DRANET interfaces through a shared device profile.
- Accept duration and Slurm --time values for the timelimit annotation.
- CONTRIBUTING.md release notes section.
- Kubernetes 1.37 support.
- Validate hybrid GRES inventory and block incompatible bridge nodes.
- Restrict bridge jobs to nodes with compatible Slurm GRES configuration.
- Required pod (anti-)affinity is now rejected, not just required node affinity.
- Added well-known identifiers for heterogeneous Slurm jobs.
- Exposed Slurm JWT secret key selector.
- Warn when using deprecated v1alpha1 PodGroups.
- Allow the scheduler to read RayClusters so KubeRay pods schedule.
- Add optional informer-based Kubernetes node lookup.
- Adapt native PodGroup reads and status updates across supported API versions.
- Added support for scheduling CompositePodGroups.
- Added CompositePodGroup examples.
- Warn when LWS Pods rely on implicit gang scheduling.
- Allow rejecting legacy PodGroups and implicit LWS gangs at admission.
- Stop Slurm Bridge before Helm upgrades.
- Added support for scheduling basic CompositePodGroups.
- Allocate exact CPU sets across shared socket devices using Slurm topology.
- Register held Slurm allocations before Kubernetes scheduling.
- Prioritize workloads whose Slurm allocations are ready.
- Enabled native LeaderWorkerSet scheduling in development clusters.
- Document host-installed hybrid Slurm nodes.
- Prefer NUMA-aligned CPUs, GPUs, and NICs for DRA workloads.

### Fixed

- Don't delete placeholder jobs for terminating pods.
- Updated google.golang.org/grpc to v1.79.3 to address CVE-2026-33186.
- Fixed cases where an out of bounds panic could occur.
- Fix external node creation with correct CPU topology.
- Update opentelemetry to resolve CVE-2026-39883.
- Fixed reconciliation of Slurm nodes to prevent the deletion of non-external
  nodes.
- GO-2026-4864 GO-2026-4865 GO-2026-4866 GO-2026-4869 GO-2026-4870 GO-2026-4946
  GO-2026-4947.
- GO-2026-4918 GO-2026-4971 GO-2026-4976 GO-2026-4977 GO-2026-4980 GO-2026-4981
  GO-2026-4982 GO-2026-4986.
- GO-2026-5005 GO-2026-5006 GO-2026-5013 GO-2026-5014 GO-2026-5015 GO-2026-5016
  GO-2026-5017 GO-2026-5018 GO-2026-5019 GO-2026-5020 GO-2026-5021 GO-2026-5023
  GO-2026-5024 GO-2026-5025 GO-2026-5026 GO-2026-5027 GO-2026-5028 GO-2026-5029
  GO-2026-5030 GO-2026-5033.
- Prevent pods with non-existent resource requests from being bound by
  slurm-bridge.
- Fixes bug whereby multiple ResourceClaims were created after claim bind
  failures.
- GO-2026-5037 GO-2026-5038 GO-2026-5039.
- Removed namespace from jwtKeyRef.
- Fixed nodeSelector and affinity fields for deployments.
- Fix behavior so that external node feature update no longer triggers
  reconfigure.
- Fix namespace override.
- Fix external job update code path from being unreachable.
- Protect against a race condition where an external job update may occur but
  the external job has started.
- Allow pods to be annotated if an external job update race occurs and the job
  is already running.
- Fixed kind.sh nodes not having the necessary GresType for the example gpu dra
  driver.
- Omit empty CPU DRA requests and fail on incomplete allocations.
- Fix pods going into unschedulable queue for situations in which they should
  stay in the backoffq/activeq.
- Adds namespace:get RBAC to admission controller.
- Remove pod RBAC for admission controller.
- Remove node:update RBAC for node controller.
- Removed deviceclasses RBAC from node controller.
- Removed pods:update RBAC for pod controller.
- Added pods/finalizers rbac to pod controller.
- Removed unnecessary core RBAC.
- Removed volumeattachment RBAC.
- Removed RBAC for bindings.
- Removed RBAC for NodeResourceTopologies.
- Remove RBAC for workload status subresources.
- Remove create/list/watch from workload RBAC.
- Fix apigroup for SubjectAccessReviews.
- Removed node:patch RBAC from scheduler.
- Removed pod:delete RBAC from scheduler.
- Removed pods/finalizers:update RBAC from scheduler.
- Removed pods/status:update RBAC from scheduler.
- Removed resourceclaims/binding RBAC from scheduler.
- Removed resourceclaims/status:update RBAC from scheduler.
- Resolved DRA prebind when kube and Slurm node names differ and allocation
  pools do not match node names.
- Fixed config changes not rolling out after a helm upgrade.
- Skip empty DRA claims and use pod resource names in mappings.
- Match DRA mappings only to full device class names.
- Mark assigned PodGroups as scheduled.
- Delete bound pods after their Slurm placeholder job stops running.
- Reject unsupported DRA DeviceClass requests from multiple containers.
- Keep DRA GPU request names consistent across claims, mappings, and
  allocations.
- Limit DRA claims and allocations to the GRES requested by each pod.
- Fixed repeated termination of jobs.
- The node controller will watch resourcesliecs for changes to external nodes in
  order to update them.
- Replace /readyz probe with StartedChecker instead of a ping.
- Correct the readinessProbe to use /readyz.
- GO-2026-4970 GO-2026-5856.
- Pods with some invalid owners in the owner tree can now be scheduled.
- GO-2026-5942 GO-2026-5970.
- GO-2026-5158.
- GO-2026-6061.
- Set the Helm chart appVersion to the slurm-bridge release version.
- Add a 5m timeout to all slurm rest calls.
- GO-2026-5026 GO-2026-5942 GO-2026-5972 GO-2026-6088 GO-2026-6089 GO-2026-6090
  GO-2026-6091 GO-2026-6218.
- Slurm annotations can now be set on jobsets with PodGroups.
- External jobs shouldn't require all nodes.
- GO-2026-6094 GO-2026-6107.
- Pin and reconcile the NVIDIA mock fixture.
- GO-2026-6354 GO-2026-6355.
- Distinguish default and explicitly empty DRA profile registries.
- Pod reconcile no longer deletes a Pod on a stale cache miss, only on a
  live-confirmed not-found job.
- Apply controllers.resources to the controllers container.
- Report configuration parse failures without an unrecovered panic.
- PodDisruptionBudgets no longer set both minAvailable and maxUnavailable.
- Support built-in Workload APIs on Kubernetes 1.36 and 1.37.
- An empty `schedulerConfig.schedulerName` now falls back to the default.
- Debug-mode config path is configurable via --slurm-bridge-config.
- Preserve protobuf for normal scheduler API requests.
- Prevent new hybrid jobs when DRA inventory or GRES compatibility checks fail.
- Ignore incomplete DRA pools that are inaccessible to the node.
- Avoid recreating external nodes when Slurm reorders GRES entries.
- Stop advertising node GRES compatibility after inventory errors.
- Preserve node inventory while per-device DRA pools are incomplete.
- Correct broken links, typos, and install commands in the documentation.
- The scheduler PodDisruptionBudget no longer blocks node drains.
- GO-2026-6348 GO-2026-6441 GO-2026-6443.
- Match Slurm host-list expansion for scheduling and device allocations.
- Registering an external Slurm node no longer leaves its NodeAddr defaulting to
  an unresolvable hostname.
- Gang quorum could reject valid jobs mid-labeling.
- Map DRA devices to the correct Slurm GRES indexes across profiles.
- Reject DRA ResourceSlice pools without a single explicit node.
- Preserve driver-named GPU GRES types.
- Diagnose conflicting singleton driver selectors.
- Preserve Slurm GRES index offsets in DRA inventory mappings.
- The self-signed admission webhook certificate is now reused across renders.
- Exclude development files from the packaged Helm chart.
- Ensure annotatePodsWithNodes is idempotent.
- Only log an invalid-label deletion when the pod carries that label.
- Running pods no longer get stuck on jobid/node admission denials.
- GO-2026-6107.
- Collect e2e failure diagnostics from the selected Kubernetes context.
- PodGroup topology constraints and resource claims are rejected clearly.
- GO-2026-6505 GO-2026-6505 GO-2026-6505.
- Bound apiserver calls in the scheduler's job translation with a 10s timeout.
- Cache PodGroup Gets in the scheduling hot path to cut apiserver calls.
- Render GHFM alerts correctly in generated docs.
- A deleted sibling no longer blocks node and label assignment for its gang.
- Preserve MCS isolation when updating pending bridge jobs.
- Gang pods waiting for siblings wait for an event instead of retrying blindly.
- Avoid redundant node patches during taint reconciliation.
- Native gang PodGroups no longer lose their Slurm job while members are being
  labeled.
- Preserve concurrent node taint changes during reconciliation.
- Bound memory used by the pending job update cache.
- Retry pending job updates after a failed Slurm request.
- GO-2026-6599 GO-2026-6600 GO-2026-6603 GO-2026-6603 GO-2026-6604 GO-2026-6605
  GO-2026-6607 GO-2026-6608 GO-2026-6609 GO-2026-6610 GO-2026-6610 GO-2026-6611
  GO-2026-6611 GO-2026-6612 GO-2026-6612 GO-2026-6613 GO-2026-6617 GO-2026-6617.
- Fixed annotation parsing for heterogeneous podgroups.
- Ensure all HetJob components pass PostFilter before submitting a Slurm job.
- Clean up stale Slurm jobs when pod names are reused.
- Preserve valid Slurm jobs when the Kubernetes pod cache lags.
- Stop listing all Slurm nodes on every Node taint reconcile.
- Use cluster credentials for the pre-upgrade shutdown hook.
- Skip Slurm node drain for Bridge-drained nodes.
- Avoid a 30-second delay in scheduling after startup.
- Retain DRA reservations until Slurm cancellation succeeds.
- Reject unsupported exclusive CPU DRA allocations before Slurm submission.
- Allow existing legacy Pod updates after disabling admission feature gates.
- Retry draining nodes every 30 seconds without reconciliation errors.
- Prevent retained terminal pods from blocking bridge upgrades.
- Handling for pods with missing hetjob offset.
- Fixed external-job consistency when compositepodgroup quorum is lost.
- Ensured that controller-annotation selection always references the intended
  PodGroup.
- Job updates no longer fail on clusters without slurmdbd.
- GO-2026-6809.
- Build bridge images with the current Go 1.27 patch release.
- Stop Kind setup when cluster discovery fails.
- Recomputed Slurm allocations after an allocated Kubernetes node is deleted.
- Confirmed missing allocated nodes with live Kubernetes reads before canceling
  Slurm jobs.
- Explain Slurm registration failures and quorum waits with workload events.
- Show gated Pods where to find group registration events.
- Allow subsequent PodGroup batches without counting terminal Pods.
- Preserve the requested bridge version when rerunning Kind setup.
- Preserved LeaderWorkerSet group sizes during rolling updates.
- Retain gang allocations until their current members finish.
- Clean up LWS workers after their leader disappears.
- Release obsolete pending composite allocations.
- Preserve live siblings when a heterogeneous job leader finishes.
- Avoid repeated Slurm reads while new allocations enter the cache.
- Configure the native LWS example to avoid sequential cleanup deadlocks.
- Avoid repeated Slurm reads after allocation cleanup completes.
- Disable GenericWorkload automatically on Kubernetes 1.35.
- Preserve NUMA-aware resource ordering when scheduling native gangs.
- Prevent Pod binding when Slurm resource lookup fails.
- Avoid delayed scheduling and stale device mappings.
- GO-2026-6860 GO-2026-6866.

### Changed

- Slurm-bridge is now deployed to the slurm namespace instead of slinky.
- Disabled SchedulerPopFromBackoffQ.
- PostFilter now uses list to scan slurm nodes.
- Require an MCS label and enforce MCS for non-exclusive hybrid workloads.
- Limit excluded nodes to nodes belonging to the target slurm partition.
- Controllers back off from failures starting at 1s instead of 5ms.
- Pod reconciles no longer force a live Slurm job read on every check, cutting
  reconcile latency for the most frequently hit path.
- Use built-in DRA profiles by default in Helm deployments.
- Require Workload APIs by default with an explicit feature-gate opt-out.
- RefreshCache reads on the bumped slurm-client detect completion in up to 50ms
  instead of up to syncPeriod/30 (1s at the default 30s syncPeriod).
- External-mode dev fixture now tunes Slurm's backfill interval and gives
  slurmctld/slurmrestd a small guaranteed CPU share.
- External-mode dev fixture raises default_queue_depth from 500 to 2000 to stop
  capping the main scheduler under deep queues.
- Report hybrid GRES mismatches only through the Kubernetes node condition.
- Use Slurm features as the only bridge job GRES compatibility gate.
- Represented Slurm workloads with component-scoped job information.
- Update slurmbridge plugin for component-based SlurmJobIR.
- Scheduler plugin client QPS/Burst is now configurable via helm, defaulting to
  50/100 instead of client-go's bare 5/10, applied to all of the plugin's
  Kubernetes writes.
- Require explicit configuration for the example GPU driver.
- Build with Kubernetes 1.37 and retain per-Pod scheduling.
- Cluster-scoped RBAC objects are named per release, so two can coexist.
- Register pending gangs while Bridge-managed nodes are busy.
- Pod controller worker count is now configurable.
- PreEnqueue skips the pod patch when the toleration is already present.
- Reduce no-op node reconcile latency.
- Pending Slurm jobs are no longer updated when the request is unchanged.
- Update PodGroup annotation precedence.
- Set 128 component limit for hetjobs.
- Reduced overhead of podsForPodGroup.
- Scope admission webhooks to bridge-managed pods.
- Store compact pod identities in Slurm job comments.
- Updated the Go build toolchain to 1.27.2.
- Updated JobSet integration and development installs to v0.13.0.
- Updated LeaderWorkerSet integration to v0.11.1.
- Use the upstream Kubernetes image for upgrade hooks.
- Use a pinned grouped CPU driver for development and end-to-end tests.
- Drop the Enable prefix from legacy admission feature gates.
- Avoid draining nodes when DRA registration fails.
- The account annotation applies only at job submission.
- Document supported consecutive minor upgrade paths.
- Avoid waits after Slurm job updates and cancellation.
- Updated LeaderWorkerSet to v1.0.0.
- Default allocation lifecycle and node controllers to 10 workers.
- Use cached Slurm allocation and node discovery during scheduling.
- Share scheduling state across native gang members.
- Enable native gang scheduling by default and restore 30-second polling.
- Schedule native gangs without per-Pod node annotation writes.
- Share Slurm validation across native gang bindings.
