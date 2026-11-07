"""Keep one public-docs source tree environment-correct at build time."""

from __future__ import annotations

import os
from pathlib import Path
from urllib.parse import urlparse


OPENAPI_PLACEHOLDER = "{{ public_openapi_url }}"
SUPPORTED_ENVIRONMENTS = {"production", "staging"}


def _https_url(name: str) -> str:
    """Load one public URL and reject values that are not absolute HTTPS URLs."""
    value = (os.getenv(name) or "").strip()
    if not value:
        raise ValueError(f"{name} is required")

    parsed = urlparse(value)
    if (
        parsed.scheme != "https"
        or not parsed.netloc
        or parsed.username is not None
        or parsed.password is not None
        or parsed.params
        or parsed.query
        or parsed.fragment
    ):
        raise ValueError(
            f"{name} must be an absolute credential-free HTTPS URL without params, query, or fragment"
        )
    return value


def on_config(config):
    """Apply and validate the environment-specific canonical and OpenAPI URLs."""
    environment = (os.getenv("PUBLIC_DOCS_ENVIRONMENT") or "").strip()
    if environment not in SUPPORTED_ENVIRONMENTS:
        raise ValueError("PUBLIC_DOCS_ENVIRONMENT must be production or staging")

    site_url = _https_url("PUBLIC_DOCS_SITE_URL")
    openapi_url = _https_url("PUBLIC_DOCS_OPENAPI_URL")
    config["site_url"] = site_url.rstrip("/") + "/"
    config.extra["public_docs_environment"] = environment
    config.extra["public_openapi_url"] = openapi_url
    return config


def on_page_markdown(markdown, page, config, files):
    """Render the configured OpenAPI URL without adding a templating dependency."""
    if OPENAPI_PLACEHOLDER not in markdown:
        return markdown
    return markdown.replace(OPENAPI_PLACEHOLDER, config.extra["public_openapi_url"])


def on_post_build(config):
    """Advertise the canonical sitemap only on the indexable public build."""
    site_dir = Path(config["site_dir"])
    if config.extra["public_docs_environment"] != "staging":
        (site_dir / "robots.txt").write_text(
            "User-agent: *\nAllow: /\n\nSitemap: "
            + config["site_url"]
            + "sitemap.xml\n",
            encoding="utf-8",
        )
        return

    (site_dir / "robots.txt").write_text(
        "User-agent: *\nDisallow: /\n",
        encoding="utf-8",
    )
    for name in ("sitemap.xml", "sitemap.xml.gz"):
        (site_dir / name).unlink(missing_ok=True)
