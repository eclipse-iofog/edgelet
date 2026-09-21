# Publish Model and Knowledge as OCI artifacts

This page is for people who **package** model weights or retrieval files and push them to a registry Edgelet will pull.

Edgelet does **not** treat these as container images. Do not `docker build` a Dockerfile around the files. Push an **OCI artifact** (ORAS or a model packaging format). The node copies layers into `{diskDirectory}/models/` or `{diskDirectory}/knowledge/` and materializes **`content/`**. `spec.files` is **ignored** for OCI — the full artifact is always extracted.

Operator pull and bind: [models.md](models.md), [knowledge.md](knowledge.md). YAML: [manifest-reference.md](manifest-reference.md).

---

## When to use OCI vs Hugging Face

| Source | Use when |
|--------|----------|
| **OCI** (`registry.type: oci`) | Private or enterprise registry, air-gap mirror, pinned digest, or you already ship ORAS / Docker Model / ModelKit |
| **Hugging Face** (`type: hf`) | Public or enterprise Hub. **Model** uses the Hub **model** API. **Knowledge** uses the Hub **dataset** API |

`spec.repo` never includes the registry host. The host lives on the Registry row (`spec.url`).

---

## What the node does on pull

1. Resolve `spec.repo` + `spec.revision` against that registry.
   - Empty revision → tag **`latest`**.
   - `sha256:` + 64 hex → digest.
   - Anything else → tag.
2. Fetch the **manifest and all layers**. Model and Knowledge pull never use the container-engine image pull path.
3. Unpack into `{metadata.name}/content/`.
4. Write `{metadata.name}/manifest.json` with the **manifest digest**.

**Model** tries packaging formats in this order:

1. Docker model-spec
2. CNCF ModelPack
3. KitOps ModelKit
4. Generic ORAS

**Knowledge** always uses **generic ORAS**. It does not run those three detectors. If you publish Knowledge as model-spec, Edgelet still unpacks it as ORAS (filenames from annotations, not Docker’s `model.gguf` rules).

Tag and digest pulls of the same blob share storage in that kind’s `oci-store/`. Model blobs live under `{diskDirectory}/models/oci-store/`. Knowledge blobs live under `{diskDirectory}/knowledge/oci-store/` and never write the Model store.

---

## How generic ORAS becomes `content/`

Used for **Knowledge** and for **Model** when the artifact is not model-spec / ModelPack / ModelKit. At least one layer is required.

| Layer | Result in `content/` |
|-------|----------------------|
| Media type contains **`tar`** (for example `application/vnd.oci.image.layer.v1.tar+gzip`) | Archive is **extracted** as a directory tree |
| Regular file layer | Filename from `org.cncf.model.filepath`, else `org.opencontainers.image.title` |
| No name, **one** file layer | `model.bin` |
| No name, several file layers | `layer-0`, `layer-1`, … plus an extension guessed from the media type |

`oras push` sets `org.opencontainers.image.title` from the local filename. That is the easiest way to get stable names.

---

## Publish Knowledge

Pack the tree the microservice should see under `{bindPath}/{name}/`.

### Individual files

Best when you have a few named files:

```bash
oras push registry.example.com/acme/wiki-index:v1 \
  --artifact-type application/vnd.unknown.artifact.v1 \
  ./index/faiss.index:application/octet-stream \
  ./chunks/wiki.jsonl:application/jsonl
```

`oras` titles are **basenames**. After `bindPath: /knowledge` and `name: wiki-faiss`, the container sees:

```
/knowledge/wiki-faiss/faiss.index
/knowledge/wiki-faiss/wiki.jsonl
```

To keep subdirectory paths, use a tar layer (below) or set `org.cncf.model.filepath` on each layer.

### Directory as one tar layer

Best for a corpus or an index plus chunks:

```bash
tar -C ./corpus -czf /tmp/corpus.tar.gz .
oras push registry.example.com/acme/product-docs:v1 \
  --artifact-type application/vnd.unknown.artifact.v1 \
  /tmp/corpus.tar.gz:application/vnd.oci.image.layer.v1.tar+gzip
```

Everything under `./corpus` appears under `content/` with the same relative paths.

### Consume on the node

```yaml
apiVersion: edgelet.iofog.org/v1
kind: Registry
spec:
  id: 10
  type: oci
  url: registry.example.com
  private: true
  username: robot
  password: *****
---
apiVersion: edgelet.iofog.org/v1
kind: Knowledge
metadata:
  name: wiki-faiss
spec:
  repo: acme/wiki-index          # no host
  revision: sha256:<manifest-digest>
  registry: 10
  files: []                      # ignored for oci
  format: faiss
```

Record the digest after push:

```bash
oras manifest fetch --descriptor registry.example.com/acme/wiki-index:v1
```

Prefer a digest on production nodes. A tag (`v1`, `latest`) sets `revisionFloating: true` and a later reconcile can pick a new manifest.

---

## Publish Model

Pick one packaging style. Edgelet detects it on pull.

### Docker model-spec

Use Docker’s model tooling (`docker model package` / `docker model push`) or any client that writes:

- Config media type starting with `application/vnd.docker.ai.model.config`
- Layers such as `application/vnd.docker.ai.gguf.v3`

A single GGUF layer becomes **`content/model.gguf`**. License and chat-template layers become `LICENSE` and `template.jinja`. Use this for **`kind: Model`**, not Knowledge.

### CNCF ModelPack

Manifest, config, or layer media types under `application/vnd.cncf.model.…`. File names come from **`org.cncf.model.filepath`** (for example `weights/model.safetensors`).

### KitOps ModelKit

`kit pack` / `kit push`. Detected from KitOps or Jozu config media types or a Kitfile annotation.

### Generic ORAS

Fine for a raw GGUF or ONNX file:

```bash
oras push registry.example.com/acme/tiny-llm:1.0 \
  --artifact-type application/vnd.unknown.artifact.v1 \
  ./tiny.gguf:application/octet-stream
```

That becomes `content/tiny.gguf` (title annotation).

```yaml
apiVersion: edgelet.iofog.org/v1
kind: Model
metadata:
  name: tiny-llm
spec:
  repo: acme/tiny-llm
  revision: sha256:<digest>
  registry: 10
  format: gguf
```

---

## Do not do this

| Mistake | What happens |
|---------|----------------|
| `docker build` and `docker push` a container that contains the files | Edgelet pulls an image-shaped artifact. Unpack may fail or dump unnamed layers. Use ORAS or model tooling |
| Put the host in `spec.repo` (`registry.example.com/acme/wiki`) | Validate error. Host is on the Registry |
| Point Knowledge at a Hugging Face **model** repo | Knowledge always calls the dataset API and fails. Republish as a **dataset** or as OCI |
| Expect `spec.files` to subset an OCI artifact | The list is ignored. Split the artifact, or use Hugging Face and `files` |
| Publish Knowledge as Docker model-spec hoping for `model.gguf` | Knowledge skips that unpacker. Use ORAS titles or a tar |
| Float `revision: latest` or `v1` in production | `revisionFloating: true`; a later reconcile can pick a new manifest. Pin `sha256:…` |

---

## Registry, auth, and TLS

- Built-in **id 1** is `docker.io` (`oci`). Add a user row (id 4+) for a private host.
- Private OCI: `username` + `password`.
- Extra CA: `spec.ca` (base64 PEM). `insecure: true` allows `http://` and skips TLS verify.
- Image pull and `edgelet image pull` still require `type: oci`.

`oras` login must use the **same** host and credentials as the Edgelet Registry row.

---

## After pull

```
{diskDirectory}/models/{name}/content/      # Model
{diskDirectory}/knowledge/{name}/content/   # Knowledge
```

Catalog bind is `{bindPath}/{metadata.name}/` = that `content/` tree. The application should not assume Docker-only names unless you published model-spec as a **Model**.

```bash
edgelet model inspect tiny-llm
edgelet knowledge inspect wiki-faiss
```

Check `digest`, `revisionFloating`, and that the listed content paths match what you pushed.

---

## Related docs

| Document | Topic |
|----------|--------|
| [models.md](models.md) | Model deploy, bind, prune |
| [knowledge.md](knowledge.md) | Knowledge deploy, bind, prune |
| [manifest-reference.md](manifest-reference.md) | Registry + Model + Knowledge YAML |
| [examples/model.yaml](examples/model.yaml) | HF GGUF + OCI tag/digest samples |
| [examples/knowledge.yaml](examples/knowledge.yaml) | HF dataset + OCI tag/digest samples |
| [examples/registry.yaml](examples/registry.yaml) | OCI and HF registry rows |
