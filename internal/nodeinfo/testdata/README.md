# Upstream CPU fixtures

`upstream-cpu-*.json` wrap devices produced by `device.Build` from
[dra-driver-cpu commit 009e907](https://github.com/kubernetes-sigs/dra-driver-cpu/tree/009e907a3d27a2a4a008eaa7fd4df37a90d4d45e).
Each fixture contains one node-local ResourceSlice with pool `node`.

Both use `LayoutSocket`, `ExposeExtAttrs: true`, and a two-socket topology with
four physical cores per socket. The first core of each socket is reserved:

| Fixture                    | Socket 0 CPUs | Socket 1 CPUs | Reserved CPUs | Sibling stride |
| -------------------------- | ------------- | ------------- | ------------- | -------------- |
| `upstream-cpu-no-smt.json` | 0-3           | 4-7           | 0,4           | None           |
| `upstream-cpu-smt.json`    | 0-3,8-11      | 4-7,12-15     | 0,4,8,12      | 8              |

The fixtures retain upstream device names, capacity, and topology attributes.
Node-allocatable resource mappings and PCIe attributes are disabled, matching
the bridge's driver configuration.
