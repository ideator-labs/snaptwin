# snaptwin

Restore one VM memory snapshot twice and see which "unique" values your app hands out twice.

Lambda MicroVMs, Lambda SnapStart and Firecracker all start new instances from one snapshot of an initialized app. Anything the app generated during init (an instance ID, a signing key, a seeded random generator) is copied into every instance. AWS documents the rule and leaves the checking to you. snaptwin does the checking: it boots your app in a VM, snapshots it after the health check passes, restores the snapshot into two clones, calls your restore hook, then probes each clone and fails when a probe answers the same in both.

```
$ bash demo.sh
SHARED  instance-id      ab9c86e5fc96011a | ab9c86e5fc96011a
SHARED  session-token    b18e1fc2abed80bd511af95c79da6a7c | b18e1fc2abed80bd511af95c79da6a7c
SHARED  request-id       80fe68a9c6bf397b | 80fe68a9c6bf397b
UNIQUE  fresh-random     fd95de4f9c4defe3e657f313c019be04 | ef6b200391209a504ecf91e03c719b8b
FAIL  3/4 probes returned the same value in more than one snapshot clone
...
6 passed, 0 failed
```

The same app booted fresh twice passes every probe. That is why ordinary tests never see this bug.

This is a proof of concept. The VM is QEMU (software emulation, no KVM needed), not Firecracker. The guest is a static Go binary in an initramfs, not a Dockerfile. Read [AGENTS.md](AGENTS.md) for what is real, what is a stand-in, how to run it and what to build next.

Requirements: Linux x86_64, Go 1.24+, `qemu-system-x86_64`, curl. `bash demo.sh` downloads a 44 MB guest kernel once and checks its SHA-256.
