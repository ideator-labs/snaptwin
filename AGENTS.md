# AGENTS.md

Handoff for a coding agent with no prior context. Read this before changing anything.

## What this is

snaptwin is a CLI that checks whether an app is safe to clone from a VM memory snapshot.

Platforms such as AWS Lambda MicroVMs (Firecracker based) build an image by starting your app, waiting for it to be ready, and snapshotting memory, disk and processes. Every MicroVM is then restored from that one snapshot. Values your app created during init are identical in every instance unless you regenerate them in the `/run` lifecycle hook. AWS documents this. Its only checker is a SpotBugs scanner for Java SnapStart functions.

The claim this repo proves: restoring one snapshot into two clones and probing them finds per-instance values that are shared, and the same probes against two fresh boots find nothing. A correct `/run` hook makes the probes pass.

## Status

Real:
- A real VM memory snapshot. QEMU pauses the guest, writes full RAM and device state to a file (`migrate file:`), and each clone is a new QEMU process loading that file with `-incoming`.
- A real Linux guest. Firecracker's CI kernel 6.1.155 boots with an initramfs whose `/init` is the demo app.
- A real kernel RNG reseed on restore. Each clone gets a `vmgenid` device with a new GUID, which the guest kernel uses to reseed. This mirrors what AWS says Lambda does on restore.
- Real probes over HTTP through QEMU user networking, and a real pass or fail exit code.

Stand-in or not built:
- QEMU with software emulation (TCG), not Firecracker. Firecracker v1.17.0 was tried first on the build box. KVM there is nested and crashes in `kvm_arch_vcpu_create` (kernel oops), so Firecracker cannot run on it. QEMU snapshots have the same full-memory semantics. The Firecracker backend is the first next step.
- The guest app is one static Go binary packed by `cmd/mkinitrd`. There is no Dockerfile to rootfs path yet.
- The demo app's bugs are deliberate (`examples/guestapp`).

## Data shape

Input, `snaptwin.json`:

```json
{
  "health": "/health",
  "hook": {"method": "POST", "path": "/run"},
  "probes": [{"name": "instance-id", "path": "/instance-id"}]
}
```

- `health` must answer 200 before the snapshot is taken and before a clone is probed.
- `hook` is optional. It is called once on each clone after restore, like the Lambda MicroVMs `/run` hook. Its status is reported but does not fail the run.
- Every probe is a GET whose 200 body must differ between clones.

Output, `--json report.json`:

```json
{
  "mode": "snapshot",
  "clones": [{"index": 1, "hook_status": 404, "values": {"instance-id": "ab9c..."}}],
  "verdicts": [{"probe": "instance-id", "values": ["ab9c...", "ab9c..."], "unique": false}],
  "pass": false
}
```

Exit codes: 0 every probe unique, 1 at least one probe shared, 2 setup error.

## Repo map

- `cmd/snaptwin/main.go` owns the spec, the build-snapshot-then-restore flow, probing, the verdict (`judge`) and the report.
- `cmd/snaptwin/vm.go` owns QEMU: process start, QMP, `Snapshot` (stop then migrate to file) and `Resume` (wait for the incoming load, then `cont`).
- `cmd/mkinitrd/main.go` writes a gzipped newc initramfs with one binary as `/init` plus `/dev/console`. No `cpio` binary needed.
- `examples/guestapp/main.go` is the demo app. Without `snaptwin.mode=fixed` on the kernel command line it has no `/run` hook. With it, `POST /run` regenerates the instance ID, signing key and request ID generator.
- `examples/guestapp/snaptwin.json` is the spec the demo uses.
- `demo.sh` downloads and verifies the kernel, builds everything, runs four scenarios and prints numbered checks.

## Setup, run, verify (fresh clone)

```bash
# prerequisites on Debian or Ubuntu
sudo apt-get install -y qemu-system-x86 curl
go version   # 1.24 or newer

git clone https://github.com/ideator-labs/snaptwin
cd snaptwin
bash demo.sh            # expect "6 passed, 0 failed", exit 0, about 10 to 20 seconds
bash demo.sh --break    # expect checks 1 and 2 to FAIL, exit 1
bash demo.sh            # passes again
```

`--break` makes the buggy scenario boot each clone fresh instead of restoring the snapshot. Checks 1 and 2 must fail. If they pass, the demo no longer proves anything.

Run against your own guest:

```bash
go build -o build/snaptwin ./cmd/snaptwin
./build/snaptwin --spec snaptwin.json --kernel .cache/vmlinux-6.1.155 \
  --initrd my-app.cpio.gz --clones 3 --json report.json
```

The app must listen on TCP 8080 inside the guest (`--port` changes it). The default `--append` gives the guest 10.0.2.15 through the kernel `ip=` parameter.

## Design decisions

- Black-box probes over static analysis. AWS's existing checker is SpotBugs rules for Java only. Probing a restored instance works for any language and also catches state hidden in dependencies.
- Two restores, never one restore plus the original. The original and a clone can differ for reasons unrelated to the snapshot. Two restores of one file isolate exactly what the snapshot carries.
- The snapshot is taken paused (`stop` then `migrate`) so memory cannot change while it is written. QEMU then loads each clone paused, so `Resume` sends `cont`.
- Clones run one after another, each on a free host port. Concurrency adds nothing to the verdict and doubles memory.
- One guest kernel pinned by URL and SHA-256 in `demo.sh`. It is Firecracker's CI kernel, so the Firecracker backend can reuse it.

## Known limits

- QEMU TCG, not Firecracker or KVM. Restore timing and devices differ from Lambda.
- UNIQUE does not mean safe. The demo app also serves `/global-rand`, which returns Go's top-level `math/rand/v2` value. The Go runtime seeds that generator once per thread at process start (`runtime/rand.go`), so a snapshot clones it. In 2-clone runs it came back unique every time. In one 3-clone run it came back SHARED in all three. Inferred cause: the clones consume a timing-dependent number of runtime random draws before the probe. That is why it is not in the demo spec, and why a single probe can miss a shared generator. Try it with `--clones 3` and a spec that includes `/global-rand`.
- Kernel reseed depends on the VM. In one early run on QEMU's `microvm` machine (no vmgenid device) `/fresh-random`, which reads `crypto/rand` per request, came back SHARED. On `q35` without vmgenid it came back unique. That was not deterministic, so the demo does not check it.
- Only GET probes with exact body comparison. No headers, no JSON field selection, no POST probes.
- Linux x86_64 only. Needs `qemu-system-x86_64` on PATH.
- No Dockerfile or OCI image input.

## Next steps

1. Firecracker backend on a KVM host. Done when `snaptwin --vmm firecracker` runs the same spec against the same kernel and initrd and `demo.sh` passes with it on a machine where `/dev/kvm` works.
2. OCI image input. Done when `snaptwin --image ghcr.io/x/y:tag` builds an ext4 rootfs from the image, boots its ENTRYPOINT, and the demo app packaged as a Dockerfile gives the same 3 SHARED probes.
3. Lambda MicroVMs hook parity. Done when the spec supports `/ready`, `/run`, `/suspend`, `/resume` with the same semantics as the AWS docs, and a suspend then resume cycle is tested.
4. Repeat probes. Done when `--samples N` probes each value N times per clone and a probe is SHARED if any sample matches across clones, and a fixture that draws a timing-dependent number of values is flagged.
5. GitHub Action. Done when a workflow in this repo runs snaptwin on the demo app and fails the job on SHARED.
6. Add a LICENSE before any launch.

## Working rules

- `bash demo.sh` is the artifact. Run it, and `--break`, after every change. Do not report success from `go build` alone.
- Keep the guest app's bugs deliberate and documented in its package comment.
- Name the data shape first when adding features. Spec in, report out.
- No em-dashes in prose. Short sentences. Honest limits.
- Commit as `danielgerlag <daniel@gerlag.ca>`.

## Context and sources

- Lambda MicroVMs expanded to 4 more regions on Oct 22, 2026: https://aws.amazon.com/about-aws/whats-new/2026/10/lambda-microvms-4-additional-regions/
- How Lambda builds the snapshot and the lifecycle hooks: https://docs.aws.amazon.com/lambda/latest/dg/microvms-how-it-works.html
- Snapshot uniqueness rules for MicroVMs: https://docs.aws.amazon.com/lambda/latest/dg/microvms-images-snapshots.html
- MicroVMs best practices (generate IDs in `/run`): https://docs.aws.amazon.com/lambda/latest/dg/microvms-best-practices.html
- SnapStart uniqueness and the Java-only scanner: https://docs.aws.amazon.com/lambda/latest/dg/snapstart-uniqueness.html , https://github.com/aws/aws-lambda-snapstart-java-rules
- Firecracker on random state in clones: https://github.com/firecracker-microvm/firecracker/blob/main/docs/snapshotting/random-for-clones.md
- An HN thread asking the same question of another snapshot platform: https://news.ycombinator.com/item?id=49101007
