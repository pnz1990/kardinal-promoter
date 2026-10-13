# Copyright 2026 The kardinal-promoter Authors.
# Licensed under the Apache License, Version 2.0
"""The docs component map: what runs where and what talks to what, in the station map's
visual language.

station_map.py draws pipelines. A component architecture is not one, so it gets its own
block, drawn by this module with the same parts: the UI's frames (group__frame), station
dots and labels (station__dot, station__name, station__sub), edges (station-map__edge),
its chips (k-chip), the design tokens and the fonts. A page declares:

    ```component-map
    title: kardinal's components
    zones:
      cluster: {name: Kubernetes cluster}
      system: {name: kardinal-system, in: cluster}
    components:
      controller: {name: kardinal-controller, at: [1, 1], zone: system, chip: Deployment, kardinal: true,
                   role: [reconcilers, UI and API], items: [promotion: bundle · pipeline]}
      git: {name: Git repository, at: [3, 1], role: [env branches]}
    edges:
      - controller -> git: git push (HTTPS, SSH)
      - ui <-> api: HTTPS
    ```

kardinal: true marks what kardinal itself runs: its dot is cardinal red.
at: [column, row] places a component on a grid (span: n widens it over n columns).
A zone is the box around its components and the zones in it (in:). Columns and rows
take the size of what is in them; the gaps take the size of the edge labels that cross
them and of the zone borders they hold. An edge between two columns runs across the gap
between them, with one bend when the rows differ; an edge within a column runs down the
gap between the rows. The build fails when an edge or a label runs over a component
another edge does not end at, or two labels overlap, so the author moves a component.
"""

from __future__ import annotations

import hashlib
import html
import json
import re
from typing import Any, Dict, List, Optional, Sequence, Tuple

Point = Tuple[float, float]


class SpecError(ValueError):
    pass


def _e(s: str) -> str:
    return html.escape(s, quote=True)


def _n(v: float) -> str:
    r = round(v, 1)
    return str(int(r)) if r == int(r) else str(r)


class Metrics:
    """The lengths the map is laid out with, from the design tokens."""

    def __init__(self, tokens: Dict[str, Any], px: Any, mono_em: float, display_em: float, r: float, chamfer: float, rank_sep: float, tick: float):
        space = lambda k: float(tokens["space"][k]["$value"]) * 4  # noqa: E731  (a space step is 0.25rem)
        text = {k: px(v["$value"]) for k, v in tokens["text"].items() if k in ("2xs", "xs", "sm", "md")}
        leading = {k: float(v["$value"]) for k, v in tokens["leading"].items() if k in ("2xs", "xs", "sm")}
        self.mono_em, self.display_em = mono_em, display_em
        self.r, self.chamfer, self.tick = r, chamfer, tick
        self.pad = space("3")
        self.zpad = space("3")
        self.gap_x = space("12")
        self.gap_y = space("12")
        self.lane = space("2")
        # A bend runs down this far before its target: two chamfers and a lane.
        self.bend = chamfer * 2 + self.lane
        self.name_px, self.name_line = text["sm"], text["sm"] * leading["sm"]
        self.sub_px, self.sub_line = text["xs"], text["xs"] * leading["xs"]
        self.cap_px, self.cap_line = text["2xs"], text["2xs"] * leading["2xs"]
        self.chip_px = text["xs"]
        self.chip_pad = space("1")
        self.chip_gap = space("2")
        # Edge labels at text-2xs, as the station map's gate labels.
        self.label_px = text["2xs"]
        self.label_gap = space("1")
        self.ascent = 0.8
        # The zone label is caps with --k-tracking-caps: wider than the label face.
        self.caps_em = display_em + 0.1

    def mono(self, s: str, px: float) -> float:
        return len(s) * px * self.mono_em

    def display(self, s: str, px: float) -> float:
        return len(s) * px * self.display_em


class Zone:
    def __init__(self, zid: str, raw: Any):
        if isinstance(raw, str):
            raw = {"name": raw}
        if not isinstance(raw, dict):
            raise SpecError(f"zone {zid}: a name or {{name, in, kind}}")
        self.id = zid
        self.name = str(raw.get("name", zid))
        self.parent: Optional[str] = str(raw["in"]) if raw.get("in") else None
        self.kind = str(raw.get("kind", "namespace" if self.parent else "outside"))
        if self.kind not in ("cluster", "namespace", "outside"):
            raise SpecError(f"zone {zid}: kind is cluster, namespace or outside")
        self.c0 = self.r0 = 10 ** 6
        self.c1 = self.r1 = -1
        self.depth = 0
        self.rect = (0.0, 0.0, 0.0, 0.0)


class Box:
    def __init__(self, cid: str, raw: Any, m: Metrics):
        if not isinstance(raw, dict) or "at" not in raw:
            raise SpecError(f"component {cid}: {{name, at: [column, row], ...}}")
        unknown = set(raw) - {"name", "at", "span", "zone", "role", "items", "chip", "rows", "kardinal"}
        if unknown:
            raise SpecError(f"component {cid}: unknown fields {sorted(unknown)}")
        self.id = cid
        self.name = str(raw.get("name", cid))
        at = raw["at"]
        if not (isinstance(at, list) and len(at) == 2):
            raise SpecError(f"component {cid}: at is [column, row]")
        self.col, self.row = int(at[0]), int(at[1])
        self.span = int(raw.get("span", 1))
        self.rows = int(raw.get("rows", 1))
        self.zone: Optional[str] = str(raw["zone"]) if raw.get("zone") else None
        role = raw.get("role", [])
        self.role = [str(x) for x in ([role] if isinstance(role, str) else role)]
        self.items: List[Tuple[str, str]] = []
        for it in raw.get("items", []):
            if isinstance(it, dict) and len(it) == 1:
                k, v = next(iter(it.items()))
                # A list of members runs on over lines, under the first.
                vs = [str(x) for x in v] if isinstance(v, list) else [str(v)]
                self.items.append((str(k), vs[0]))
                self.items += [("", x) for x in vs[1:]]
            else:
                raise SpecError(f"component {cid}: an item is 'group: members' or 'group: [members, ...]'")
        self.chip = str(raw["chip"]) if raw.get("chip") else None
        # What kardinal itself runs: the cardinal-red dot, as the station map's thread.
        self.kardinal = bool(raw.get("kardinal", False))
        self.m = m
        self.x = self.y = 0.0
        self.w, self.h = self.content()

    def content(self) -> Tuple[float, float]:
        m = self.m
        head = m.r * 2 + m.chip_gap + m.display(self.name, m.name_px)
        if self.chip:
            head += m.chip_gap + self.chip_w
        group = max([0.0] + [m.display(g, m.sub_px) for g, _ in self.items])
        lines = [m.mono(t, m.sub_px) for t in self.role]
        lines += [group + m.chip_gap + m.mono(v, m.sub_px) for _, v in self.items]
        w = max([head] + lines) + 2 * m.pad
        h = 2 * m.pad + m.name_line + m.sub_line * (len(self.role) + len(self.items))
        if self.items:
            h += m.chip_gap
        return w, h

    @property
    def chip_w(self) -> float:
        # The chip's face is the semibold label face: a little wider than a name.
        return len(self.chip or "") * self.m.chip_px * (self.m.display_em + 0.08) + 2 * self.m.chip_pad

    @property
    def rect(self) -> Tuple[float, float, float, float]:
        return self.x, self.y, self.x + self.w, self.y + self.h


class Edge:
    def __init__(self, a: str, b: str, label: str, both: bool):
        self.a, self.b, self.label, self.both = a, b, label, both
        self.points: List[Point] = []
        self.label_at: Optional[Tuple[float, float, str]] = None  # x, y, anchor
        self.label_box = (0.0, 0.0, 0.0, 0.0)


EDGE = re.compile(r"\s*(.+?)\s*(<->|->)\s*(.+?)\s*")


class Spec:
    def __init__(self, raw: Any, m: Metrics):
        if not isinstance(raw, dict):
            raise SpecError("a component map is a YAML mapping")
        unknown = set(raw) - {"title", "desc", "caption", "zones", "components", "edges"}
        if unknown:
            raise SpecError(f"unknown fields {sorted(unknown)}")
        if not raw.get("title"):
            raise SpecError("title is required (the SVG's accessible name)")
        self.title = str(raw["title"])
        self.desc = str(raw["desc"]) if raw.get("desc") else None
        self.caption = str(raw["caption"]) if raw.get("caption") else None
        self.zones = {str(k): Zone(str(k), v) for k, v in (raw.get("zones") or {}).items()}
        for z in self.zones.values():
            if z.parent and z.parent not in self.zones:
                raise SpecError(f"zone {z.id}: unknown zone {z.parent!r}")
        self.boxes = {str(k): Box(str(k), v, m) for k, v in (raw.get("components") or {}).items()}
        if not self.boxes:
            raise SpecError("components: at least one")
        for b in self.boxes.values():
            if b.zone and b.zone not in self.zones:
                raise SpecError(f"component {b.id}: unknown zone {b.zone!r}")
        self.edges: List[Edge] = []
        for e in raw.get("edges") or []:
            if isinstance(e, dict) and len(e) == 1:
                k, label = next(iter(e.items()))
            elif isinstance(e, str):
                k, label = e, ""
            else:
                raise SpecError(f"an edge is 'a -> b: label': {e!r}")
            mm = EDGE.fullmatch(str(k))
            if not mm:
                raise SpecError(f"an edge is 'a -> b: label' or 'a <-> b: label': {e!r}")
            a, arrow, b = mm.groups()
            for s in (a, b):
                if s not in self.boxes:
                    raise SpecError(f"edge {k!r}: unknown component {s!r}")
            self.edges.append(Edge(a, b, str(label or ""), arrow == "<->"))

    def chain(self, zid: Optional[str]) -> List[str]:
        out: List[str] = []
        while zid:
            out.append(zid)
            zid = self.zones[zid].parent
        return out


# ── layout ───────────────────────────────────────────────────────────────────


def _overlap(a0: float, a1: float, b0: float, b1: float) -> bool:
    return a0 < b1 and b0 < a1


def layout(spec: Spec, m: Metrics) -> None:
    boxes = list(spec.boxes.values())
    # Zones: the cells of their components and of the zones in them.
    for b in boxes:
        for depth, zid in enumerate(reversed(spec.chain(b.zone))):
            z = spec.zones[zid]
            z.c0, z.c1 = min(z.c0, b.col), max(z.c1, b.col + b.span - 1)
            z.r0, z.r1 = min(z.r0, b.row), max(z.r1, b.row + b.rows - 1)
            z.depth = depth
    for z in spec.zones.values():
        if z.c1 < 0:
            raise SpecError(f"zone {z.id} holds no component")
    for b in boxes:
        for z in spec.zones.values():
            inside = z.c0 <= b.col and b.col + b.span - 1 <= z.c1 and z.r0 <= b.row and b.row + b.rows - 1 <= z.r1
            if inside and z.id not in spec.chain(b.zone):
                raise SpecError(f"component {b.id} sits in the cells of zone {z.id}, which it is not in: move it")
    cols = max(b.col + b.span for b in boxes)
    rows = max(b.row + b.rows for b in boxes)

    # Column widths and row heights: what is in them (a spanning box shares its width).
    cw = [0.0] * cols
    rh = [0.0] * rows
    for b in boxes:
        if b.span == 1:
            cw[b.col] = max(cw[b.col], b.w)
        if b.rows == 1:
            rh[b.row] = max(rh[b.row], b.h)

    # The zone borders at each side of a column or row: a zone and the zones nested in it
    # that share that side stack their paddings; zones side by side on other rows do not.
    kids: Dict[str, List[Zone]] = {}
    for z in spec.zones.values():
        if z.parent:
            kids.setdefault(z.parent, []).append(z)

    def stack(z: Zone, side: str) -> int:
        return 1 + max([0] + [stack(k, side) for k in kids.get(z.id, []) if getattr(k, side) == getattr(z, side)])

    def border(c: int, side: str) -> int:
        return max([0] + [stack(z, side) for z in spec.zones.values() if getattr(z, side) == c])

    ztop = m.zpad + m.cap_line  # a zone's label sits in its top padding
    # Edge labels: an edge's label sits in the first gap it crosses, beside the source,
    # clear of a bend that goes down by the target; that gap must hold it.
    need_x = [m.gap_x] * (cols + 1)
    # The bends into a box's side each take a lane of the gap before it, and a straight
    # edge's label into that box must clear them.
    into: Dict[str, int] = {}
    for e in spec.edges:
        a, b = spec.boxes[e.a], spec.boxes[e.b]
        if not _overlap(a.col, a.col + a.span, b.col, b.col + b.span):
            into[b.id] = into.get(b.id, 0) + 1
    for e in spec.edges:
        a, b = spec.boxes[e.a], spec.boxes[e.b]
        adjacent = b.col == a.col + a.span or a.col == b.col + b.span
        if e.label and adjacent and _overlap(a.row, a.row + a.rows, b.row, b.row + b.rows):
            g = max(a.col, b.col)
            lanes = into.get(b.id, 0) - 1
            need_x[g] = max(need_x[g], m.mono(e.label, m.label_px) + 2 * m.lane + m.bend + lanes * m.lane * 2)

    xs = [0.0] * (cols + 1)  # the left edge of each column
    x = 0.0
    for c in range(cols):
        x += border(c, "c0") * m.zpad
        if c > 0:
            x += need_x[c]
        xs[c] = x
        x += cw[c] + border(c, "c1") * m.zpad
    ys = [0.0] * (rows + 1)
    y = 0.0
    for r in range(rows):
        y += border(r, "r0") * ztop
        if r > 0:
            y += m.gap_y
        ys[r] = y
        y += rh[r] + border(r, "r1") * m.zpad
    xs[cols], ys[rows] = x, y

    for b in boxes:
        right = xs[b.col + b.span - 1] + cw[b.col + b.span - 1]
        b.x = xs[b.col]
        b.w = max(b.w, right - b.x)
        b.y = ys[b.row]
        if b.rows > 1:
            b.h = max(b.h, ys[b.row + b.rows - 1] + rh[b.row + b.rows - 1] - b.y)

    # Zone rectangles: the cells, out by its stack of paddings on each side.
    for z in spec.zones.values():
        x0 = xs[z.c0] - m.zpad * stack(z, "c0")
        x1 = xs[z.c1] + cw[z.c1] + m.zpad * stack(z, "c1")
        y0 = ys[z.r0] - ztop * stack(z, "r0")
        y1 = ys[z.r1] + rh[z.r1] + m.zpad * stack(z, "r1")
        z.rect = (x0, y0, x1, y1)
    zs = list(spec.zones.values())
    for i, a in enumerate(zs):
        for o in zs[i + 1:]:
            if a.parent == o.parent and _overlap(a.rect[0], a.rect[2], o.rect[0], o.rect[2]) and _overlap(a.rect[1], a.rect[3], o.rect[1], o.rect[3]):
                raise SpecError(f"zones {a.id} and {o.id} overlap: move their components apart")

    _route(spec, m, xs, cw)


def _ports(n: int, lo: float, hi: float) -> List[float]:
    return [lo + (hi - lo) * (i + 1) / (n + 1) for i in range(n)]


def _route(spec: Spec, m: Metrics, xs: List[float], cw: List[float]) -> None:
    # Which side of each box an edge leaves or enters by, and the order along it.
    sides: Dict[Tuple[str, str], List[Tuple[float, Edge, str]]] = {}
    plan: Dict[int, Tuple[str, str]] = {}
    for e in spec.edges:
        a, b = spec.boxes[e.a], spec.boxes[e.b]
        if _overlap(a.col, a.col + a.span, b.col, b.col + b.span):
            sa, sb = ("bottom", "top") if b.row > a.row else ("top", "bottom")
        else:
            sa, sb = ("right", "left") if b.col > a.col else ("left", "right")
        plan[id(e)] = (sa, sb)
        ca = (b.x + b.w / 2) if sa in ("top", "bottom") else (b.y + b.h / 2)
        cb = (a.x + a.w / 2) if sb in ("top", "bottom") else (a.y + a.h / 2)
        sides.setdefault((a.id, sa), []).append((ca, e, "a"))
        sides.setdefault((b.id, sb), []).append((cb, e, "b"))
    port: Dict[Tuple[int, str], float] = {}
    for (bid, side), lst in sides.items():
        bx = spec.boxes[bid]
        lst.sort(key=lambda t: t[0])
        lo, hi = (bx.x, bx.x + bx.w) if side in ("top", "bottom") else (bx.y + m.pad, bx.y + bx.h - m.pad)
        for p, (_, e, end) in zip(_ports(len(lst), lo, hi), lst):
            port[(id(e), end)] = p

    # The lane of each bend: the bends into one side of a column share its gap. Of those
    # that come down, the one entering highest runs closest to the target; of those that
    # come up, the one entering lowest. So no bend's last run crosses another's vertical.
    def straight(e: Edge) -> Optional[float]:
        a, b = spec.boxes[e.a], spec.boxes[e.b]
        pa, pb = port[(id(e), "a")], port[(id(e), "b")]
        # Where both boxes span: as near the target's own port as that allows, so the
        # target's other ports keep their room.
        lo, hi = max(a.y, b.y) + m.pad, min(a.y + a.h, b.y + b.h) - m.pad
        if lo > hi:
            return None
        return min(max(pb, lo), hi)

    # Straight edges keep their height; the other ports on each side are spread again in
    # the room between them, in the same order, so a bend never leaves on the far side
    # of a straight edge and crosses it.
    fixed = {id(e): straight(e) for e in spec.edges if plan[id(e)][0] in ("left", "right") and straight(e) is not None}
    for (bid, side), lst in sides.items():
        if side in ("top", "bottom"):
            continue
        bx = spec.boxes[bid]
        lo = bx.y + m.pad
        run: List[Tuple[Edge, str]] = []
        for _, e, end in lst + [(0.0, None, "")]:  # type: ignore[list-item]
            y = fixed.get(id(e)) if e is not None else bx.y + bx.h - m.pad
            if y is None:
                run.append((e, end))
                continue
            for p, (re_, rend) in zip(_ports(len(run), lo, y), run):
                port[(id(re_), rend)] = p
            if e is not None:
                port[(id(e), end)] = y
            lo, run = y, []

    lane: Dict[int, int] = {}
    groups: Dict[Tuple[str, str], List[Edge]] = {}
    for e in spec.edges:
        sa, sb = plan[id(e)]
        if sa in ("left", "right") and straight(e) is None:
            b = spec.boxes[e.b]
            xb = b.x if sb == "left" else b.x + b.w
            groups.setdefault((str(round(xb)), sb), []).append(e)
    for lst in groups.values():
        down = sorted([e for e in lst if port[(id(e), "a")] < port[(id(e), "b")]], key=lambda e: port[(id(e), "b")])
        up = sorted([e for e in lst if port[(id(e), "a")] >= port[(id(e), "b")]], key=lambda e: -port[(id(e), "b")])
        for k, e in enumerate(down + up):
            lane[id(e)] = k

    gaps: Dict[int, Optional[Tuple[float, float]]] = {}
    for e in spec.edges:
        a, b = spec.boxes[e.a], spec.boxes[e.b]
        sa, sb = plan[id(e)]
        pa, pb = port[(id(e), "a")], port[(id(e), "b")]
        if sa in ("left", "right"):
            xa = a.x + a.w if sa == "right" else a.x
            xb = b.x if sb == "left" else b.x + b.w
            y = straight(e)
            if y is not None:
                # Straight, at the target's port when the source spans it: each edge
                # keeps its own entry point.
                pts = [(xa, y), (xb, y)]
            else:
                off = m.bend + lane[id(e)] * m.lane * 2
                gx = xb - off if sb == "left" else xb + off
                pts = [(xa, pa), (gx, pa), (gx, pb), (xb, pb)]
        else:
            ya = a.y + a.h if sa == "bottom" else a.y
            yb = b.y if sb == "top" else b.y + b.h
            if b.x <= pa <= b.x + b.w:
                pts = [(pa, ya), (pa, yb)]
            elif a.x <= pb <= a.x + a.w:
                pts = [(pb, ya), (pb, yb)]
            else:
                gy = (ya + yb) / 2
                pts = [(pa, ya), (pa, gy), (pb, gy), (pb, yb)]
        e.points = pts
        gap = None
        if sa in ("left", "right"):
            g = a.col + a.span if sa == "right" else a.col
            gap = (a.x + a.w, xs[g]) if sa == "right" else (xs[g - 1] + cw[g - 1], a.x)
        gaps[id(e)] = gap
    _place_labels(spec, m, gaps)
    _check(spec, m)


Rect = Tuple[float, float, float, float]


def _outward(mid: float, room: float, step: float) -> List[float]:
    """mid, then mid ± step, ± 2 step, ... out to ± room."""
    out = [mid]
    k = 1
    while k * step <= room:
        out += [mid - k * step, mid + k * step]
        k += 1
    return out


def _label_candidates(e: Edge, m: Metrics, gap: Optional[Tuple[float, float]]) -> List[Tuple[Tuple[float, float, str], Rect]]:
    """Where an edge's label may go, best first: over the run out of the source (in the
    gap beside it), under it, over the run into the target, then beside a vertical run."""
    pts = e.points
    w, h = m.mono(e.label, m.label_px), m.label_px
    out: List[Tuple[Tuple[float, float, str], Rect]] = []

    def over_under(i: int, clip: Optional[Tuple[float, float]]) -> None:
        (x0, y0), (x1, _) = pts[i], pts[i + 1]
        lo, hi = sorted((x0, x1))
        # Clear of the arrowhead and of a chamfered bend at either end.
        lo, hi = lo + m.chamfer, hi - m.chamfer
        if clip is not None:
            lo, hi = max(lo, clip[0]), min(hi, clip[1])
        if hi - lo < w:
            return
        # The middle first, then further out along the run, either way, a lane at a time.
        mid, room = (lo + hi) / 2, (hi - lo - w) / 2
        for cx in _outward(mid, room, m.label_gap):
            for top in (y0 - m.label_gap - h, y0 + m.label_gap):
                out.append(((cx, top + h * m.ascent, "middle"), (cx - w / 2, top, cx + w / 2, top + h)))

    horiz = [i for i in range(len(pts) - 1) if abs(pts[i + 1][1] - pts[i][1]) < 0.5 and abs(pts[i + 1][0] - pts[i][0]) > 0.5]
    if horiz and horiz[0] == 0:
        over_under(0, None)
    if horiz and horiz[-1] != 0:
        over_under(horiz[-1], None)
    for i in range(len(pts) - 1):
        (x0, y0), (x1, y1) = pts[i], pts[i + 1]
        if abs(x1 - x0) > 0.5 or abs(y1 - y0) < h + 2 * m.label_gap:
            continue
        lo, hi = sorted((y0, y1))
        mid, room = (lo + hi) / 2, max(0.0, (hi - lo - h) / 2 - m.chamfer)
        for cy in _outward(mid, room, m.label_gap):
            lx = x0 + m.label_gap * 2
            out.append(((lx, cy + h * m.ascent / 2, "start"), (lx, cy - h / 2, lx + w, cy + h / 2)))
            rx = x0 - m.label_gap * 2
            out.append(((rx, cy + h * m.ascent / 2, "end"), (rx - w, cy - h / 2, rx, cy + h / 2)))
    return out


def _grow(r: Rect, d: float) -> Rect:
    return r[0] - d, r[1] - d, r[2] + d, r[3] + d


def _segments(e: Edge, m: Metrics) -> List[Tuple[Point, Point]]:
    """The segments an edge is drawn with: its bends chamfered."""
    pts = _chamfered(e.points, m.chamfer)
    return list(zip(pts, pts[1:]))


def _cross(p: Point, q: Point, r: Point, s: Point) -> bool:
    """Whether segments pq and rs cross (touching at an end does not count)."""
    def orient(a: Point, b: Point, c: Point) -> float:
        return (b[0] - a[0]) * (c[1] - a[1]) - (b[1] - a[1]) * (c[0] - a[0])
    d1, d2 = orient(r, s, p), orient(r, s, q)
    d3, d4 = orient(p, q, r), orient(p, q, s)
    return ((d1 > 0 > d2) or (d1 < 0 < d2)) and ((d3 > 0 > d4) or (d3 < 0 < d4))


def _seg_rect(p: Point, q: Point, r: Rect) -> bool:
    """Whether segment pq touches rectangle r."""
    x0, y0, x1, y1 = r
    inside = lambda pt: x0 <= pt[0] <= x1 and y0 <= pt[1] <= y1  # noqa: E731
    if inside(p) or inside(q):
        return True
    corners = [(x0, y0), (x1, y0), (x1, y1), (x0, y1)]
    return any(_cross(p, q, corners[i], corners[(i + 1) % 4]) for i in range(4))


def _place_labels(spec: Spec, m: Metrics, gaps: Dict[int, Optional[Tuple[float, float]]]) -> None:
    placed: List[Rect] = []
    # Zone borders are lines too.
    borders: List[Tuple[Point, Point]] = []
    for z in spec.zones.values():
        x0, y0, x1, y1 = z.rect
        c = [(x0, y0), (x1, y0), (x1, y1), (x0, y1)]
        borders += [(c[i], c[(i + 1) % 4]) for i in range(4)]
    for e in spec.edges:
        if not e.label:
            continue
        cands = _label_candidates(e, m, gaps.get(id(e)))
        clear = [c for c in cands
                 if not any(_boxes_hit(c[1], b.rect) for b in spec.boxes.values())
                 and not any(_boxes_hit(c[1], r) for r in placed)]
        # A label touches no line: not another edge's, crossing or along it, and not its own.
        lines = [sg for o in spec.edges for sg in _segments(o, m)] + borders
        clear = [c for c in clear if not any(_seg_rect(p, q, _grow(c[1], 1)) for p, q in lines)]
        pick = (clear or [None])[0]
        if pick is None:
            raise SpecError(f"the label of {e.a} -> {e.b} has no room clear of the lines: move a component or shorten it")
        e.label_at, e.label_box = pick
        placed.append(pick[1])


def _seg_hits(p: Point, q: Point, r: Tuple[float, float, float, float], inset: float) -> bool:
    x0, y0, x1, y1 = r[0] + inset, r[1] + inset, r[2] - inset, r[3] - inset
    if abs(p[1] - q[1]) < 0.5:
        return y0 < p[1] < y1 and _overlap(min(p[0], q[0]), max(p[0], q[0]), x0, x1)
    if abs(p[0] - q[0]) < 0.5:
        return x0 < p[0] < x1 and _overlap(min(p[1], q[1]), max(p[1], q[1]), y0, y1)
    return False


def _boxes_hit(a: Tuple[float, float, float, float], b: Tuple[float, float, float, float]) -> bool:
    return _overlap(a[0], a[2], b[0], b[2]) and _overlap(a[1], a[3], b[1], b[3])


def _check(spec: Spec, m: Metrics) -> None:
    for e in spec.edges:
        for b in spec.boxes.values():
            if b.id in (e.a, e.b):
                continue
            for p, q in zip(e.points, e.points[1:]):
                if _seg_hits(p, q, b.rect, 1):
                    raise SpecError(f"edge {e.a} -> {e.b} runs over {b.id}: move a component")
            if e.label and _boxes_hit(e.label_box, b.rect):
                raise SpecError(f"the label of {e.a} -> {e.b} runs over {b.id}: move a component")
    for i, e in enumerate(spec.edges):
        for o in spec.edges[i + 1:]:
            if not {e.a, e.b} & {o.a, o.b}:
                continue
            if any(_cross(p, q, r, t) for p, q in _segments(e, m) for r, t in _segments(o, m)):
                raise SpecError(f"edges {e.a} -> {e.b} and {o.a} -> {o.b} share a component and cross: move a component")
    labelled = [e for e in spec.edges if e.label]
    for i, e in enumerate(labelled):
        for o in labelled[i + 1:]:
            if _boxes_hit(e.label_box, o.label_box):
                raise SpecError(f"the labels of {e.a} -> {e.b} and {o.a} -> {o.b} overlap: move a component")


# ── rendering ────────────────────────────────────────────────────────────────


def _chamfered(pts: Sequence[Point], c: float) -> List[Point]:
    """The orthogonal polyline with each corner cut at 45°: the station map's bends."""
    if len(pts) < 3:
        return list(pts)
    out = [pts[0]]
    for i in range(1, len(pts) - 1):
        (x0, y0), (x1, y1), (x2, y2) = pts[i - 1], pts[i], pts[i + 1]
        la = abs(x1 - x0) + abs(y1 - y0)
        lb = abs(x2 - x1) + abs(y2 - y1)
        k = min(c, la / 2, lb / 2)
        ux, uy = ((x1 - x0) / la, (y1 - y0) / la) if la else (0, 0)
        vx, vy = ((x2 - x1) / lb, (y2 - y1) / lb) if lb else (0, 0)
        out += [(x1 - ux * k, y1 - uy * k), (x1 + vx * k, y1 + vy * k)]
    out.append(pts[-1])
    return out


def _path(pts: Sequence[Point]) -> str:
    return " ".join(f"{'M' if i == 0 else 'L'}{_n(x)} {_n(y)}" for i, (x, y) in enumerate(pts))


def _arrow(p: Point, q: Point, size: float) -> str:
    """A chevron at q, pointing from p to q."""
    dx, dy = q[0] - p[0], q[1] - p[1]
    ln = (dx * dx + dy * dy) ** 0.5 or 1
    ux, uy = dx / ln, dy / ln
    bx, by = q[0] - ux * size, q[1] - uy * size
    nx, ny = -uy * size * 0.6, ux * size * 0.6
    return f"M{_n(bx + nx)} {_n(by + ny)} L{_n(q[0])} {_n(q[1])} L{_n(bx - nx)} {_n(by - ny)}"


def _describe(spec: Spec) -> str:
    parts = []
    for z in spec.zones.values():
        inside = [b.name for b in spec.boxes.values() if b.zone == z.id]
        if inside:
            parts.append(f"In {z.name}: " + ", ".join(inside) + ".")
    for b in spec.boxes.values():
        bits = b.role + [f"{g}: {v}" if g else v for g, v in b.items]
        if bits:
            parts.append(f"{b.name}: " + "; ".join(bits) + ".")
    for e in spec.edges:
        verb = "and" if e.both else "to"
        parts.append(f"{spec.boxes[e.a].name} {verb} {spec.boxes[e.b].name}" + (f": {e.label}." if e.label else "."))
    return " ".join(parts)


def render(raw: Any, m: Metrics) -> str:
    spec = Spec(raw, m)
    layout(spec, m)
    uid = "k-cmap-" + hashlib.sha1(json.dumps(raw, sort_keys=True, default=str).encode()).hexdigest()[:8]

    zones = []
    for z in sorted(spec.zones.values(), key=lambda z: z.depth):
        x0, y0, x1, y1 = z.rect
        zones.append(
            f'<g class="k-cmap__zone" data-kind="{z.kind}">'
            f'<rect x="{_n(x0)}" y="{_n(y0)}" width="{_n(x1 - x0)}" height="{_n(y1 - y0)}"/>'
            f'<text x="{_n(x0 + m.zpad)}" y="{_n(y0 + m.zpad / 2 + m.cap_px * m.ascent + m.cap_px * 0.15)}" class="k-cmap__zone-label">{_e(z.name)}</text></g>'
        )

    edges, labels = [], []
    for e in spec.edges:
        pts = _chamfered(e.points, m.chamfer)
        d = _path(pts) + " " + _arrow(pts[-2], pts[-1], m.tick * 0.6)
        if e.both:
            d += " " + _arrow(pts[1], pts[0], m.tick * 0.6)
        edges.append(f'<path d="{d}" class="station-map__edge k-cmap__edge"/>')
        if e.label and e.label_at:
            lx, ly, anchor = e.label_at
            # The label's halo is the colour of the zone it sits on.
            bx = (e.label_box[0] + e.label_box[2]) / 2
            by = (e.label_box[1] + e.label_box[3]) / 2
            on = [z for z in spec.zones.values() if z.rect[0] <= bx <= z.rect[2] and z.rect[1] <= by <= z.rect[3]]
            kind = max(on, key=lambda z: z.depth).kind if on else "outside"
            labels.append(f'<text x="{_n(lx)}" y="{_n(ly)}" text-anchor="{anchor}" class="k-cmap__edge-label" data-on="{kind}">{_e(e.label)}</text>')

    boxes = []
    for b in spec.boxes.values():
        x, y = b.x, b.y
        cy = y + m.pad + m.name_line / 2
        nx = x + m.pad + m.r * 2 + m.chip_gap
        parts = [
            f'<rect class="group__frame k-cmap__frame" x="{_n(x)}" y="{_n(y)}" width="{_n(b.w)}" height="{_n(b.h)}"/>',
            f'<circle class="station__dot" cx="{_n(x + m.pad + m.r)}" cy="{_n(cy)}" r="{_n(m.r)}"/>',
            f'<text x="{_n(nx)}" y="{_n(cy + m.name_px * 0.35)}" class="station__name">{_e(b.name)}</text>',
        ]
        if b.chip:
            cx = x + b.w - m.pad - b.chip_w
            ch = m.chip_px + 2 * m.chip_pad
            parts.append(
                f'<g class="k-cmap__chip"><rect x="{_n(cx)}" y="{_n(cy - ch / 2)}" width="{_n(b.chip_w)}" height="{_n(ch)}"/>'
                f'<text x="{_n(cx + m.chip_pad)}" y="{_n(cy + m.chip_px * 0.35)}">{_e(b.chip)}</text></g>'
            )
        ly = y + m.pad + m.name_line
        for t in b.role:
            ly += m.sub_line
            parts.append(f'<text x="{_n(x + m.pad)}" y="{_n(ly - m.sub_px * 0.3)}" class="station__sub">{_e(t)}</text>')
        if b.items:
            ly += m.chip_gap
            gw = max(m.display(g, m.sub_px) for g, _ in b.items)
            for g, v in b.items:
                ly += m.sub_line
                if g:
                    parts.append(f'<text x="{_n(x + m.pad)}" y="{_n(ly - m.sub_px * 0.3)}" class="k-cmap__group">{_e(g)}</text>')
                parts.append(f'<text x="{_n(x + m.pad + gw + m.chip_gap)}" y="{_n(ly - m.sub_px * 0.3)}" class="station__sub">{_e(v)}</text>')
        mine = ' data-kardinal=""' if b.kardinal else ""
        boxes.append(f'<g class="station k-cmap__box" data-tone="verified" data-component="{_e(b.id)}"{mine}>{"".join(parts)}</g>')

    rects = [z.rect for z in spec.zones.values()] + [b.rect for b in spec.boxes.values()] + [e.label_box for e in spec.edges if e.label]
    x0 = min(r[0] for r in rects) - m.lane
    y0 = min(r[1] for r in rects) - m.lane
    w = max(r[2] for r in rects) + m.lane - x0
    h = max(r[3] for r in rects) + m.lane - y0
    desc = spec.desc or _describe(spec)
    tid, did = f"{uid}-title", f"{uid}-desc"
    svg = (
        f'<svg class="station-map__svg k-cmap" viewBox="{_n(x0)} {_n(y0)} {_n(w)} {_n(h)}" width="{_n(w)}" height="{_n(h)}" '
        f'role="img" aria-labelledby="{tid} {did}" focusable="false">'
        f'<title id="{tid}">{_e(spec.title)}</title><desc id="{did}">{_e(desc)}</desc>'
        f'<g>{"".join(zones)}</g><g class="station-map__edges">{"".join(edges)}</g>'
        f'<g class="station-map__nodes">{"".join(boxes)}</g><g>{"".join(labels)}</g>'
        "</svg>"
    )
    # The map scrolls before its text-xs lines would read smaller than text-2xs (the
    # station map's rule; its zone and edge labels are text-2xs already).
    min_w = w * m.cap_px / m.sub_px
    cap = f'<figcaption class="k-map__caption">{_e(spec.caption)}</figcaption>' if spec.caption else ""
    return (
        f'<figure class="k-map k-map--components" style="--map-w: {_n(w)}px; --map-min-w: {_n(min_w)}px">'
        f'<div class="k-map__scroll"><div class="station-map" data-level="map">{svg}</div></div>{cap}</figure>'
    )
