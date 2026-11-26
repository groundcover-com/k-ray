# Diagnosis rules

This file lists every rule that makes a likely cause, a problem line, a
health state or a difference block. It is for a reviewer who knows
Kubernetes and wants to check each rule against the code.

How to read it:

- **Where** is the file and the function. Paths are under `internal/`.
- **Trigger** is what the code checks, read from the code.
- **Prints** is a real example of the text. When a test pins the text, the
  example is the text of the test. Otherwise it is the format string of the
  code with sample values.
- **Fires** is a test that proves the rule prints. **Silent** is a test that
  proves the rule stays silent when it must. "none" means that no such test
  exists today.
- **Thresholds** are the numbers the rule uses. "Reason not stated" means that
  the code, the tests, the docs and the commit messages give no reason for
  the value. Both lists at the end collect these gaps.

The guard test `TestRulesTableNamesRealTests` (in `internal/guard`) fails when
a test name in this file does not exist in the repository.

The output of a rule passes the redactor before it is cut. The rules below do
not repeat this.

## 1. Pod causes

The deep diagnosis is `tools/diagnose.go` `podCauses` (with `light` false),
called by `diagnosePod` for `describe` and for the worst three rows of `list`.
The light diagnosis is the same function with `light` true, called by
`tools/causes.go` `lightPodCauses` for every pod row of `list`. The light mode
reads no logs, nodes, pods or ConfigMaps. The `switch` in `podCauses` stops at
the first case that matches, per container, in this order: OOM, image pull,
config error, exit code, liveness restart, not ready.

| ID | Rule | Where | Trigger | Prints | Fires | Silent | Thresholds |
|---|---|---|---|---|---|---|---|
| POD-1 | OOM kill at the limit | `tools/diagnose.go` `podCauses` | The last termination (`state.terminated`, else `lastState.terminated`) has reason `OOMKilled`, and the container has a memory limit. Adds the memory use when the metrics API has a sample above 0. The time of the last kill is left out while `crashOngoing` is true. No age check. | `container api is OOM killed at its memory limit of 1024Mi (14 restarts). Memory use now: 900Mi (88% of the limit).` | `TestDiagnoseOOMKill`, `TestOOMCauseSaysWhenTheLastKillWas`, `TestOOMInCrashLoopHasNoTime`, `TestFailedPodWithoutReason` | `TestSilentOldOOMOfReadyPodIsNotAWarning` (the pod row is healthy, so describe does not diagnose) | none |
| POD-2 | OOM kill with no limit | `tools/diagnose.go` `podCauses` | As POD-1, and the memory limit is 0. | `container cache is OOM killed and has no memory limit (2 restarts, last 3h ago): the node ran out of memory.` | none | none | none |
| POD-3 | Run time before the OOM kill | `tools/diagnose.go` `oomRunTime` | Added to POD-1. Uses `startedAt` and `finishedAt` of the termination. Under 2 minutes: a limit too low. 1 hour or more: a slow leak or a load that grew. Between, or times missing: the generic sentence. | `It ran for 10s before the kill, which points to a limit that is too low for the workload, not to a slow leak.` | `TestOOMCauseShowsRunTime` (under 2 minutes); `TestFailedPodWithoutReason` (generic sentence) | none | 2 minutes; 1 hour. Reason for the 2 minute split is stated in the code ("a kill soon after the start is not a slow leak"); the values are not. |
| POD-4 | Volume attached to another node | `tools/diagnose.go` `podCauses` | The pod is `Pending` and an event has reason `FailedAttachVolume` and a message that starts with `Multi-Attach error`. Names the claims of the pod. | `the pod cannot start because its volume is still attached to another node (Multi-Attach error, ×3). The volume (claim data) attaches to one node at a time (ReadWriteOnce). ...` | `TestDiagnoseMultiAttach`, `TestListRowsHoldTheirCause` | none | none |
| POD-5 | Image pull: access refused | `tools/diagnose.go` `imagePullCause` | The container waits with `ImagePullBackOff`, `ErrImagePull` or `InvalidImageName`. The waiting message, plus the newest `Failed` event that starts with `Failed to pull image`, contains (case-insensitive) `unauthorized`, `denied`, `authentication required`, `401`, `403` or `no basic auth credentials`. This case is checked first. | `container pay cannot pull its image: the registry refused access to reg/pay:1 (the pod has no imagePullSecrets).` | none | none | none |
| POD-6 | Image pull: not found | `tools/diagnose.go` `imagePullCause` | As POD-5, no POD-5 word, and the text contains `not found`, `manifest unknown` or `name unknown`. | `container pay cannot pull its image: the image or tag registry.example/pay:1.4.2 does not exist in the registry (not found).` | `TestDiagnoseImagePull`, `TestListRowsHoldTheirCause` | none | none |
| POD-7 | Image pull: registry not reachable | `tools/diagnose.go` `imagePullCause` | As POD-5, no earlier word, and the text contains `no such host`, `i/o timeout`, `connection refused`, `dial tcp`, `tls:` or `x509`. | `container pay cannot pull its image: the registry of reg/pay:1 is not reachable from the node.` | none | none | none |
| POD-8 | Image pull: name not valid | `tools/diagnose.go` `imagePullCause` | As POD-5, no earlier word, and the text contains `invalid reference format` or `invalidimagename`. | `container pay cannot pull its image: the image name reg/Pay:1 is not valid.` | none | none | none |
| POD-9 | Image pull: other | `tools/diagnose.go` `imagePullCause` | As POD-5, and no word of POD-5 to POD-8. Prints the image and the message. | `container web cannot pull its image: x:1: <the kubelet message, cut at 200 bytes>` | `TestCutImagePullMessageRedactsBeforeTheCut` | none | 200 bytes of message. Reason not stated. |
| POD-10 | Missing ConfigMap or Secret | `tools/diagnose.go` `podCauses`, `neighbors` | The container waits with `CreateContainerConfigError` or `CreateContainerError`, and the message matches `(secret\|configmap) "NAME" not found`. The deep mode adds the ConfigMaps of the namespace (similar names first, see `similar`) for a missing ConfigMap. | `container notifier cannot start: it refers to the configmap "notifier-settings", which does not exist in namespace pay. ConfigMaps in the namespace: notifier-config.` | `TestMissingConfigMapNamesTheNeighbors`, `TestListRowsHoldTheirCause` | none | 200 ConfigMaps read; 8 names shown; `similar`: a shared prefix of 4 bytes or more that is at least half of the shorter name. Reasons not stated. |
| POD-11 | Exit code | `tools/diagnose.go` `podCauses` | The last termination has an exit code other than 0, and the container waits in `CrashLoopBackOff`, or is terminated now, or has restarted. Adds the meaning of the code (table `exitMeaning`), the restart count and the time of the last exit (left out while `crashOngoing`). Adds the liveness probe failure when one belongs to the container (POD-14). No age check: an exit of long ago wins over a current not-ready state (see the report of findings). | `container app exits with code 1 (application error), 4 restarts, last 2h ago.` | `TestDiagnoseCrashShowsLastCrashLogs`, `TestExitCodeCauseSaysWhenTheLastExitWas`, `TestCrashBetweenRestartsHasNoTimeInTheTerminatedState`, `TestSidecarThatCrashesIsDiagnosed`, `TestProbeEventOfTheCrashingContainerIsItsOwn` | `TestSilentCompletedInitContainerAndALivenessEventElsewhere` | none |
| POD-12 | Last error before the exit | `tools/diagnose.go` `diagnosePod`, `lastCrash`, `containerLog` | Deep mode only. For the first container of POD-11 that restarted: read the previous instance's log (else the current log), find the last entry that is an error (section 10), and append its error line to the POD-11 cause. If the runtime answers that the container is between instances, wait and read once more. | `container app exits with code 1 (application error), 7 restarts. Last error before the exit: FATAL config: DATABASE_URL is not set` | `TestDiagnoseCrashShowsLastCrashLogs`, `TestLastErrorOfATracebackIsItsExceptionLine`, `TestLastErrorOfAJavaTraceNamesTheCause` | `TestSilentLastErrorIsNotAZeroErrorLine` | 200 lines; 256 KiB; quote cut at 360 bytes; summary 1,200 bytes; 400 ms wait before the second read. Reason for the second read is stated in the code (the runtime is between two instances); the values are not. |
| POD-13 | Restarted by the kubelet after liveness failures | `tools/diagnose.go` `podCauses` | The last termination has exit code 0, the container restarted, and a liveness probe event belongs to it (POD-14). Never for an init container that is not a sidecar. | `container cart is restarted by the kubelet because its liveness probe fails (×17, last 1m ago): command timed out: ... timed out after 1s. It has 2,986 restarts and stops with exit code 0 each time, so the application itself does not crash.` | `TestDiagnoseRestartsByLivenessProbe`, `TestRestartedPodsHaveTheirOwnRowWithTheCause`, `TestProbeEventIsAttachedToTheContainerItNames` | `TestSilentCompletedInitContainerAndALivenessEventElsewhere`, `TestProbeEventOfTheCrashingContainerIsItsOwn` | probe message cut at 200 bytes. Reason not stated. |
| POD-14 | A probe event belongs to one container | `tools/diagnose.go` `podCauses` (`probeEvent`), `containerOfFieldPath`, `probeOwners` | An `Unhealthy` event whose message starts with `<Kind> probe failed`. With `fieldPath` `spec.containers{NAME}` or `spec.initContainers{NAME}`: that container only. Without: the one container that defines that probe, or the only container of a pod in which no container defines it. The newest event wins. The event age is not compared with the time of the termination. | (no text of its own; it feeds POD-11, POD-13, POD-15, POD-17) | `TestProbeEventIsAttachedToTheContainerItNames`, `TestProbeEventWithoutFieldPath` | `TestProbeEventWithoutFieldPath` (two containers define the probe, no fieldPath: nothing) | none |
| POD-15 | Not ready: readiness probe | `tools/diagnose.go` `podCauses`, `probeText` | The container runs, is not ready, the pod phase is `Running`, and a readiness probe event belongs to it. Adds the probe of the spec. | `container web is not ready because its readiness probe fails (×42, last 50s ago): HTTP probe failed with statuscode: 503. Probe: HTTP GET /ready on port 8080.` | `TestDiagnoseProbeFailure`, `TestListRowsHoldTheirCause` | `TestSilentRollingDeploymentIsNotUnhealthyAndHasNoCause` (a new pod that is not ready and has no event gets no cause) | message cut at 200 bytes. Reason not stated. |
| POD-16 | Last error in the log of a container that is not ready | `tools/diagnose.go` `diagnosePod`, `containerLog` | Deep mode only. A cause of POD-15 or POD-17 exists for a running container that is not ready: read its current log and append the last error line. | `container web is not ready because its readiness probe fails (×42, last 50s ago). Last error in its log: dial tcp 10.0.0.5:5432: connection refused` | none | none | as POD-12 |
| POD-17 | Not ready: startup probe | `tools/diagnose.go` `podCauses` | As POD-15 with no readiness event, and a startup probe event belongs to the container. | `container web is not ready because its startup probe fails (×6): Startup probe failed: HTTP probe failed with statuscode: 500` | none | none | message cut at 200 bytes. Reason not stated. |
| POD-18 | Restart with no event | `tools/causes.go` `lightPodCauses` | Light diagnosis only, and only when `podCauses` found nothing. The first app container with a last termination and at least 1 restart. | `container quiet restarted 4 times; its last exit code was 0 (completed), 2h ago. No event is left that says why.` | `TestRestartedPodsHaveTheirOwnRowWithTheCause` | none | 1 restart |
| POD-19 | Init container that has not completed (short block) | `tools/describe.go` `briefInitCause` | A short block (ambiguous name) of a pod or workload with a problem, when the light diagnosis found nothing. The first init container that is not a sidecar and did not end with code 0. Names the addresses in its command and its last log line. | `pod/web-7f-abc: init container wait-db has not completed: running for 3h; its last log line: waiting for db:5432 password=[REDACTED:...]` | `TestAmbiguousNameBlocksCarryTheirCause`, `TestBriefInitCauseKeepsAnOrdinaryCommand` | `TestSilentSidecarNotNamedAsCauseOfAnotherProblem` | 20 log lines; 16 KiB; 160 bytes shown; 3 addresses. Reasons not stated. |
| POD-20 | No cause found (short block) | `tools/describe.go` `briefCauses`, `noCauseText` | A short block of a pod or workload that is unhealthy or warning, with no cause from POD-1 to POD-19 and no related problem or line. | `no cause found from pod state and events. For more: describe deployment web namespace=b.` | `TestAmbiguousNameBlockWithoutCauseSaysSo` | `TestAmbiguousNameBlocksCarryTheirCause` | none |
| POD-21 | A workload gives the cause of its worst pod | `tools/diagnose.go` `diagnoseWorkload` | The workload is not healthy (section 9). The worst pod is the first unhealthy pod, else the first warning pod, of the pods that its selector matches. Not for a CronJob. | `pod/api-7f9c8-x2k: container api is OOM killed at its memory limit of 1024Mi` | `TestDiagnoseWorkloadNamesThePodCause`, `TestUnhealthyDeploymentKeepsItsCause`, `TestListWorkloadsShowsThePodCause` | `TestSilentHealthyDeploymentGetsNoCause`, `TestSilentCompletedJobGetsNoCause`, `TestSilentRollingDeploymentIsNotUnhealthyAndHasNoCause` | 200 pods read; 5 pod lines. Reasons not stated. |
| POD-22 | Log of the failed pod of a Job | `tools/describe.go` `describeObj`, `failedContainerLog` | A Job that is not healthy, whose worst pod has no crash block: the first container that terminated with a code other than 0 and did not restart. | `last log lines of container app in the failed pod/migrate-abcde:` | `TestDescribeJobShowsItsFailedPod` | `TestSilentCompletedJobGetsNoCause` | none |
| POD-23 | Earlier line of a healthy pod | `tools/describe.go` `podEarlier` | The pod row is healthy. Each container (init and app) with at least 1 restart and either 3 or more restarts or a last termination `OOMKilled`. | `earlier: container app restarted 9 times, last 201d ago` | `TestSilentOldOOMOfReadyPodIsNotAWarning`, `TestSilentManyOldRestartsOfReadyPodIsNotAWarning` | `TestSilentPlainRestartsPrintNoEarlierLine` | 3 restarts. Reason not stated (the code says only that the old rules showed it as a warning). |
| POD-24 | Earlier line of a healthy workload | `tools/describe.go` `workloadEarlier` | The workload row is healthy, not a Job. The pod with the most restarts among the pods for which POD-23 prints. | `earlier: pod/web-1: container app restarted 3 times, last 2h ago` | `TestSilentHealthyDeploymentGetsNoCause` | `TestSilentPlainRestartsPrintNoEarlierLine` | as POD-23 |
| POD-25 | Earlier attempts of a healthy Job | `tools/describe.go` `workloadEarlier` | The Job row is healthy and the larger of `status.failed` and the number of pods in phase `Failed` is above 0. The text says that the run completed; the code does not check that. | `earlier attempts: 2 pods failed before the run that completed` | `TestSilentCompletedJobGetsNoCause` | none | none |

## 2. Scheduling

All rules need a pod in phase `Pending` with condition `PodScheduled` False.
`tools/diagnose.go` `schedulingCause` decides which rules run: when the
scheduler message has a reason this code restates (SCH-1) and no word
`Insufficient`, only SCH-1 runs. Otherwise the room check (SCH-4 to SCH-9)
runs too. The light mode of `list` runs SCH-1 to SCH-3 only.

| ID | Rule | Where | Trigger | Prints | Fires | Silent | Thresholds |
|---|---|---|---|---|---|---|---|
| SCH-1 | The scheduler's reasons | `tools/diagnose.go` `placementCause` | The message (before ` preemption:`) matches `N/M nodes are available: ...`, and at least one reason does not contain `Insufficient`. | `no node accepts the pod (0 of 14). The scheduler gives these reasons: 1 node(s) had untolerated taint {ci: true}; 3 node(s) had untolerated taint {main: true}; ...` | `TestDiagnoseUnschedulableByTaintsAndSelector` | `TestDiagnoseUnschedulableByTaintsAndSelector` (no preemption text) | reasons cut at 900 bytes. Reason not stated. |
| SCH-2 | Tolerations of the pod | `tools/diagnose.go` `placementCause`, `tolerationsText` | SCH-1, and a reason contains `taint`. The two default tolerations (`not-ready`, `unreachable`) are left out. | `The pod tolerates only: edge=true:NoSchedule.` or `The pod has no toleration for a taint.` | `TestDiagnoseUnschedulableByTaintsAndSelector` | none | 300 bytes. Reason not stated. |
| SCH-3 | Node selection of the pod | `tools/diagnose.go` `placementCause`, `nodeSelectionText` | SCH-1, and a reason contains `node affinity` or `node selector`. | `The pod selects nodes with: pool=edge.` | `TestDiagnoseUnschedulableByTaintsAndSelector` | none | 400 bytes. Reason not stated. |
| SCH-4 | Larger than any node | `tools/diagnose.go` `roomCause` | Deep mode. The pod request (sum of the app containers, or the largest init container if larger) is above the largest allocatable of the nodes that are not cordoned and that the pod can be placed on (`placement.go` `nodeAccepts`). Memory and CPU are checked apart. | `the pod requests 20480Mi memory, more than any node can offer (largest: node-a with 16384Mi allocatable). It cannot fit on the current nodes.` | `TestDiagnoseUnschedulable`, `TestListPodsProblemsFirst` | none | 5,000 pods read; 2,000 nodes read. Reasons not stated. |
| SCH-5 | Nodes full | `tools/diagnose.go` `roomCause` | Not SCH-4, and the request is above the most free room (allocatable less the requests of the app containers of the pods on the node that have not ended) of the nodes counted in SCH-4. | `the pod requests 12288Mi memory; the most free on any node is 11008Mi (node-a: 16384Mi allocatable, 5376Mi requested by other pods).` | `TestDiagnoseNodesFull`, `TestRoomCheckDoesNotNameATaintedNode` | `TestRoomCheckDoesNotNameATaintedNode` (a tainted node is not named) | as SCH-4 |
| SCH-6 | Room by requests | `tools/diagnose.go` `roomCause` | Neither SCH-4 nor SCH-5, a node has room for both CPU and memory, and the pod has no placement rule that `unevaluatedPlacement` reports (topology spread that must hold, required pod affinity or anti-affinity, `matchFields`, `Gt` or `Lt`, a toleration with another operator). | `by requests, node node-t1 has room for the pod (100m CPU, 2048Mi memory). The scheduler message names another reason, such as a taint, node affinity, or a volume.` | `TestRoomCheckCountsATaintedNodeThePodTolerates`, `TestRoomCheckHonoursNodeSelectorAndAffinity` | `TestSilentRoomSentenceWithAnUnevaluatedPlacementRule`, `TestRoomCheckDoesNotNameATaintedNode`, `TestDiagnoseUnschedulableByTaintsAndSelector` | as SCH-4 |
| SCH-7 | No node has both | `tools/diagnose.go` `roomCause` | Neither SCH-4 nor SCH-5 nor SCH-6: no single node has room for CPU and memory together. | `no single node has both 500m CPU and 2048Mi memory free.` | none | none | as SCH-4 |
| SCH-8 | Every node cordoned | `tools/diagnose.go` `roomCause` | No node is left to count, no node was left out for placement, and some node was read. | `every node is cordoned (3): no node accepts new pods.` | none | none | none |
| SCH-9 | Notes of the room check | `tools/diagnose.go` `roomCause` | Added to SCH-4, SCH-5 or SCH-7: cordoned nodes, and a pod list cut at its cap. | `2 nodes are cordoned and not counted. This is an estimate: only the first 5,000 pods were read.` | none | none | 5,000 pods. Reason not stated. |
| SCH-10 | No room text when only placement excludes nodes | `tools/diagnose.go` `roomCause` | Every node that is not cordoned was left out by `nodeAccepts`: the room check says nothing. | (nothing) | none | `TestRoomCheckDoesNotNameATaintedNode` | none |

## 3. Services

| ID | Rule | Where | Trigger | Prints | Fires | Silent | Thresholds |
|---|---|---|---|---|---|---|---|
| SVC-1 | No ready endpoints (describe) | `tools/describe.go` `describeObj`, `serviceEndpoints` | The Service has a selector and its EndpointSlices hold no ready endpoint. An endpoint with no `ready` field counts as ready. When the slice list was cut and nothing is ready, nothing is said. | `service has no ready endpoints` | `TestDiagnoseServiceSelectorMismatch` | `TestDescribeWorkloadAndService` | 50 EndpointSlices. Reason not stated. |
| SVC-2 | No selector | `tools/diagnose.go` `serviceCause` | The Service has no selector. `describe` calls `serviceCause` only with a selector (SVC-1), so this runs from `list` (SVC-12) only, for a Service with no selector whose row is unhealthy or warning by a condition (section 6). It does not read the endpoints. | `the service has no selector: its endpoints are managed by hand or by another controller, and none is ready.` | none | none | none |
| SVC-3 | Selected pods, none ready | `tools/diagnose.go` `serviceCause` | The selector matches pods and none is ready (`podReady`). | `selector app=web matches 1 pod, but none is ready: pod/probe-1: container web: running but not ready` | `TestDiagnoseServicePodsNotReady`, `TestListServicesShowsHealthAndCause` | none | 200 pods read; 3 pods named. Reasons not stated. |
| SVC-4 | Owner scaled to 0 | `tools/diagnose.go` `serviceCause`, `scaledToZeroOwners` | The selector matches no pod, and a Deployment or StatefulSet of the namespace has `spec.replicas` 0 and template labels that hold the selector. | `selector app=web matches 0 pods in namespace pay: deployment/web is scaled to 0 replicas.` | `TestServiceOfAWorkloadScaledToZero` | `TestSilentServiceNotScaledToZero` | 2,000 objects per kind. Reason not stated. |
| SVC-5 | No pod has the label | `tools/diagnose.go` `serviceCause` | The selector matches no pod, no SVC-4 owner, and no pod of the namespace has a selector key with another value. | `selector app=web matches 0 pods in namespace pay. No pod in the namespace has the label app.` | none | none | 500 pods read. Reason not stated. |
| SVC-6 | Pods with other values of the label | `tools/diagnose.go` `serviceCause`, `similar` | The selector matches no pod, no SVC-4 owner, and pods of the namespace have a selector key with another value. Similar values first (`similar`), else all; most pods first. | `selector app=checkout matches 0 pods in namespace pay. Pods with other values for the same label: app=checkout-v2 (2 pods).` | `TestDiagnoseServiceSelectorMismatch`, `TestSilentServiceNotScaledToZero`, `TestListServicesShowsHealthAndCause` | none | 500 pods read; 3 values shown; `similar` as POD-10. Reasons not stated. |
| SVC-7 | targetPort name that no container declares | `tools/related_kinds.go` `targetPortFindings`, `tools/diagnose.go` `servicePortMismatch` | A string `targetPort` that no container port of the selected pods names. Adds a sentence when the EndpointSlices hold endpoints and no ports. A certain fault. | `service port 80 has targetPort "http-api", but the selected pods declare no container port with that name (declared: http 8080). The EndpointSlices hold 1 endpoint and no ports, so no traffic reaches them although they look ready.` | `TestDescribeServiceNamesThePortMismatch` | none | 200 pods read; 50 EndpointSlices. Reasons not stated. |
| SVC-8 | Numeric targetPort that no container declares | `tools/related_kinds.go` `targetPortFindings` | A numeric `targetPort` that no container port declares, when the pods declare at least one port. A strong hint only. | `service port 80 has targetPort 9090, which no container port of the selected pods declares (declared: 8080). Strong hint of a wrong port; it is fine only if the container listens on it without declaring it.` | `TestServiceNumericTargetPortHint`, `TestNumericTargetPortThatNoDeclaredPortMatchesIsReportedWithReadyEndpoints` | `TestNumericTargetPortThatNoDeclaredPortMatchesIsReportedWithReadyEndpoints` (pods declare no port) | as SVC-7 |
| SVC-9 | EndpointSlices with endpoints and no ports | `tools/diagnose.go` `servicePortMismatch` | No SVC-7 or SVC-8 finding, and the slices hold endpoints and no ports. Sets the describe state to unhealthy. | `the EndpointSlices of the service hold 2 endpoints and no ports, so no traffic reaches them although they look ready. Compare the service targetPort with the container ports (declared: none).` | none | none | 50 EndpointSlices. Reason not stated. |
| SVC-10 | List row: no ready endpoints | `tools/list.go` `list` | A Service with a selector; the slices of the namespace were read; it has no slice, or no ready endpoint. Not when the slice list was cut and none is ready in view. A warning when SVC-4 finds an owner, else unhealthy. | `no ready endpoints (0 of 0): deployment/web is scaled to 0 replicas` | `TestListServicesShowsHealthAndCause`, `TestServiceOfAWorkloadScaledToZero` | `TestSilentCutEndpointSliceListSaysNothingAboutAService` | 2,000 EndpointSlices. Reason not stated. |
| SVC-11 | List row: endpoints have no ports | `tools/list.go` `list`, `endpointCounts` | Ready endpoints, the slices of the Service hold endpoints and no ports, and the Service has `spec.ports`. | `endpoints have no ports: a targetPort matches no container port` | `TestDescribeServiceNamesThePortMismatch`, `TestSilentHeadlessServiceWithoutPortsInTheList` (second case) | `TestSilentHeadlessServiceWithoutPortsInTheList` | as SVC-10 |
| SVC-12 | List row cause of a Service | `tools/list.go` `listCauses` | The first three unhealthy or warning rows: SVC-2 to SVC-6, else SVC-7 to SVC-9. | `selector app=checkout matches 0 pods in namespace pay; pods with other values for the same label: app=checkout-v2 (2 pods)` | `TestListServicesShowsHealthAndCause` | none | 3 rows; 240 bytes. Reasons not stated. |

## 4. Workload and revision differences

| ID | Rule | Where | Trigger | Prints | Fires | Silent | Thresholds |
|---|---|---|---|---|---|---|---|
| REV-1 | What changed in the revision | `tools/difference.go` `revisionDiff`, `diffTemplates`, `diffContainer` | A Deployment with an older kept ReplicaSet (by the revision annotation), and a signal: the Deployment is unhealthy or warning, or a pod of the current ReplicaSet fails (`podFailing`), or the current ReplicaSet is younger than 1 hour, or the rollout is not complete (`rolloutComplete`). | `changed in revision 2 (10m ago), against revision 1:` then `- container app image: registry.example/web:1 -> registry.example/web:2` | `TestRevisionDiffNewPodsFail`, `TestRevisionDiffWhenYoungOrIncomplete` | `TestRevisionDiffHealthyOldRolloutPrintsNothing` | 1 hour; 8 lines; 80 bytes per value; 200 ReplicaSets read. Reasons not stated. |
| REV-2 | Restart only | `tools/difference.go` `revisionDiff` | REV-1 prints, and the only change is the `restartedAt` annotation. An old, complete rollout that changed nothing in the template prints nothing. | `- restarted at 2026-11-13T11:50:00Z, no other change` | `TestRevisionDiffRestartOnly` | none | as REV-1 |
| REV-3 | New pods not ready, old pods ready | `tools/difference.go` `revisionDiff` | REV-1 prints, the old revision has a ready pod, and a pod of the current revision is not ready, with a failure signal (`startingQuietly` false). | `the pods of revision 2 are not ready; revision 1 has 3 ready pods` | `TestRevisionDiffNewPodsWithAFailureSignalKeepTheirWording` | `TestSilentRollingDeploymentIsNotUnhealthyAndHasNoCause` | none |
| REV-4 | New pods not ready yet | `tools/difference.go` `revisionDiff`, `startingQuietly` | As REV-3, and every pod that is not ready shows no failure: no restart, no last termination, no error exit, no waiting reason other than `ContainerCreating` or `PodInitializing`, scheduled, not failed. | `the pods of revision 2 are not ready yet (started 40s ago); revision 1 has 3 ready pods` | `TestSilentRollingDeploymentIsNotUnhealthyAndHasNoCause` | `TestRevisionDiffNewPodsWithAFailureSignalKeepTheirWording` | none |

## 5. Sibling differences

`tools/difference_pods.go` `siblingDiff` compares the failing pods (row state
unhealthy or warning) with the healthy pods of one workload. It needs at least
one of each.

| ID | Rule | Where | Trigger | Prints | Fires | Silent | Thresholds |
|---|---|---|---|---|---|---|---|
| SIB-1 | A fact that only the failing pods have | `tools/difference_pods.go` `siblingDiff`, `podFacts` | A fact (node, revision hash, image, image digest, claim, restart policy, QoS class) that every pod has, that has one value in all failing pods, and that no healthy pod has. | `- node: node-b in all 3 failing pods, in none of the 3 healthy pods` | `TestSiblingDiffAllOnOneNode`, `TestTwoFailingPodsOnOneNode` | `TestSiblingDiffNeedsBothGroups` | 8 lines. Reason not stated. |
| SIB-2 | One failing pod | `tools/difference_pods.go` `siblingDiff` | As SIB-1 with one failing pod: also every healthy pod must share one single value. | `- node: node-b in the failing pod, in none of the 2 healthy pods` | `TestSingleFailingPodWithHealthyPodsOnOneOtherNode` | `TestSilentOneFailingPodOnItsOwnNodePrintsNoNodeLine` | 2 failing pods |
| SIB-3 | A claim of each pod is no fact | `tools/difference_pods.go` `siblingDiff` | The claim fact is skipped when every pod has a different claim. | (nothing) | none | `TestSilentStatefulSetOrdinalRemainderIsNotClaimed` | none |
| SIB-4 | Age | `tools/difference_pods.go` `siblingDiff` | At least 3 pods, and every failing pod is newer (or older) than every healthy pod by more than 1 minute. | `- age: every failing pod is newer than every healthy pod (failing: at most 5m old; healthy: at least 3d old)` | none | none | 3 pods (reason stated in the code: with fewer, any split would look like a pattern); 1 minute (reason not stated) |
| SIB-5 | StatefulSet ordinal | `tools/difference_pods.go` `ordinalPattern` | A StatefulSet, every pod name ends with a number, and every failing ordinal is above (or below) every healthy one. A remainder is never claimed. | `- ordinal: all failing pods are above every healthy one (failing 17, 18, 19; the highest healthy is 16)` | `TestStatefulSetOrdinalAboveEveryHealthyPod` | `TestSilentStatefulSetOrdinalRemainderIsNotClaimed` | 6 ordinals shown. Reason not stated. |
| SIB-6 | Revision folds image, digest and age | `tools/difference_pods.go` `siblingDiff` | A revision line exists: the image, image digest and age lines are dropped, and the image is added to the revision line. | `- revision: bbbb2 in all 2 failing pods, in none of the 2 healthy pods (image registry.example/web:2)` | none | none | none |
| SIB-7 | Nothing separates them | `tools/difference_pods.go` `siblingDiff` | No line from SIB-1 to SIB-5, and some fact was compared. | `no difference in node, revision, image, image ID, age, restart policy or QoS class between the 2 failing and the 2 healthy pods` | `TestSiblingDiffNothingSeparatesThem` | `TestSiblingDiffNeedsBothGroups` | none |

## 6. Generic conditions and the polarity tables

`shape/conditions.go` `Signals` runs for every kind except Pod, Event and
ConfigMap, and adds its lines to the row of section 9.

| ID | Rule | Where | Trigger | Prints | Fires | Silent | Thresholds |
|---|---|---|---|---|---|---|---|
| CND-1 | A condition that is not healthy | `shape/conditions.go` `Signals`, `ConditionHealthy` | A `status.conditions` entry that is not healthy by the polarity tables, not said by the row of the kind (`coveredByRow`), and not informational. Unhealthy. Newest first. | `Stalled is True: RetriesExhausted: gave up after 5 tries` | `TestSignalsForACustomResource`, `TestCustomResourceConditions`, `TestKnownPolarityFalseStaysUnhealthy`, `TestNodeDetectorConditionTrueIsAProblem` | `TestCustomResourceWithHealthyConditionsIsHealthy`, `TestSilentNodeDetectorConditionsAtTheirHealthyStatus` | message cut at 200 bytes. Reason not stated. |
| CND-2 | Unknown polarity, False | `shape/conditions.go` `Signals`, `polarityKnown` | Status `False`, and the type is in no table and contains no word of the tables. A warning that claims no fault. | `condition Frobnicated is False: NotYet` | `TestUnknownPolarityFalseIsAWarning` | `TestUnknownPolarityFalseIsAWarning` (True is healthy) | none |
| CND-3 | Soft types | `shape/conditions.go` `Signals` | `ScalingLimited` or `Suspended` is `True`: a warning, not unhealthy. | `ScalingLimited is True: TooManyReplicas: ...` | none | none | none |
| CND-4 | Informational types | `shape/conditions.go` `Signals` | Type `Unused`: never a line, whatever the status. | (nothing) | none | `TestInformationalConditionNeverSignals`, `TestSilentClaimWithUnusedConditionFalse` | none |
| CND-5 | Said once | `shape/conditions.go` `coveredByRow` | The row of the kind already says the condition: every Pod condition; Node Ready and the four pressure types; Deployment Progressing, ReplicaFailure, Available; ReplicaSet ReplicaFailure; Job Failed, Suspended, Complete; PodDisruptionBudget DisruptionAllowed. | (nothing) | none | `TestSignalsCutLongMessagesAndSayEachFactOnce`, `TestDescribeJobShowsItsFailedPod` | none |
| CND-6 | Autoscaler message kept longer | `shape/conditions.go` `Signals` | The kind is `HorizontalPodAutoscaler`. | `ScalingActive is False: FailedGetResourceMetric: the HPA was unable to compute the replica count: ...` | `TestAutoscalerDescribeShowsTheMetricReasonInFull`, `TestAutoscalerSignals` | none | 500 bytes (reason stated in the code: the message names the metric that cannot be computed) |
| CND-7 | Replicas of any other kind | `shape/conditions.go` `statusFacts` | A kind with no rule of its own (not Deployment, StatefulSet, DaemonSet, ReplicaSet, Job, Pod, HPA, CronJob) with `spec.replicas` above 0 and `status.readyReplicas` (else `availableReplicas`) below it. Unhealthy. | `1 of 3 replicas ready` | `TestSignalsForReplicasOfACustomResource` | none | none |

The polarity tables are in `shape/conditions.go`. A type is matched by its
lowercase name. `Unknown` is never healthy.

| Table | Members | Effect | Test |
|---|---|---|---|
| `healthyWhenFalse` | replicafailure, failed, failuretarget, memorypressure, diskpressure, pidpressure, networkunavailable, stalled, degraded, conflict, unschedulable, nonstructuralschema, terminating, scalinglimited, suspended, disruptiontarget, podresizepending, podresizeinprogress, resizing, filesystemresizepending, and the node problem detector types (kerneldeadlock, readonlyfilesystem, frequentkubeletrestart, frequentdockerrestart, frequentcontainerdrestart, frequentunregisternetdevice, corruptdockeroverlay2, ntpproblem, kubeletproblem, containerruntimeproblem, filesystemcorruptionproblem, vmeventscheduled) | True is a problem; False is healthy | `TestConditionPolarity`, `TestSilentNodeDetectorConditionsAtTheirHealthyStatus` |
| `problemWords` | failure, failed, pressure, unavailable, degraded, stalled, conflict, error, remaining, problem, deadlock, corrupt, readonly, frequent (substring) | as `healthyWhenFalse` | `TestConditionPolarity` |
| `healthyWhenTrue` | ready, available, bound, accepted, initialized, progressing, containersready, podscheduled, resolvedrefs, programmed, established, namesaccepted, abletoscale, scalingactive, healthy, synced, reconciled, approved, complete, successcriteriamet, readyforquery, certificatesigned | polarity known: False is unhealthy (CND-1) | `TestKnownPolarityFalseStaysUnhealthy` |
| `healthyWords` | ready, available, healthy, synced, accepted, bound, initialized, established, programmed, reconciled (substring) | polarity known, as `healthyWhenTrue` | `TestKnownPolarityFalseStaysUnhealthy` |
| `notAProblemWhenFalse` | complete, successcriteriamet | False is healthy | `TestConditionPolarity` |
| `softTypes` | scalinglimited, suspended | True is a warning (CND-3) | none |
| `informational` | unused | never a line (CND-4) | `TestInformationalConditionNeverSignals` |
| special case in `ConditionHealthy` | progressing with reason ProgressDeadlineExceeded | never healthy | `TestConditionPolarity` |

## 7. HPA, CronJob, PDB, quota and other related lines

The related lines come from `tools/related.go` `related` and the `rel...`
functions. They print under `related, with a problem:` in `describe`.

| ID | Rule | Where | Trigger | Prints | Fires | Silent | Thresholds |
|---|---|---|---|---|---|---|---|
| REL-1 | HPA at max replicas | `shape/health.go` `Inspect` | `status.currentReplicas` is at least `spec.maxReplicas`, max above 0, and `minReplicas` (default 1) is not equal to max. Warning. | `at max replicas (5)` | `TestInspectOtherKinds`, `TestSilentAutoscalerWithOneReplicaRange` | `TestSilentAutoscalerWithOneReplicaRange` | none |
| REL-2 | HPA with no current metric | `shape/conditions.go` `statusFacts` | `status.currentMetrics` is empty, `spec.metrics` is not, no `ScalingActive` condition that is not True, and the HPA is older than 3 minutes. Warning. | `no current metric value: status.currentMetrics is empty, so the autoscaler cannot decide` | `TestAutoscalerSignals` | `TestAutoscalerSignals` (ScalingActive False case only) | 3 minutes. Reason not stated. |
| REL-3 | HPA target missing | `tools/related_kinds.go` `relAutoscalerTarget` | The target kind is in the allowlist and the read of the target answers not found. | `- deployment/api: the target does not exist in namespace prod` | `TestAutoscalerDescribeShowsTheMetricReasonInFull` | none | none |
| REL-4 | Autoscaler of a workload with a problem | `tools/related.go` `relAutoscalers` | An HPA of the namespace targets the workload and its row (section 9 and 6) has a problem. | `- horizontalpodautoscaler/api: ScalingActive is False: FailedGetResourceMetric: ...` | `TestDescribeWorkloadShowsTheRelatedProblems` | `TestDescribeHealthyWorkloadStaysSmall` | 200 HPAs read. Reason not stated. |
| REL-5 | CronJob schedule not valid | `shape/conditions.go` `cronFacts`, `shape/cron.go` `ParseCron` | The schedule does not parse: not 5 fields, a value out of range, a range that runs backwards, a step below 1, `@every` under 1 second. Unhealthy. | `schedule "61 * * * *" is not valid: minute field "61": 61 is outside 0-59` | `TestCronJobSignals` | `TestEveryCronDescriptorIsValid` | none |
| REL-6 | CronJob that can never fire | `shape/conditions.go` `cronFacts`, `shape/cron.go` `CanFire` | Only the day of the month limits the date (day of week is `*`), and no named day exists in any named month (February has 29). Unhealthy. | `schedule "0 3 31 2 *" can never fire: no day it names exists in the months it names` | `TestCronJobSignals`, `TestCronJobSignalsInDescribeAndList` | `TestCronJobSignals` (29 February) | none |
| REL-7 | CronJob never scheduled | `shape/conditions.go` `cronFacts`, `shape/cron.go` `Next` | Not suspended, no `lastScheduleTime`, and the first fire time after the creation time is more than the grace ago. The schedule is read in the zone of the creation time as parsed. Warning. | `never scheduled: it is 3h old and the schedule "*/10 * * * *" should have fired 2h ago` | `TestCronJobSignals`, `TestEveryScheduleFiresAtItsInterval`, `TestCronJobSignalsInDescribeAndList` | `TestCronJobSignals` (new, suspended, yearly) | grace 2 minutes, plus 26 hours when `spec.timeZone` is set. Reason for the extra grace is stated in the code (the schedule is read in that zone, this reader is not); the values are not. |
| REL-8 | CronJob last run did not succeed | `shape/conditions.go` `cronFacts` | `lastScheduleTime` is set, no active Job, the last schedule is more than 2 minutes ago, and `lastSuccessfulTime` is set and older than the last schedule. Warning. | `last run did not succeed: scheduled 20m ago, last success 3h ago` | `TestCronJobLastRunFailed` | `TestSilentCronJobWithoutARecordedSuccessIsNotAFailure` | 2 minutes. Reason not stated. |
| REL-9 | CronJob result not known | `shape/conditions.go` `cronFacts` | As REL-8, and no `lastSuccessfulTime` is recorded. Warning. | `result of the last run is not known: scheduled 20m ago, the status records no success` | `TestSilentCronJobWithoutARecordedSuccessIsNotAFailure`, `TestSilentCronJobWhoseLastJobCannotBeFound` | none | 2 minutes. Reason not stated. |
| REL-10 | CronJob newest Job failed | `tools/describe.go` `cronResultKnown` | REL-9 printed, and the newest Job that the CronJob owns has an unhealthy row. Replaces the REL-9 line. | `last run did not succeed: scheduled 1h ago, last success never` | `TestCronJobSignalsInDescribeAndList` | `TestSilentCronJobWhoseLastJobCannotBeFound` | 200 Jobs read. Reason not stated. |
| REL-11 | CronJob suspended | `shape/health.go` `Inspect` | `spec.suspend` is true. Warning. | `suspended` | none | none | none |
| REL-12 | Recent Jobs of a CronJob | `tools/related_kinds.go` `relCronJobs` | One of the 3 newest Jobs that the CronJob owns has an unhealthy or warning row. | `- job/failing-29: failed: BackoffLimitExceeded: Job has reached the specified backoff limit` | `TestCronJobSignalsInDescribeAndList` | none | 3 Jobs; 200 Jobs read. Reasons not stated. |
| REL-13 | PDB allows no disruption | `shape/health.go` `Inspect`, `budgetForbidsByItself` | `disruptionsAllowed` is 0, `expectedPods` above 0, and not by its own setting (`maxUnavailable` resolves to 0, or `minAvailable` to the expected pods or more). Warning. | `no disruptions allowed (2 of 3 pods healthy)` | `TestSilentBudgetThatForbidsByItsOwnSetting`, `TestSilentPodDisruptionBudgetThatForbidsByItsOwnSetting` | `TestSilentBudgetThatForbidsByItsOwnSetting`, `TestSilentPodDisruptionBudgetThatForbidsByItsOwnSetting` | none |
| REL-14 | PDB forbids by its own setting | `tools/describe.go` `describeObj`, `tools/related.go` `relBudgets` | As REL-13, but by its own setting: a plain fact, not a problem. | `allows no disruptions by its own setting` | `TestSilentPodDisruptionBudgetThatForbidsByItsOwnSetting` | `TestSilentPodDisruptionBudgetThatForbidsByItsOwnSetting` | none |
| REL-15 | Quota at its limit | `tools/related.go` `relQuotas`, `quotaResourceUsed` | Deployment, StatefulSet or ReplicaSet with `status.replicas` below `spec.replicas`. A quota with no scope whose used value is at or above hard for `pods`, `count/pods`, or a CPU or memory entry that a container of the template requests or limits above 0. | `- resourcequota/compute at its limit (the workload has 1 of 3 pods): pods used 10 of 10` | `TestQuotaAtItsPodLimitWhenTheWorkloadIsShort`, `TestDescribeWorkloadShowsTheRelatedProblems` | `TestSilentQuotaOfAnUnrelatedResource`, `TestSilentQuotaWithAScope` | 50 quotas read. Reason not stated. |
| REL-16 | Quota named by an event | `tools/related.go` `relQuotas`, `quotaNamedByEvents` | As REL-15, and a `FailedCreate` event of the workload (or of at most 2 active ReplicaSets of a Deployment) holds `exceeded quota: NAME`. Scoped quotas count too. | `- resourcequota/scoped: named by a FailedCreate event (the workload has 1 of 3 pods)` | `TestQuotaNamedByAFailedCreateEvent` | `TestQuotaNamedByAFailedCreateEvent` (another quota is not named) | 2 ReplicaSets. Reason not stated. |
| REL-17 | ReplicaSet that says more | `tools/related.go` `relReplicaSets` | An active ReplicaSet (replicas above 0) of the Deployment whose row has a line other than `N of M replicas ready`. Adds its first Warning event (for the first 2 such sets) when the line does not hold it. | `- replicaset/api-6f: replica failure: FailedCreate: pods "api-6f-x" is forbidden: exceeded quota: compute, ...` | `TestDescribeWorkloadShowsTheRelatedProblems` | `TestDescribeHealthyWorkloadStaysSmall` | 2 sets with events; 220 bytes of event; 200 sets read. Reasons not stated. |
| REL-18 | Claim missing or not bound | `tools/related.go` `relClaims`, `storageClassNote`, `classNote` | A claim that the template mounts does not exist, or its row has a problem. Adds whether its storage class exists. | `- persistentvolumeclaim/data: claim is Pending; storage class "fast" does not exist (storage classes that exist: gold, standard)` | `TestDescribeWorkloadShowsTheRelatedProblems` | `TestSilentClaimWithUnusedConditionFalse`, `TestDescribeHealthyWorkloadStaysSmall` | 200 claims; 100 storage classes. Reasons not stated. |
| REL-19 | ConfigMap missing | `tools/related.go` `relConfigMaps` | One of the first 3 ConfigMaps the template refers to answers not found, and no reference to it is optional. | `- configmap/app-config: does not exist in namespace prod, and the template needs it` | none | none | 3 ConfigMaps (stated in `docs/architecture.md` 7.7 under what this gives up) |
| REL-20 | ConfigMap key missing | `tools/related.go` `relConfigMaps` | As REL-19, the ConfigMap exists and lacks a key that an env reference or a volume item names. | `- configmap/api-config: exists, but has no key LOG` | `TestDescribeWorkloadShowsTheRelatedProblems` | `TestDescribeHealthyWorkloadStaysSmall` | as REL-19 |
| REL-21 | Service of a workload with a target port problem | `tools/related.go` `relServicesOf` | A Service whose selector matches the template labels has an SVC-7 or SVC-8 finding against the template's declared ports. | `- service/api: service port 80 has targetPort 9090, which no container port of the selected pods declares (declared: 8080). ...` | `TestServiceNumericTargetPortHint` | `TestNumericTargetPortThatNoDeclaredPortMatchesIsReportedWithReadyEndpoints` | 200 Services read. Reason not stated. |
| REL-22 | Init container that has not completed | `tools/related_kinds.go` `relInitContainers` | describe of a Pod (not a short block). Each of the first 2 init containers that are not sidecars and did not end with code 0. The log is read only when the container has started. | `init container wait-db has not completed: running for 3h; its last log lines:` | `TestDescribePodShowsTheInitContainerThatWaits` | `TestDescribePodShowsTheInitContainerThatWaits` (completed init), `TestSilentSidecarRunningInHealthyPod` | 2 containers; 6 log lines. Reasons not stated. |
| REL-23 | Selected pod with a problem | `tools/related_kinds.go` `relServicePods` | describe of a Service with a selector, at least one ready pod, and a selected pod that is not ready with an unhealthy or warning row. Silent when no pod is ready (SVC-3 says it). | `- pod/web-2: container web: CrashLoopBackOff, 3 restarts` | none | none | 3 pods; 200 pods read. Reasons not stated. |
| REL-24 | Claim storage class and volume | `tools/related_kinds.go` `relClaimStorage` | describe of a claim. The storage class line prints when the claim is not Bound or the class is missing. The volume line is a problem when the PersistentVolume row has one. | `storage class: storage class "fast" does not exist (storage classes that exist: standard)` | `TestDescribeClaimIngressAndNode` | `TestDescribeClaimIngressAndNode` (bound claim) | 100 storage classes. Reason not stated. |
| REL-25 | Ingress backend missing | `tools/related_kinds.go` `relIngressBackends` | A backend Service name that no Service of the namespace has. | `- service/gone: the backend service does not exist in namespace prod` | `TestDescribeClaimIngressAndNode` | none | 10 backends; 200 Services. Reasons not stated. |
| REL-26 | Ingress backend port missing | `tools/related_kinds.go` `relIngressBackends`, `servicePortHas` | The backend port (number or name) matches no port of the Service. | `- service/web: the service has no port 8080 (it has 80)` | `TestDescribeClaimIngressAndNode` | none | as REL-25 |
| REL-27 | Ingress backend with no ready endpoints | `tools/related_kinds.go` `relIngressBackends` | The Service has a selector, is not ExternalName, has no ready endpoint, and the slice list was read and not cut. | `- service/web: the backend service has no ready endpoints` | `TestSilentCutEndpointSliceListSaysNothingAboutAnIngressBackend` | `TestSilentCutEndpointSliceListSaysNothingAboutAnIngressBackend` | 2,000 EndpointSlices. Reason not stated. |
| REL-28 | Node with pending pods | `tools/related_kinds.go` `relNodePods` | describe of a Node. A pod assigned to it is in phase `Pending` and is at least 2 minutes old. | `- node/node-1: 1 pod is assigned to it and still pending: prod/late-0` | `TestDescribeClaimIngressAndNode`, `TestSilentNodeWithYoungPendingPods` | `TestSilentNodeWithYoungPendingPods` | 2 minutes (reason stated in the code: a pod created a moment ago has not had time to start; the value is not); 2,000 pods read |

## 8. Resource verdicts

`tools/diagnose.go` `resourceFindings`, per container, from the metrics API
joined with the requests and limits of the spec. No verdict when the sample is
0 for both CPU and memory. Warnings are added to the pod row in `list`
(`tools/list.go` `list`, app containers only) and printed in `top` and
`describe`. Information is printed in `top` and `describe` only.

| ID | Rule | Where | Trigger | Prints | Fires | Silent | Thresholds |
|---|---|---|---|---|---|---|---|
| RES-1 | Near OOM | `tools/diagnose.go` `resourceFindings` | A memory limit, and memory use at 85% of it or more. Warning. | `near OOM: memory at 88% of the 1024Mi limit` | `TestDiagnoseOOMKill`, `TestTopVerdicts` | none | 85%. Reason not stated. |
| RES-2 | CPU at limit | `tools/diagnose.go` `resourceFindings` | A CPU limit, and CPU use at 90% of it or more. Warning. | `CPU at 98% of the 1000m limit, throttling likely` | `TestTopVerdicts`, `TestListWarnsBeforeTheCrash` | none | 90%. Reason not stated. |
| RES-3 | Above memory request | `tools/diagnose.go` `resourceFindings` | A memory request, and memory use above 1.1 times it. Information. | `memory above its request (600Mi used, 512Mi requested): first to be evicted when the node runs low` | none | none | 1.1. Reason not stated. |
| RES-4 | No requests or limits | `tools/diagnose.go` `resourceFindings` | No CPU or memory request or limit. Information. | `no requests or limits: the scheduler cannot plan for it and nothing caps it` | `TestDiagnoseOOMKill`, `TestTopVerdicts` | `TestTopSidecarHasItsRequestsAndLimits` | none |
| RES-5 | No memory limit | `tools/diagnose.go` `resourceFindings` | No memory limit, and some other request or limit. Information. | `no memory limit: a leak can use all the node's memory` | `TestDescribeHealthyPodHasNoCause` | none | none |
| RES-6 | CPU over-provisioned | `tools/diagnose.go` `resourceFindings` | A CPU request of 100m or more, and at least 4 times the use. Information. | `CPU over-provisioned (requests 1000m, uses 20m)` | `TestTopVerdicts` | none | 4 times; 100m. Reasons not stated. |
| RES-7 | Memory over-provisioned | `tools/diagnose.go` `resourceFindings` | A memory request of 128Mi or more, and at least 4 times the use. Information. | `memory over-provisioned (requests 4096Mi, uses 200Mi)` | `TestTopVerdicts` | none | 4 times; 128Mi. Reasons not stated. |
| RES-8 | Information is not a list warning | `tools/list.go` `list` | Only findings with `warn` (RES-1, RES-2) change the pod row. | (nothing) | none | `TestListWarnsBeforeTheCrash` | none |

## 9. List health per kind

`shape/health.go` `Inspect` gives the state and problem lines of a row. The
same state decides whether `describe` gives a likely cause (section 1).

| ID | Rule | Where | Trigger | Prints | Fires | Silent | Thresholds |
|---|---|---|---|---|---|---|---|
| HLT-1 | Pending pod is a warning | `shape/health.go` `podRow` | Phase `Pending`: the state is warning, also with no problem line (a pod created a second ago too). | (state only) | none | none | none |
| HLT-2 | Pod not scheduled | `shape/health.go` `podRow` | Phase `Pending` and condition `PodScheduled` False. Unhealthy. The preemption part is cut off. | `not scheduled: 0/3 nodes are available: 3 Insufficient memory.` | `TestInspectOtherKinds` | none | none |
| HLT-3 | Container waits with a reason | `shape/health.go` `podRow` | A container (init too) waits with a reason other than `ContainerCreating` and `PodInitializing`. Unhealthy. | `container api: CrashLoopBackOff, last exit 137 (OOMKilled), 14 restarts` | `TestInspectPodProblems` | none | none |
| HLT-4 | App container terminated with an error | `shape/health.go` `podRow` | An app container is terminated now with a code other than 0. Unhealthy. | `container report: terminated, exit 137 (OOMKilled)` | `TestFailedPodWithoutReason` | none | none |
| HLT-5 | Old restart is history | `shape/health.go` `podRow` | The pod is `Running`, every app container is ready, and the last termination of the container finished more than 24 hours ago: no restart line. | (nothing) | none | `TestSilentOldRestartOfReadyPodIsHealthy`, `TestSilentOldOOMOfReadyPodIsNotAWarning`, `TestSilentManyOldRestartsOfReadyPodIsNotAWarning` | 24 hours (reason stated in the code: one day covers a daily load cycle) |
| HLT-6 | Last restart was an OOM kill | `shape/health.go` `podRow` | Not HLT-5, and the last termination has reason `OOMKilled`. Warning. | `container app: last restart was OOMKilled (1 restarts)` | `TestRecentRestartOfReadyPodStaysAWarning` | `TestSilentOldRestartOfReadyPodIsHealthy` | 24 hours (HLT-5) |
| HLT-7 | Many restarts | `shape/health.go` `podRow` | Not HLT-5 or HLT-6, and 3 restarts or more. Warning. | `container proxy: 5 restarts` | `TestRecentRestartOfReadyPodStaysAWarning`, `TestRestartedPodsHaveTheirOwnRowWithTheCause` | `TestSilentOldRestartOfReadyPodIsHealthy` | 3 restarts. Reason not stated. |
| HLT-8 | Running but not ready | `shape/health.go` `podRow` | An app container runs, is not ready, and the pod phase is `Running`. Warning. | `container web: running but not ready` | `TestDiagnoseServicePodsNotReady`, `TestOldRestartOfPodThatIsNotReadyStaysAProblem` | none | none |
| HLT-9 | Not started | `shape/health.go` `podRow` | Phase `Pending`, no other problem line, and the pod is older than 2 minutes. Warning. | `not started after 5m (ContainerCreating)` | none | none | 2 minutes. Reason not stated. |
| HLT-10 | Pod failed | `shape/health.go` `podRow` | Phase `Failed`. Unhealthy. The line is added when the pod has a reason or message, or when no other line explains it. | `pod failed: Evicted The node was low on resource: memory.` | none | `TestFailedPodWithoutReason` | none |
| HLT-11 | Terminating too long | `shape/health.go` `podRow` | A deletion timestamp more than 5 minutes ago. Warning. | `terminating for 12m` | none | none | 5 minutes. Reason not stated. |
| HLT-12 | Restart-only row | `tools/list.go` `restartOnly`, `tools/causes.go` `rowTier` | The pod row is a warning, `Running`, all containers ready, and every line is `container NAME: N restarts`. The row comes after the other warnings and gets no deep diagnosis. | (row order only) | `TestRestartedPodsHaveTheirOwnRowWithTheCause`, `TestListReadsEventsOnceAndNoLogsForRestartedPods` | none | none |
| HLT-13 | Deployment short of available replicas | `shape/health.go` `Inspect` | `availableReplicas` below `spec.replicas` (default 1), and not HLT-14. Unhealthy. | `1 of 3 replicas available` | `TestInspectOtherKinds`, `TestRollingDeploymentOutsideItsBudgetStaysUnhealthy` | `TestInspectOtherKinds` (all available) | none |
| HLT-14 | Deployment rolling inside its budget | `shape/health.go` `deploymentRollingInBudget` | Not `Recreate`; `Progressing` is True with a reason other than `NewReplicaSetAvailable` and `ProgressDeadlineExceeded`; available at least replicas less `maxUnavailable` (25% by default, rounded down). Warning. | `rolling out: 3 of 4 available` | `TestSilentRollingDeploymentInsideItsBudgetIsNotUnhealthy`, `TestSilentRollingDeploymentIsNotUnhealthyAndHasNoCause` | `TestRollingDeploymentOutsideItsBudgetStaysUnhealthy` | 25%, rounded down (the Kubernetes default, stated in the code) |
| HLT-15 | Deployment not progressing | `shape/health.go` `Inspect` | Condition `Progressing` is False. Unhealthy. | `not progressing: ProgressDeadlineExceeded: ReplicaSet "web-5d" has timed out progressing.` | none | none | none |
| HLT-16 | Deployment or ReplicaSet replica failure | `shape/health.go` `Inspect` | Condition `ReplicaFailure` is True. Unhealthy. | `replica failure: FailedCreate: pods "api-6f-x" is forbidden: exceeded quota: compute, ...` | `TestDescribeWorkloadShowsTheRelatedProblems` | none | none |
| HLT-17 | Deployment paused | `shape/health.go` `Inspect` | `spec.paused` is true. Warning. | `rollout paused` | none | none | none |
| HLT-18 | StatefulSet short of ready replicas | `shape/health.go` `Inspect` | `readyReplicas` below `spec.replicas`, and not HLT-19. Unhealthy. | `2 of 3 replicas ready` | `TestInspectOtherKinds`, `TestRollingStatefulSet`, `TestStatefulSetWithOneRevisionIsNotRolling` | none | none |
| HLT-19 | StatefulSet rolling inside its budget | `shape/health.go` `statefulSetRollingInBudget` | Not `OnDelete`; an update is in progress (revisions differ, or with no revisions some pods updated); fewer updated pods than the partition leaves; ready at least replicas less `maxUnavailable` (1 by default, a percentage rounded up, at least 1). Warning. | `rolling out: 2 of 3 ready` | `TestRollingStatefulSet` | `TestRollingStatefulSet`, `TestStatefulSetWithOneRevisionIsNotRolling` | 1 (the Kubernetes default, stated in the code) |
| HLT-20 | DaemonSet short of ready pods | `shape/health.go` `Inspect` | `numberUnavailable` above 0 or `numberReady` below `desiredNumberScheduled`, and not HLT-21. Unhealthy. | `8 of 10 pods ready` | `TestRollingDaemonSet` | none | none |
| HLT-21 | DaemonSet rolling inside its budget | `shape/health.go` `daemonSetRollingInBudget` | Not `OnDelete`; fewer updated than desired; ready at least desired less `maxUnavailable` (1 by default, a percentage rounded up). Warning. | `rolling out: 9 of 10 ready` | `TestRollingDaemonSet` | `TestRollingDaemonSet` | 1 (the Kubernetes default, stated in the code) |
| HLT-22 | DaemonSet misscheduled | `shape/health.go` `Inspect` | `numberMisscheduled` above 0. Warning. | `2 pods misscheduled` | none | none | none |
| HLT-23 | ReplicaSet short of ready replicas | `shape/health.go` `Inspect` | `spec.replicas` above 0 and `readyReplicas` below it. Unhealthy. | `1 of 2 replicas ready` | none | `TestDescribeHealthyWorkloadStaysSmall` | none |
| HLT-24 | Job failed | `shape/health.go` `Inspect` | Condition `Failed` is True. Unhealthy. | `failed: BackoffLimitExceeded: Job has reached the specified backoff limit` | `TestInspectOtherKinds`, `TestDescribeJobShowsItsFailedPod` | `TestSilentCompletedJobGetsNoCause` | none |
| HLT-25 | Job with failed pods so far | `shape/health.go` `Inspect` | No True condition of Complete, Failed or Suspended, and `status.failed` above 0. Warning. | `2 failed pods so far` | none | `TestSilentCompletedJobGetsNoCause` | none |
| HLT-26 | Claim not bound | `shape/health.go` `Inspect` | Phase not `Bound`. Unhealthy. | `claim is Pending` | `TestInspectOtherKinds` | `TestSilentClaimWithUnusedConditionFalse` | none |
| HLT-27 | Volume failed | `shape/health.go` `Inspect` | PersistentVolume phase `Failed`. Unhealthy. | `volume failed: <status message>` | none | none | none |
| HLT-28 | Node not ready | `shape/health.go` `nodeRow` | Condition `Ready` is not True. Unhealthy. | `not ready: KubeletNotReady PLEG is not healthy` | `TestInspectOtherKinds` | none | none |
| HLT-29 | Node pressure | `shape/health.go` `nodeRow` | `MemoryPressure`, `DiskPressure`, `PIDPressure` or `NetworkUnavailable` is True. Unhealthy. | `MemoryPressure: low memory` | `TestDescribeClaimIngressAndNode` | none | none |
| HLT-30 | Node cordoned | `shape/health.go` `nodeRow` | `spec.unschedulable` is true. Warning. | `cordoned (unschedulable)` | none | none | none |
| HLT-31 | Namespace terminating | `shape/health.go` `Inspect` | Phase `Terminating`. Warning. | `terminating` | none | none | none |
| HLT-32 | Warning event | `shape/health.go` `Inspect` | An Event of type `Warning`: the row is a warning with no line. | (state only) | none | none | none |
| HLT-33 | Kinds with conditions only | `shape/health.go` `Inspect` | A kind with no rule of its own that has `status.conditions` is healthy unless section 6 finds a line. | (state only) | `TestCustomResourceWithHealthyConditionsIsHealthy`, `TestCustomResourceConditions` | `TestCustomResourceWithHealthyConditionsIsHealthy` | none |

## 10. Log error detection

These rules decide which log line is an error. They feed the log summaries
and the quotes of POD-12 and POD-16.

| ID | Rule | Where | Trigger | Prints | Fires | Silent | Thresholds |
|---|---|---|---|---|---|---|---|
| LOG-1 | A level field decides alone | `shape/logs.go` `IsErrorLine` | The line has `level`, `severity`, `lvl` or `loglevel` with `:` or `=`. It is an error only for error, err, fatal, panic, critical, crit, emerg, emergency, alert, severe, dpanic. Other words of the line do not count. | (the line, in the error groups) | none | none | none |
| LOG-2 | klog error or fatal | `shape/logs.go` `IsErrorLine` | No level field, and the line starts with `E` or `F` and four digits and a space. | `E1113 12:00:00.000000 1 reflector.go:1] failed to list` | `TestIsErrorLineStillFindsErrors` | none | none |
| LOG-3 | Error words | `shape/logs.go` `IsErrorLine` | No level field, and a word error, errors, fatal, panic, exception, traceback, critical, segfault, segmentation fault, oomkilled or out of memory, after LOG-4 removes the benign phrases. | `0 errors but FATAL: config missing` | `TestIsErrorLineStillFindsErrors` | `TestSilentLinesThatCountZeroErrorsAreNotErrors` | none |
| LOG-4 | Lines that count zero errors | `shape/logs.go` `benignErrRE` | Phrases such as `0 errors`, `no errors`, `without errors`, `no error found`, `errors=0`, `error_count: 0` are removed before LOG-3. | (the line is not an error) | none | `TestSilentLinesThatCountZeroErrorsAreNotErrors`, `TestSilentLastErrorIsNotAZeroErrorLine` | none |
| LOG-5 | JSON record level | `shape/logs.go` `readableJSON` | A JSON object with a message key. Its level key decides as LOG-1; with no level, LOG-1 to LOG-4 run on the rewritten line `LEVEL message key=value ...`. | `INFO job started. job_id=25dbec4d-... job_type=new-accounts` | `TestLogsShowJSONMessages` | `TestLogsShowJSONMessages` (an INFO record is not an error) | 400 bytes of fields. Reason not stated. |
| LOG-6 | A trace is one entry | `shape/logs.go` `ParseLogs`, `joinsEntry` | An indented line, `at `, `Caused by`, `... N more`, `goroutine N`, `[signal`, an exception line after an error, a chain line after a Python traceback, or a Go frame after a panic, joins the entry before it. | (one group for the trace) | `TestStackTraceIsOneGroupAcrossPods`, `TestGoPanicIsOneGroupWithTheApplicationFrame`, `TestPythonChainedTracebacks` | none | 200 continuation lines. Reason not stated. |
| LOG-7 | The error line of a trace | `shape/logs.go` `Condense`, `ErrorLine` | A Python traceback shows its exception line and last application frame; a Java trace its head and every `Caused by:` line. The quote of POD-12 uses this line. | `aiokafka.errors.LeaderNotAvailableError: [Error 5] LeaderNotAvailableError at /app/consumer/main.py:42 (run)` | `TestPythonTracebackShowsTheExceptionLine`, `TestErrorLineKeepsTheCausedByLine`, `TestLastErrorOfATracebackIsItsExceptionLine`, `TestLastErrorOfAJavaTraceNamesTheCause` | `TestLastErrorOfATracebackIsItsExceptionLine` (not the word Traceback) | none |

## Rules with no silent-case test

- POD-2 OOM kill with no limit
- POD-3 Run time before the OOM kill
- POD-4 Volume attached to another node
- POD-5 Image pull: access refused
- POD-6 Image pull: not found
- POD-7 Image pull: registry not reachable
- POD-8 Image pull: name not valid
- POD-9 Image pull: other
- POD-10 Missing ConfigMap or Secret
- POD-16 Last error in the log of a container that is not ready
- POD-17 Not ready: startup probe
- POD-18 Restart with no event
- POD-25 Earlier attempts of a healthy Job
- SCH-2 Tolerations of the pod
- SCH-3 Node selection of the pod
- SCH-4 Larger than any node
- SCH-7 No node has both
- SCH-8 Every node cordoned
- SCH-9 Notes of the room check
- SVC-2 No selector
- SVC-3 Selected pods, none ready
- SVC-5 No pod has the label
- SVC-6 Pods with other values of the label
- SVC-7 targetPort name that no container declares
- SVC-9 EndpointSlices with endpoints and no ports
- SVC-12 List row cause of a Service
- REV-2 Restart only
- SIB-4 Age
- SIB-6 Revision folds image, digest and age
- CND-3 Soft types
- CND-6 Autoscaler message kept longer
- CND-7 Replicas of any other kind
- REL-3 HPA target missing
- REL-9 CronJob result not known
- REL-11 CronJob suspended
- REL-12 Recent Jobs of a CronJob
- REL-19 ConfigMap missing
- REL-23 Selected pod with a problem
- REL-25 Ingress backend missing
- REL-26 Ingress backend port missing
- RES-1 Near OOM
- RES-2 CPU at limit
- RES-3 Above memory request
- RES-5 No memory limit
- RES-6 CPU over-provisioned
- RES-7 Memory over-provisioned
- HLT-1 Pending pod is a warning
- HLT-2 Pod not scheduled
- HLT-3 Container waits with a reason
- HLT-4 App container terminated with an error
- HLT-8 Running but not ready
- HLT-9 Not started
- HLT-11 Terminating too long
- HLT-12 Restart-only row
- HLT-15 Deployment not progressing
- HLT-16 Deployment or ReplicaSet replica failure
- HLT-17 Deployment paused
- HLT-18 StatefulSet short of ready replicas
- HLT-20 DaemonSet short of ready pods
- HLT-22 DaemonSet misscheduled
- HLT-27 Volume failed
- HLT-28 Node not ready
- HLT-29 Node pressure
- HLT-30 Node cordoned
- HLT-31 Namespace terminating
- HLT-32 Warning event
- LOG-1 A level field decides alone
- LOG-2 klog error or fatal
- LOG-6 A trace is one entry

These rules have no test that proves they fire: POD-2, POD-5, POD-7, POD-8,
POD-16, POD-17, SCH-7, SCH-8, SCH-9, SVC-2, SVC-5, SVC-9, SIB-4, SIB-6, CND-3,
REL-11, REL-19, REL-23, RES-3, HLT-1 (as a state with no line), HLT-9, HLT-11,
HLT-15, HLT-17, HLT-22, HLT-27, HLT-30, HLT-31, HLT-32, LOG-1.

## Thresholds with no stated reason

The value is in the code and often in the docs, but no reason for the value
is written in the code, the tests, the docs or the commit messages.

Rule thresholds:

- `tools/diagnose.go` `nearOOMPercent` 85 (RES-1)
- `tools/diagnose.go` `cpuAtLimitPercent` 90 (RES-2)
- `tools/diagnose.go` `aboveRequestFactor` 1.1 (RES-3)
- `tools/diagnose.go` `overProvisioned` 4, `minCPURequest` 100m, `minMemRequest` 128Mi (RES-6, RES-7)
- `tools/diagnose.go` `oomRunTime`: 2 minutes and 1 hour (POD-3). The code states why a short run is not a leak; it does not state why 2 minutes or 1 hour.
- `shape/health.go` `podRow`: 3 restarts (HLT-7); `tools/describe.go` `podEarlier`: 3 restarts (POD-23)
- `shape/health.go` `podRow`: 2 minutes before `not started` (HLT-9)
- `shape/health.go` `podRow`: 5 minutes before `terminating for` (HLT-11)
- `shape/conditions.go` `statusFacts`: 3 minutes of HPA age (REL-2)
- `shape/conditions.go` `cronFacts`: grace 2 minutes (REL-7), and 2 minutes after the last schedule (REL-8, REL-9)
- `shape/conditions.go` `cronFacts`: 26 hours more grace with `spec.timeZone` (REL-7). The code states why there is more grace; it does not state why 26 hours.
- `tools/related_kinds.go` `relNodePods`: 2 minutes (REL-28). The code states why young pods are left out; it does not state why 2 minutes.
- `tools/difference.go` `diffYoungRollout` 1 hour (REV-1)
- `tools/difference.go` `diffMinGap` 1 minute (SIB-4)
- `tools/diagnose.go` `similar`: 4 bytes of shared prefix, half of the shorter name (POD-10, SVC-6)
- `tools/diagnose.go` `containerLog`: 400 ms wait before the second read (POD-12). The code states why it reads again; it does not state why 400 ms.

Byte caps of causes and lines:

- `tools/causes.go` `rowCauseMax` 160 and `deepCauseMax` 240 (the docs state why the cause is on the row, not why these sizes)
- `tools/causes.go` `compactCause`: the head keeps 55% when a quote follows; `middleCut` keeps 2/5 and 3/5; at least 30 bytes of quote
- `tools/list_large.go` `shortCauseMax` 100
- `tools/diagnose.go` `containerLog`: quote of the last error cut at 360 bytes
- `tools/diagnose.go` `podCauses`: probe messages cut at 200 bytes (POD-11, POD-13, POD-15, POD-17)
- `tools/diagnose.go` `imagePullCause`: 200 bytes (POD-9)
- `tools/diagnose.go` `placementCause` 900, `tolerationsText` 300, `nodeSelectionText` 400 bytes (SCH-1 to SCH-3)
- `shape/conditions.go` `conditionMessageMax` 200 (CND-1)
- `shape/health.go` `DefaultProblemMax` 300
- `tools/related.go` `relReplicaSets`: 220 bytes of event (REL-17)
- `tools/describe.go` `briefInitCause`: 160 bytes of command and of log line (POD-19)

Read caps that can keep a rule silent:

- `tools/diagnose.go` `crashLogTail` 200 lines, `crashLogBytes` 256 KiB, `crashLogBudget` 1,200 bytes (POD-12, POD-16)
- `tools/diagnose.go` `schedulingPodCap` 5,000 pods and 2,000 nodes (SCH-4 to SCH-9); `tools/top.go` `topListCap` 5,000
- `tools/diagnose.go` `diagnoseWorkload`: 200 pods; `serviceCause`: 200 and 500 pods; `neighbors`: 200 ConfigMaps
- `tools/describe.go` `serviceEndpoints` and `tools/diagnose.go` `servicePortMismatch`: 50 EndpointSlices
- `tools/list.go` `fetchCap` 2,000 and `causesInList` 3
- `tools/related.go` `relatedListCap` 200, `relatedLinesMax` 8, `relatedPeersMax` 6, `relatedLogLines` 6; 50 quotas; 2 ReplicaSets with events; 3 recent Jobs of a CronJob; 2 init containers; 10 Ingress backends
- `tools/difference.go` `diffLinesMax` 8 and `diffValueMax` 80
- `tools/describe.go` `lastLogLine`: 20 lines and 16 KiB
- `shape/logs.go` `maxMore` 200 continuation lines; `readableJSON` 400 bytes of fields

`relatedConfigMaps` 3 has its reason in `docs/architecture.md` 7.7 (it bounds
the reads, and the section lists it under what this gives up).
