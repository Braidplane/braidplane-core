"""Check a single-platform OCI archive against its expected image digest."""
import hashlib
import json
import re
import tarfile


IMAGE_MANIFESTS = {"application/vnd.oci.image.manifest.v1+json", "application/vnd.docker.distribution.manifest.v2+json"}
IMAGE_CONFIGS = {"application/vnd.oci.image.config.v1+json", "application/vnd.docker.container.image.v1+json"}


def unique_keys(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            raise ValueError(f"duplicate OCI key: {key}")
        result[key] = value
    return result


def verify_archive(path, expected_digest, platform):
    if not re.fullmatch(r"sha256:[0-9a-f]{64}", expected_digest) or platform not in ("linux/amd64", "linux/arm64"):
        raise ValueError("invalid expected OCI identity")
    with tarfile.open(path, "r:") as archive:
        members = archive.getmembers()
        names = [entry.name for entry in members]
        if len(names) != len(set(names)) or any(not (entry.isfile() or entry.isdir()) for entry in members):
            raise ValueError("OCI archive contains duplicate names or unsupported entries")
        entries = {entry.name: entry for entry in members}

        def content(name, limit=None):
            member = entries.get(name)
            if member is None or not member.isfile() or (limit is not None and member.size > limit):
                raise ValueError(f"missing or invalid OCI entry: {name}")
            return archive.extractfile(member)

        def blob(descriptor, json_blob=False):
            if not isinstance(descriptor, dict):
                raise ValueError("invalid OCI descriptor")
            digest, size = descriptor.get("digest"), descriptor.get("size")
            if not isinstance(digest, str) or not re.fullmatch(r"sha256:[0-9a-f]{64}", digest) or type(size) is not int or size < 0:
                raise ValueError("invalid OCI digest or size")
            name = "blobs/sha256/" + digest[7:]
            member = entries.get(name)
            if member is None or member.size != size:
                raise ValueError("OCI blob size differs from its descriptor")
            data = bytearray() if json_blob else None
            if json_blob and size > 1048576:
                raise ValueError("OCI metadata is too large")
            hashed = hashlib.sha256()
            with content(name) as stream:
                while chunk := stream.read(1024 * 1024):
                    hashed.update(chunk)
                    if data is not None:
                        data.extend(chunk)
            if hashed.hexdigest() != digest[7:]:
                raise ValueError("OCI blob bytes differ from their digest")
            return json.loads(data, object_pairs_hook=unique_keys) if data is not None else None

        with content("index.json", 1048576) as stream:
            index = json.load(stream, object_pairs_hook=unique_keys)
        manifests = index.get("manifests") if isinstance(index, dict) else None
        if (not isinstance(index, dict) or index.get("schemaVersion") != 2 or not isinstance(manifests, list) or len(manifests) != 1 or
                not isinstance(manifests[0], dict) or manifests[0].get("digest") != expected_digest or
                manifests[0].get("mediaType") not in IMAGE_MANIFESTS):
            raise ValueError("OCI archive must contain the expected single image manifest")
        manifest = blob(manifests[0], json_blob=True)
        if (not isinstance(manifest, dict) or manifest.get("schemaVersion") != 2 or
                manifest.get("mediaType") not in IMAGE_MANIFESTS or not isinstance(manifest.get("layers"), list) or
                not isinstance(manifest.get("config"), dict) or manifest["config"].get("mediaType") not in IMAGE_CONFIGS):
            raise ValueError("invalid OCI image manifest")
        config = blob(manifest["config"], json_blob=True)
        if not isinstance(config, dict) or f"{config.get('os')}/{config.get('architecture')}" != platform:
            raise ValueError("OCI image platform differs from the candidate")
        for layer in manifest["layers"]:
            blob(layer)
