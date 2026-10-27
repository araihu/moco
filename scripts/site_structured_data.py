#!/usr/bin/env python3
"""Add route-specific Schema.org graphs to Margo's static HTML."""
import argparse
import os
from html.parser import HTMLParser
import json
from pathlib import Path
import re

ORIGIN = "https://moco.araihu.com"
APP_ID = ORIGIN + "/#application"
SITE_ID = ORIGIN + "/#website"
SCRIPT_ID = "moco-structured-data"
COLLECTIONS = {"/guides/", "/reference/"}
DOCUMENTATION_AUTHOR = {"@type": "Organization", "name": "AraiHu", "url": "https://araihu.com/"}


class Metadata(HTMLParser):
    def __init__(self, source):
        super().__init__(convert_charrefs=True)
        self.canonical = []
        self.meta = {}
        self.title = ""
        self.heading = ""
        self.scripts = []
        self.capture = None
        self.feed(source)

    def handle_starttag(self, tag, attrs):
        attrs = dict(attrs)
        if tag == "link" and attrs.get("rel") == "canonical":
            self.canonical.append(attrs.get("href", ""))
        if tag == "meta":
            self.meta[attrs.get("name", attrs.get("property", ""))] = attrs.get("content", "")
        if tag in ("title", "h1"):
            self.capture = tag
        if tag == "script" and attrs.get("type") == "application/ld+json":
            self.scripts.append("")
            self.capture = "jsonld"

    def handle_endtag(self, tag):
        if tag in ("title", "h1", "script"):
            self.capture = None

    def handle_data(self, data):
        if self.capture == "title":
            self.title += data
        elif self.capture == "h1":
            self.heading += data
        elif self.capture == "jsonld":
            self.scripts[-1] += data


def page_metadata(path, root):
    page = Metadata(path.read_text())
    if "noindex" in page.meta.get("robots", ""):
        return None
    route = "/" + path.relative_to(root).as_posix().removesuffix("index.html")
    expected = ORIGIN + route
    if page.canonical != [expected] or not page.meta.get("description"):
        raise ValueError(f"{path}: missing description or canonical differs from {expected}")
    return page, route


def graph_for(page, route, app, version, pages):
    url = ORIGIN + route
    graph = [
        {"@type": "WebSite", "@id": SITE_ID, "url": ORIGIN + "/", "name": "Mocó",
         "inLanguage": "en", "about": {"@id": APP_ID}},
        {"@type": "SoftwareApplication", "@id": APP_ID, "url": ORIGIN + "/",
         "name": "Mocó", "softwareVersion": version, **app},
        {"@type": "WebPage" if route == "/" else "CollectionPage" if route in COLLECTIONS else "WebPage",
         "@id": url + "#webpage", "url": url, "name": page.title.strip(),
         "description": page.meta["description"], "inLanguage": "en",
         "isPartOf": {"@id": SITE_ID}, "about": {"@id": APP_ID}},
    ]
    web = graph[-1]
    if route == "/":
        web["mainEntity"] = {"@id": APP_ID}
    elif route in COLLECTIONS:
        web["hasPart"] = [{"@type": "TechArticle", "@id": ORIGIN + child + "#article",
                           "url": ORIGIN + child, "headline": item.heading.strip() or item.title.strip(),
                           "image": item.meta["og:image"],
                           "author": DOCUMENTATION_AUTHOR,
                           "about": {"@id": APP_ID}}
                          for item, child in pages if child.startswith(route) and child != route]
        if route == "/reference/":
            web["hasPart"].append({"@type": "APIReference",
                                  "@id": ORIGIN + "/openapi/catalogs/moco/documents/openapi/#reference",
                                  "url": ORIGIN + "/openapi/catalogs/moco/documents/openapi/",
                                  "headline": "Mocó API reference", "author": DOCUMENTATION_AUTHOR,
                                  "about": {"@id": APP_ID}})
    else:
        article_id = url + "#article"
        web["mainEntity"] = {"@id": article_id}
        graph.append({"@type": "TechArticle", "@id": article_id, "url": url,
                      "headline": page.heading.strip() or page.title.strip(),
                      "description": page.meta["description"], "inLanguage": "en",
                      "image": page.meta["og:image"],
                      "author": DOCUMENTATION_AUTHOR,
                      "about": {"@id": APP_ID}, "isPartOf": {"@id": SITE_ID},
                      "mainEntityOfPage": {"@id": web["@id"]}})
    return {"@context": "https://schema.org", "@graph": graph}


def serialize(graph):
    # JSON-LD is script data, not HTML. Prevent an authored string closing the tag.
    return json.dumps(graph, ensure_ascii=False, separators=(",", ":")).replace("<", "\\u003c").replace("&", "\\u0026")


def inject(source, graph):
    source = re.sub(r'<script id="moco-structured-data" type="application/ld\+json">.*?</script>',
                    "", source, flags=re.DOTALL)
    if source.count("</head>") != 1:
        raise ValueError("Expected exactly one closing head tag")
    return source.replace("</head>", f'<script id="{SCRIPT_ID}" type="application/ld+json">{serialize(graph)}</script></head>')


def validate(path, root):
    metadata = page_metadata(path, root)
    if metadata is None:
        return
    page, route = metadata
    if len(page.scripts) != 1:
        raise ValueError(f"{route}: expected exactly one JSON-LD script")
    document = json.loads(page.scripts[0])
    if document.get("@context") != "https://schema.org":
        raise ValueError(f"{route}: incorrect Schema.org context")
    nodes = document["@graph"]
    ids = [node["@id"] for node in nodes]
    if len(ids) != len(set(ids)):
        raise ValueError(f"{route}: duplicate JSON-LD entity identifiers")
    indexed = {node["@id"]: node for node in nodes}
    app = indexed[APP_ID]
    web = indexed[ORIGIN + route + "#webpage"]
    if "aggregateRating" in app or "review" in app or "priceCurrency" in app.get("offers", {}):
        raise ValueError(f"{route}: ratings, reviews, and offer currency must remain absent")
    for node in nodes + web.get("hasPart", []):
        if node.get("@type") in ("TechArticle", "APIReference") and node.get("author") != DOCUMENTATION_AUTHOR:
            raise ValueError(f"{route}: documentation author must be AraiHu")
    if (app["@type"] != "SoftwareApplication" or app["name"] != "Mocó"
            or not app.get("softwareVersion") or not app.get("featureList")
            or indexed[SITE_ID]["@type"] != "WebSite"
            or web["url"] != page.canonical[0]
            or web["description"] != page.meta["description"]):
        raise ValueError(f"{route}: JSON-LD identity or metadata mismatch")
    if route in COLLECTIONS:
        if web["@type"] != "CollectionPage" or not web.get("hasPart"):
            raise ValueError(f"{route}: missing documentation collection")
    elif route != "/":
        article = indexed[ORIGIN + route + "#article"]
        if (article["@type"] != "TechArticle" or article["headline"] != page.heading.strip()
                or article["image"] != page.meta["og:image"]
                or article["mainEntityOfPage"] != {"@id": web["@id"]}):
            raise ValueError(f"{route}: incorrect technical article")


def enrich(root):
    app = json.loads(Path("site/structured-data.json").read_text())
    version = os.environ.get("SITE_VERSION") or re.search(r"^  version: (\S+)$", Path("site.yaml").read_text(), re.MULTILINE).group(1)
    # Manja HTML is covered by its own byte-level export receipts. Leave it intact.
    candidates = sorted(p for p in root.rglob("*.html") if "openapi" not in p.relative_to(root).parts)
    entries = [(p, page_metadata(p, root)) for p in candidates]
    pages = [metadata for _, metadata in entries if metadata is not None]
    for path, metadata in entries:
        if metadata is not None:
            page, route = metadata
            path.write_text(inject(path.read_text(), graph_for(page, route, app, version, pages)))
    print(f"Added static JSON-LD to {len(pages)} pages.")


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("root", type=Path)
    enrich(parser.parse_args().root)
