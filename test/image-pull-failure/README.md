# Image pull failure

Lima checks for a registry image that cannot be downloaded.

| Case | What it checks |
|------|----------------|
| Nothing running yet | `edgelet deploy` of a missing image stays **queued**. `imagePullStatus` is `failed`. `errorMessage` contains the pull error. No container is created. `restartCount` stays 0. |
| Controller already running | A running controller container is left in place when the new image is not on the machine. Status stays **running**, with the same pull failure. A later retry does not replace that container. |

The suite uses the docker engine on the `edgelet-engine-lifecycle` Lima VM. Install wipes that VM's edgelet database.

```bash
./test/image-pull-failure/run-all.sh
./test/image-pull-failure/run-all.sh --skip-setup --skip-build --skip-start
./test/run-all.sh --suite=image-pull-failure
```

`--skip-build` reuses `build/edgelet-linux-<arch>`. The daemon is the fat runtime inside that binary, so a build from this tree is required after process-manager changes.
