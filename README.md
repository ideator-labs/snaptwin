# snaptwin

Restore one VM memory snapshot twice and see which "unique" values your app hands out twice.

Lambda MicroVMs, Lambda SnapStart and Firecracker all start new instances from one snapshot of an initialized app. Anything the app generated during init (an instance ID, a signing key, a seeded random generator) is copied into every instance. AWS documents the rule and leaves the checking to you. snaptwin does the checking: it boots your app in a VM, snapshots it after the health check passes, restores the snapshot into two clones, calls your restore hook, then probes each clone and fails when a probe answers the same in both.

```
$ bash demo.sh
SHARED  instance-id      f74d8f0ec75e65d4 | f74d8f0ec75e65d4
SHARED  session-token    d79aa45483b0fdad807448a82bd823ff | d79aa45483b0fdad807448a82bd823ff
SHARED  request-id       788f2393790a6703 | 788f2393790a6703
UNIQUE  global-rand      5b17e0d0318bb5ea | 676364cf22b4562b
UNIQUE  fresh-random     25a5f8de1e355081fb4ec3a462cf11c6 | 262d2729dc290fee352fa9efa954fb84
FAIL  3/5 probes returned the same value in more than one snapshot clone
...
6 passed, 0 failed
```

The same app booted fresh twice passes every probe. That is why ordinary tests never see this bug.

This is a proof of concept. The VM is QEMU (software emulation, no KVM needed), not Firecracker. The guest is a static Go binary in an initramfs, not a Dockerfile. Read [AGENTS.md](AGENTS.md) for what is real, what is a stand-in, how to run it and what to build next.

Requirements: Linux x86_64, Go 1.24+, `qemu-system-x86_64`, curl. `bash demo.sh` downloads a 44 MB guest kernel once and checks its SHA-256.
