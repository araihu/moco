#!/usr/bin/env python3
"""Check the composed static artifact, including cross-generator links and metadata."""
from html.parser import HTMLParser
from pathlib import Path
from urllib.parse import unquote, urljoin, urlsplit
import struct
import json
import sys
from site_structured_data import validate as validate_structured_data

ORIGIN = "https://moco.araihu.com"
root = Path(sys.argv[1] if len(sys.argv) > 1 else "dist/site").resolve()


class Page(HTMLParser):
    def __init__(self, source):
        super().__init__(convert_charrefs=True)
        self.meta = {}
        self.canonical = []
        self.links = []
        self.ids = set()
        self.titles = 0
        self.feed(source)

    def handle_starttag(self, tag, attrs):
        attrs = dict(attrs)
        if attrs.get("id"):
            self.ids.add(attrs["id"])
        if tag == "title":
            self.titles += 1
        if tag == "meta":
            key = attrs.get("property", attrs.get("name", ""))
            self.meta.setdefault(key, []).append(attrs.get("content", ""))
        if tag == "link" and attrs.get("rel") == "canonical":
            self.canonical.append(attrs.get("href", ""))
        for attr in ("src", "href"):
            if attrs.get(attr):
                self.links.append((tag, attrs[attr]))


def require(condition, message):
    if not condition:
        raise SystemExit(message)


def local_file(url):
    path = root / unquote(urlsplit(url).path).lstrip("/")
    if path.is_dir():
        path /= "index.html"
    if not path.exists() and not path.suffix:
        path = path.with_suffix(".html")
    require(path.resolve().is_relative_to(root), f"Path escapes publication: {url}")
    return path


pages = [
    *sorted(path for path in root.rglob("*.html") if "openapi" not in path.relative_to(root).parts),
    root / "openapi/index.html",
    root / "openapi/catalogs/moco/index.html",
    root / "openapi/catalogs/moco/documents/public/index.html",
    root / "openapi/catalogs/moco/documents/internal/index.html",
]
required = (
    "description", "og:url", "og:type", "og:title", "og:description",
    "og:site_name", "og:image", "og:image:type", "og:image:width",
    "og:image:height", "og:image:alt", "twitter:card", "twitter:title",
    "twitter:description", "twitter:image", "twitter:image:alt",
)
checked = 0
for path in pages:
    require(path.is_file(), f"Missing page: {path.relative_to(root)}")
    page = Page(path.read_text())
    if "noindex" in " ".join(page.meta.get("robots", [])):
        continue
    if "openapi" not in path.relative_to(root).parts:
        validate_structured_data(path, root)
    route = "/" + str(path.relative_to(root)).removesuffix("index.html")
    if route.endswith(".html"):
        route = route.removesuffix(".html")
    url = ORIGIN + route
    require(page.titles == 1, f"{route}: expected one title")
    require(len(page.canonical) == 1, f"{route}: expected one canonical")
    require(page.canonical[0].rstrip("/") == url.rstrip("/"),
            f"{route}: incorrect canonical {page.canonical}")
    for key in required:
        require(len(page.meta.get(key, [])) == 1 and page.meta[key][0],
                f"{route}: missing or duplicate {key}")
    require(page.meta["og:url"] == page.canonical, f"{route}: og:url differs from canonical")
    require(page.meta["twitter:card"] == ["summary_large_image"], f"{route}: incorrect X Card")
    image = page.meta["og:image"][0]
    require(image.startswith(ORIGIN + "/"), f"{route}: image must use production origin")
    image_path = local_file(image)
    require(image_path.is_file(), f"{route}: missing social image {image}")
    data = image_path.read_bytes()
    require(data[:8] == b"\x89PNG\r\n\x1a\n", f"{route}: expected PNG preview")
    width, height = struct.unpack(">II", data[16:24])
    require((width, height) == (1280, 640) and len(data) < 1_000_000,
            f"{route}: invalid preview dimensions or size")
    require(page.meta["og:image:width"] == [str(width)] and
            page.meta["og:image:height"] == [str(height)], f"{route}: incorrect image metadata")
    require(page.meta["og:image:type"] == ["image/png"], f"{route}: incorrect image MIME")
    for tag, link in page.links:
        target = urljoin(url, link)
        parsed = urlsplit(target)
        if parsed.scheme not in ("http", "https"):
            continue
        if parsed.netloc != urlsplit(ORIGIN).netloc:
            require(tag == "a", f"{route}: external runtime dependency {link}")
            continue
        require(local_file(target).is_file(), f"{route}: broken local link {link}")
    checked += 1

document = (root / "openapi/catalogs/moco/documents/public/index.html").read_text()
class Descriptor(HTMLParser):
    def __init__(self):
        super().__init__()
        self.capture = False
        self.value = ""

    def handle_starttag(self, tag, attrs):
        if tag == "script":
            self.capture = dict(attrs).get("id") == "manja-local-docs-descriptor"

    def handle_endtag(self, tag):
        if tag == "script":
            self.capture = False

    def handle_data(self, data):
        if self.capture:
            self.value += data

descriptor = Descriptor()
descriptor.feed(document)
identity = json.loads(descriptor.value)
for path in (identity["publicationBase"], identity["static"]["deploymentBase"]):
    require(not {"api", "manage"}.intersection(path.split("/")),
            f"Manja reserves api/manage path segments: {path}")

for name in ("sitemap.xml", "llms.txt", "openapi/_manja/export.json", "openapi/sw.js"):
    require((root / name).is_file(), f"Missing publication artifact: {name}")
redirects = set()
for rule in (root / "_redirects").read_text().splitlines():
    source, target, status = rule.split()
    require(status == "301" and source.startswith("/") and source not in redirects,
            f"Invalid or duplicate documentation redirect: {rule}")
    redirects.add(source)
    require(not local_file(source).exists(), f"Retired maintainer page is still published: {source}")
    destination = local_file(target)
    require(destination.is_file() and urlsplit(target).path == "/maintainers/",
            f"Redirect must resolve to the maintainer page: {rule}")
    fragment = urlsplit(target).fragment
    require(fragment in Page(destination.read_text()).ids, f"Missing maintainer section: {target}")
print(f"Verified {checked} pages: metadata, JSON-LD, social image, and local links.")
print(f"Verified {len(redirects)} redirects to maintainer sections.")
